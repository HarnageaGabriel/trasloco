package diag

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var codePattern = regexp.MustCompile(`^TRL-(CFG|SEL|AUTH|HTTP|SRC|PARSE|EXPR|TPL|VAR|NAME|CONV|CAT|MOD|EMIT|VAL|PLAN|APPLY|SECRET|IDENT|REPO|CUTOVER|LLM|INT)-[0-9]{4}$`)

func parseRegistry(data []byte) ([]Entry, map[string]Entry, error) {
	var raw struct {
		SchemaVersion int              `yaml:"schemaVersion"`
		Codes         []map[string]any `yaml:"codes"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("decode registry: %w", err)
	}
	if raw.SchemaVersion != 1 {
		return nil, nil, fmt.Errorf("schemaVersion = %d, want 1", raw.SchemaVersion)
	}
	if len(raw.Codes) == 0 {
		return nil, nil, fmt.Errorf("codes is empty")
	}
	entries := make([]Entry, 0, len(raw.Codes))
	byCode := make(map[string]Entry, len(raw.Codes))
	for index, object := range raw.Codes {
		encoded, err := yaml.Marshal(object)
		if err != nil {
			return nil, nil, fmt.Errorf("encode codes[%d]: %w", index, err)
		}
		var entry Entry
		if err := yaml.Unmarshal(encoded, &entry); err != nil {
			return nil, nil, fmt.Errorf("decode codes[%d]: %w", index, err)
		}
		if err := validateEntry(entry, object); err != nil {
			return nil, nil, fmt.Errorf("codes[%d]: %w", index, err)
		}
		if _, exists := byCode[entry.Code]; exists {
			return nil, nil, fmt.Errorf("duplicate code %s", entry.Code)
		}
		entries = append(entries, entry)
		byCode[entry.Code] = entry
	}
	for _, entry := range entries {
		if entry.ReplacedBy != nil && *entry.ReplacedBy != "" {
			if _, ok := byCode[*entry.ReplacedBy]; !ok {
				return nil, nil, fmt.Errorf("%s replacedBy unknown code %s", entry.Code, *entry.ReplacedBy)
			}
		}
	}
	sortEntries(entries)
	return entries, byCode, nil
}

func validateEntry(entry Entry, raw map[string]any) error {
	if _, exists := raw["severityOverridable"]; exists {
		return fmt.Errorf("severityOverridable is not allowed; severity is fixed per code")
	}
	required := []string{"code", "area", "title", "severity", "exitClass", "remediation", "since", "status"}
	for _, key := range required {
		value, ok := raw[key]
		if !ok || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if !ValidCode(entry.Code) {
		return fmt.Errorf("invalid code %q", entry.Code)
	}
	if entry.Area != strings.Split(entry.Code, "-")[1] {
		return fmt.Errorf("area %q does not match code %s", entry.Area, entry.Code)
	}
	if len(entry.Title) > 80 {
		return fmt.Errorf("%s title exceeds 80 characters", entry.Code)
	}
	if severityDistance(entry.Severity, entry.Severity) != 0 {
		return fmt.Errorf("%s has invalid severity %q", entry.Code, entry.Severity)
	}
	if entry.Fidelity != nil {
		valid := map[Fidelity]bool{FidelityExact: true, FidelityApproximated: true, FidelityNeedsManual: true, FidelityUnsupported: true}
		if !valid[*entry.Fidelity] {
			return fmt.Errorf("%s has invalid fidelity %q", entry.Code, *entry.Fidelity)
		}
	}
	if entry.Provenance != nil && *entry.Provenance != ProvenanceDeterministic && *entry.Provenance != ProvenanceLLM {
		return fmt.Errorf("%s has invalid provenance %q", entry.Code, *entry.Provenance)
	}
	validExit := map[ExitClass]bool{ExitNone: true, ExitError: true, ExitBlocked: true, ExitDrift: true, ExitUsage: true}
	if !validExit[entry.ExitClass] {
		return fmt.Errorf("%s has invalid exitClass %q", entry.Code, entry.ExitClass)
	}
	if entry.Status != "active" && entry.Status != "deprecated" && entry.Status != "retired" {
		return fmt.Errorf("%s has invalid status %q", entry.Code, entry.Status)
	}
	if entry.Status != "active" {
		if _, ok := raw["replacedBy"]; !ok {
			return fmt.Errorf("%s status %s requires replacedBy key", entry.Code, entry.Status)
		}
		if entry.Status == "deprecated" && (entry.ReplacedBy == nil || *entry.ReplacedBy == "") {
			return fmt.Errorf("%s deprecated status requires a replacement code", entry.Code)
		}
	}
	return nil
}
