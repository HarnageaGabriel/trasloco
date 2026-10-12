package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize"
)

const retirementNotice = "Retired 2026-10-15, removed 2027-03-15 (pipelines fail after removal)."

var classLabels = map[string]string{
	"A": "drop-in replacement",
	"B": "replacement with input changes",
	"C": "manual migration",
	"D": "no replacement",
}

func writeModernizeReport(a *app, format string, result modernizeResult, executionSuccessful bool) error {
	switch format {
	case "text":
		return writeModernizeText(a.io.Out, result)
	case "json":
		return writeJSON(a.io.Out, modernizeJSON(result))
	case "sarif":
		return writeJSON(a.io.Out, makeSARIF(result, executionSuccessful))
	default:
		return errors.New("unreachable report format")
	}
}

// modernizeJSONReport is the document written by --format json. Every array
// is present (never null). filesScanned counts the YAML files read;
// filesSkipped is the part of them that is not an Azure Pipelines file.
type modernizeJSONReport struct {
	Findings     []modernize.Finding `json:"findings"`
	Warnings     []modernizeWarning  `json:"warnings"`
	FilesScanned int                 `json:"filesScanned"`
	FilesSkipped int                 `json:"filesSkipped"`
}

func modernizeJSON(result modernizeResult) modernizeJSONReport {
	report := modernizeJSONReport{Findings: result.findings, Warnings: result.warnings, FilesScanned: result.scanned, FilesSkipped: result.skipped}
	if report.Findings == nil {
		report.Findings = []modernize.Finding{}
	}
	if report.Warnings == nil {
		report.Warnings = []modernizeWarning{}
	}
	return report
}

func writeModernizeText(out io.Writer, result modernizeResult) error {
	var actionable, notes []modernize.Finding
	for _, finding := range result.findings {
		if finding.Actionable() {
			actionable = append(actionable, finding)
		} else {
			notes = append(notes, finding)
		}
	}
	var builder strings.Builder
	previous := ""
	for _, finding := range actionable {
		if previous != "" && previous != finding.Path {
			builder.WriteString("\n")
		}
		previous = finding.Path
		writeTextFinding(&builder, finding)
	}
	if len(notes) > 0 {
		if len(actionable) > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("Notes (not counted: nothing in the pipeline itself to fix)\n")
		for _, finding := range notes {
			writeTextFinding(&builder, finding)
		}
	}
	if len(actionable) == 0 {
		if len(notes) > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "No retired Azure Pipelines tasks found (%d YAML files scanned).\n", result.scanned)
	} else {
		files := make(map[string]bool)
		var a, b, c, d int
		for _, finding := range actionable {
			files[finding.Path] = true
			switch finding.Class {
			case "A":
				a++
			case "B":
				b++
			case "D":
				d++
			default:
				c++
			}
		}
		fmt.Fprintf(&builder, "\nFound %d retired task reference(s) in %d file(s): %d drop-in, %d with input changes, %d manual, %d no replacement. %s\n",
			len(actionable), len(files), a, b, c, d, retirementNotice)
	}
	_, err := io.WriteString(out, builder.String())
	return err
}

func writeTextFinding(builder *strings.Builder, finding modernize.Finding) {
	replacement, label := "unknown", "manual check"
	switch {
	case finding.Code == "TRL-MOD-0010":
		replacement, label = "not a built-in task", "extension task"
	case finding.Class != "":
		replacement, label = "no replacement", classLabels[finding.Class]
		if len(finding.Replacements) > 0 {
			replacement = strings.Join(finding.Replacements, ", ")
			if finding.Class == "D" {
				// Class D has no drop-in replacement; the catalog task is only the
				// closest one after migrating the project or platform.
				replacement = "closest task " + replacement + " after migration"
			}
		}
	}
	fmt.Fprintf(builder, "%s:%d:%d  %s -> %s  [%s]\n    %s\n    More: %s\n",
		finding.Path, finding.Line, finding.Column, finding.RetiredTask, replacement, label, finding.Action, helpOf(finding))
}

func helpOf(finding modernize.Finding) string {
	if finding.Help != "" {
		return finding.Help
	}
	return retirementURL
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}
type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Invocations []sarifInvocation `json:"invocations"`
	Results     []sarifResult     `json:"results"`
}
type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}
type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}
type sarifText struct {
	Text string `json:"text"`
}
type sarifRule struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	ShortDescription     sarifText       `json:"shortDescription"`
	Help                 sarifText       `json:"help"`
	HelpURI              string          `json:"helpUri"`
	DefaultConfiguration sarifRuleConfig `json:"defaultConfiguration"`
}
type sarifRuleConfig struct {
	Level string `json:"level"`
}
type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}
type sarifNotification struct {
	Level      string          `json:"level"`
	Message    sarifText       `json:"message"`
	Descriptor sarifDescriptor `json:"descriptor"`
	Locations  []sarifLocation `json:"locations"`
}
type sarifDescriptor struct {
	ID string `json:"id"`
}
type sarifResult struct {
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level"`
	Message    sarifText       `json:"message"`
	Locations  []sarifLocation `json:"locations"`
	Properties map[string]any  `json:"properties,omitempty"`
}
type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}
type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}
type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}
type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
}

// sarifRuleCodes are the diagnostics the check mode reports as results.
// TRL-MOD-0011 only annotates another result and is not a rule here.
var sarifRuleCodes = []string{"TRL-MOD-0001", "TRL-MOD-0002", "TRL-MOD-0003", "TRL-MOD-0004", "TRL-MOD-0005", "TRL-MOD-0010"}

func sarifLevel(finding modernize.Finding) string {
	switch {
	case !finding.Actionable():
		return "note"
	case finding.Class == "D":
		return "error"
	default:
		return "warning"
	}
}

func sarifRuleLevel(code string) string {
	switch code {
	case "TRL-MOD-0004":
		return "error"
	case "TRL-MOD-0010":
		return "note"
	default:
		return "warning"
	}
}

// sarifURI returns a percent-encoded URI reference for a report path. A
// relative path is relative to %SRCROOT%; an absolute one becomes a file URI.
func sarifURI(path string) (uri, base string) {
	if strings.HasPrefix(path, "/") || (len(path) > 1 && path[1] == ':') {
		absolute := path
		if !strings.HasPrefix(absolute, "/") {
			absolute = "/" + absolute
		}
		return (&url.URL{Scheme: "file", Path: absolute}).String(), ""
	}
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		segments[index] = strings.ReplaceAll(url.PathEscape(segment), ":", "%3A")
	}
	return strings.Join(segments, "/"), "%SRCROOT%"
}

func sarifLocationOf(path string, line, column int) sarifLocation {
	uri, base := sarifURI(path)
	return sarifLocation{PhysicalLocation: sarifPhysicalLocation{
		ArtifactLocation: sarifArtifactLocation{URI: uri, URIBaseID: base},
		Region:           sarifRegion{StartLine: line, StartColumn: column},
	}}
}

func makeSARIF(result modernizeResult, executionSuccessful bool) sarifLog {
	rules := make([]sarifRule, 0, len(sarifRuleCodes))
	for _, code := range sarifRuleCodes {
		entry, _ := diag.Lookup(code)
		rules = append(rules, sarifRule{
			ID: code, Name: code, ShortDescription: sarifText{Text: entry.Title}, Help: sarifText{Text: entry.Remediation},
			HelpURI: retirementURL, DefaultConfiguration: sarifRuleConfig{Level: sarifRuleLevel(code)},
		})
	}
	results := make([]sarifResult, 0, len(result.findings))
	for _, finding := range result.findings {
		message := fmt.Sprintf("%s: %s", finding.RetiredTask, finding.Action)
		properties := map[string]any{"help": helpOf(finding)}
		if finding.Class != "" {
			properties["class"] = finding.Class
		}
		if len(finding.Replacements) > 0 {
			properties["replacements"] = finding.Replacements
		}
		if finding.SourceURL != "" {
			properties["sourceUrl"] = finding.SourceURL
			message += " Source: " + finding.SourceURL
		}
		if len(finding.Diagnostics) > 0 {
			properties["diagnostics"] = finding.Diagnostics
		}
		results = append(results, sarifResult{
			RuleID: finding.Code, Level: sarifLevel(finding), Message: sarifText{Text: message},
			Locations:  []sarifLocation{sarifLocationOf(finding.Path, finding.Line, finding.Column)},
			Properties: properties,
		})
	}
	notifications := make([]sarifNotification, 0, len(result.warnings))
	for _, warning := range result.warnings {
		notifications = append(notifications, sarifNotification{
			Level: "warning", Message: sarifText{Text: warning.Message}, Descriptor: sarifDescriptor{ID: warning.Code},
			Locations: []sarifLocation{sarifLocationOf(warning.Path, warning.Line, warning.Column)},
		})
	}
	return sarifLog{
		Version: "2.1.0", Schema: "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool:        sarifTool{Driver: sarifDriver{Name: "trasloco modernize", InformationURI: retirementURL, Rules: rules}},
			Invocations: []sarifInvocation{{ExecutionSuccessful: executionSuccessful, ToolExecutionNotifications: notifications}},
			Results:     results,
		}},
	}
}
