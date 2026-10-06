// Package diag defines stable diagnostics and their embedded registry.
package diag

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// Severity is the diagnostic impact level.
type Severity string

const (
	// SeverityError prevents the affected resource from proceeding.
	SeverityError Severity = "error"
	// SeverityWarning records a difference or required attention.
	SeverityWarning Severity = "warning"
	// SeverityInfo records a deterministic informational decision.
	SeverityInfo Severity = "info"
)

// Fidelity describes conversion fidelity.
type Fidelity string

const (
	// FidelityExact preserves source behavior.
	FidelityExact Fidelity = "exact"
	// FidelityApproximated records a known semantic difference.
	FidelityApproximated Fidelity = "approximated"
	// FidelityNeedsManual requires user work.
	FidelityNeedsManual Fidelity = "needs-manual"
	// FidelityUnsupported has no target equivalent.
	FidelityUnsupported Fidelity = "unsupported"
)

// Provenance identifies how an output was produced, independently of fidelity.
type Provenance string

const (
	// ProvenanceDeterministic identifies deterministic engine output.
	ProvenanceDeterministic Provenance = "deterministic"
	// ProvenanceLLM identifies output proposed by a language model.
	ProvenanceLLM Provenance = "llm"
)

// ExitClass is the terminal class contributed by a diagnostic.
type ExitClass string

const (
	// ExitNone does not affect the process exit code.
	ExitNone ExitClass = "none"
	// ExitError contributes an error outcome.
	ExitError ExitClass = "error"
	// ExitBlocked contributes a manual-block outcome.
	ExitBlocked ExitClass = "blocked"
	// ExitDrift contributes a stale-plan outcome.
	ExitDrift ExitClass = "drift"
	// ExitUsage contributes an invalid-input outcome.
	ExitUsage ExitClass = "usage"
)

// Source identifies a source location. Zero coordinates are omitted.
type Source struct {
	Platform string `json:"platform,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	EndLine  int    `json:"endLine,omitempty"`
	EndCol   int    `json:"endCol,omitempty"`
}

// Resource identifies the affected migration resource.
type Resource struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Diagnostic is the exact JSON diagnostic instance shape.
type Diagnostic struct {
	Code        string         `json:"code"`
	Severity    Severity       `json:"severity"`
	Fidelity    *Fidelity      `json:"fidelity"`
	Provenance  Provenance     `json:"provenance"`
	Message     string         `json:"message"`
	Source      *Source        `json:"source"`
	Resource    *Resource      `json:"resource"`
	Remediation string         `json:"remediation"`
	DocsURL     string         `json:"docsUrl"`
	Params      map[string]any `json:"params,omitempty"`
}

// Entry is one stable registry definition.
type Entry struct {
	Code        string         `json:"code" yaml:"code"`
	Area        string         `json:"area" yaml:"area"`
	Title       string         `json:"title" yaml:"title"`
	Severity    Severity       `json:"severity" yaml:"severity"`
	Fidelity    *Fidelity      `json:"fidelity,omitempty" yaml:"fidelity,omitempty"`
	Provenance  *Provenance    `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	ExitClass   ExitClass      `json:"exitClass" yaml:"exitClass"`
	Remediation string         `json:"remediation" yaml:"remediation"`
	Since       string         `json:"since" yaml:"since"`
	Status      string         `json:"status" yaml:"status"`
	ReplacedBy  *string        `json:"replacedBy,omitempty" yaml:"replacedBy,omitempty"`
	Params      []string       `json:"params,omitempty" yaml:"params,omitempty"`
	History     []HistoryEntry `json:"history,omitempty" yaml:"history,omitempty"`
}

// HistoryEntry records a compatible registry metadata change.
type HistoryEntry struct {
	Version string `json:"version" yaml:"version"`
	Change  string `json:"change" yaml:"change"`
}

//go:embed registry.yaml
var registryYAML []byte

var (
	registryEntries []Entry
	registryByCode  map[string]Entry
	registryErr     error
)

func init() {
	registryEntries, registryByCode, registryErr = parseRegistry(registryYAML)
}

// RegistryError reports an embedded registry validation failure.
func RegistryError() error { return registryErr }

// Lookup returns a copy of the registry entry for code.
func Lookup(code string) (Entry, bool) {
	entry, ok := registryByCode[code]
	return cloneEntry(entry), ok
}

// List returns registry entries sorted by code.
func List() []Entry {
	entries := make([]Entry, len(registryEntries))
	for i, entry := range registryEntries {
		entries[i] = cloneEntry(entry)
	}
	return entries
}

// ValidateRegistry validates registry YAML using the normative structural rules.
func ValidateRegistry(data []byte) error {
	_, _, err := parseRegistry(data)
	return err
}

// ValidCode reports whether code has the normative shape and a known area.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// Option refines a diagnostic instance.
type Option func(*construction) error

type construction struct {
	Diagnostic
	redactor *redact.Redactor
	args     map[string]any
}

// Error carries a registered finding through an ordinary error chain.
type Error struct{ Finding Diagnostic }

// Error implements error; use errors.As to retrieve the finding.
func (e *Error) Error() string { return e.Finding.Code + ": " + e.Finding.Message }

// WithRedactor uses the invocation's registered secrets when constructing output.
func WithRedactor(r *redact.Redactor) Option {
	return func(d *construction) error {
		if r != nil {
			d.redactor = r
		}
		return nil
	}
}

// WithArgs supplies named registry template arguments and diagnostic parameters.
func WithArgs(args map[string]any) Option {
	return func(d *construction) error { d.args = cloneMap(args); d.Params = cloneMap(args); return nil }
}

// WithFidelity records the conversion fidelity independently of severity.
func WithFidelity(fidelity Fidelity) Option {
	return func(d *construction) error {
		switch fidelity {
		case FidelityExact, FidelityApproximated, FidelityNeedsManual, FidelityUnsupported:
			d.Fidelity = &fidelity
			return nil
		default:
			return fmt.Errorf("invalid diagnostic fidelity %q", fidelity)
		}
	}
}

// MustNew constructs a finding for a statically registered code and valid options.
// Invalid programming-time options panic; dynamic callers should use New.
func MustNew(code string, opts ...Option) Diagnostic {
	d, err := New(code, opts...)
	if err != nil {
		panic(err)
	}
	return d
}

// WithSeverity verifies severity against the fixed registry value.
func WithSeverity(severity Severity) Option {
	return func(d *construction) error {
		entry, ok := Lookup(d.Code)
		if !ok {
			return fmt.Errorf("unknown diagnostic code %q", d.Code)
		}
		if severity == entry.Severity {
			d.Severity = severity
			return nil
		}
		return fmt.Errorf("diagnostic %s severity cannot change from %s to %s", d.Code, entry.Severity, severity)
	}
}

// WithProvenance records how the finding's subject was produced.
func WithProvenance(provenance Provenance) Option {
	return func(d *construction) error {
		if provenance != ProvenanceDeterministic && provenance != ProvenanceLLM {
			return fmt.Errorf("invalid diagnostic provenance %q", provenance)
		}
		d.Provenance = provenance
		return nil
	}
}

// WithSource sets the diagnostic source location.
func WithSource(source Source) Option {
	return func(d *construction) error { d.Source = &source; return nil }
}

// WithResource sets the affected resource.
func WithResource(resource Resource) Option {
	return func(d *construction) error { d.Resource = &resource; return nil }
}

// WithMessage replaces the registry title for a more specific instance message.
func WithMessage(message string) Option {
	return func(d *construction) error { d.Message = message; return nil }
}

// WithParams sets non-secret machine-readable parameters.
func WithParams(params map[string]any) Option {
	return func(d *construction) error { d.Params = cloneMap(params); return nil }
}

// WithRemediation refines the registry remediation for this instance.
func WithRemediation(remediation string) Option {
	return func(d *construction) error { d.Remediation = remediation; return nil }
}

// New constructs a registered finding using typed options.
func New(code string, opts ...Option) (Diagnostic, error) {
	entry, ok := Lookup(code)
	if !ok {
		return Diagnostic{}, fmt.Errorf("unknown diagnostic code %q", code)
	}
	d := construction{Diagnostic: Diagnostic{
		Code: code, Severity: entry.Severity, Fidelity: entry.Fidelity,
		Provenance: ProvenanceDeterministic, Message: entry.Title,
		Remediation: entry.Remediation, DocsURL: docsURL(code),
	}, redactor: redact.New()}
	if entry.Provenance != nil {
		d.Provenance = *entry.Provenance
	}
	for _, opt := range opts {
		if opt == nil {
			return Diagnostic{}, fmt.Errorf("nil diagnostic option")
		}
		if err := opt(&d); err != nil {
			return Diagnostic{}, err
		}
	}
	if d.args != nil && strings.Contains(d.Message, "{{") {
		parsed, err := template.New(code).Option("missingkey=error").Parse(d.Message)
		if err != nil {
			return Diagnostic{}, fmt.Errorf("parse diagnostic template %s: %w", code, err)
		}
		var rendered bytes.Buffer
		if err := parsed.Execute(&rendered, d.args); err != nil {
			return Diagnostic{}, fmt.Errorf("render diagnostic template %s: %w", code, err)
		}
		d.Message = rendered.String()
	}
	d.Message = oneLine(d.redactor.Redact(d.Message))
	d.Remediation = oneLine(d.redactor.Redact(d.Remediation))
	params, err := redactParams(d.redactor, d.Params)
	if err != nil {
		return Diagnostic{}, err
	}
	d.Params = params
	if d.Source != nil {
		d.Source.File = d.redactor.Redact(d.Source.File)
		d.Source.Platform = d.redactor.Redact(d.Source.Platform)
	}
	if d.Resource != nil {
		d.Resource.Kind = d.redactor.Redact(d.Resource.Kind)
		d.Resource.Key = d.redactor.Redact(d.Resource.Key)
		d.Resource.Name = d.redactor.Redact(d.Resource.Name)
	}
	return d.Diagnostic, nil
}

// ExitClassFor returns the registry exit class for a diagnostic.
func ExitClassFor(d Diagnostic) ExitClass {
	entry, ok := Lookup(d.Code)
	if !ok {
		return ExitError
	}
	return entry.ExitClass
}

// HighestExitClass reduces diagnostics to their most actionable class.
func HighestExitClass(diagnostics []Diagnostic) ExitClass {
	priority := map[ExitClass]int{ExitNone: 0, ExitBlocked: 1, ExitError: 2, ExitDrift: 3, ExitUsage: 4}
	result := ExitNone
	for _, diagnostic := range diagnostics {
		class := ExitClassFor(diagnostic)
		if priority[class] > priority[result] {
			result = class
		}
	}
	return result
}

func redactParams(r *redact.Redactor, params map[string]any) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal diagnostic params: %w", err)
	}
	encoded = r.RedactBytes(encoded)
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("redact diagnostic params: %w", err)
	}
	return result, nil
}

func docsURL(code string) string {
	return "https://github.com/HarnageaGabriel/trasloco/blob/main/docs/diagnostics/" + code + ".md"
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "\n", " ")), " ")
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneEntry(entry Entry) Entry {
	if entry.Fidelity != nil {
		fidelity := *entry.Fidelity
		entry.Fidelity = &fidelity
	}
	if entry.Provenance != nil {
		provenance := *entry.Provenance
		entry.Provenance = &provenance
	}
	if entry.ReplacedBy != nil {
		replacedBy := *entry.ReplacedBy
		entry.ReplacedBy = &replacedBy
	}
	entry.Params = append([]string(nil), entry.Params...)
	entry.History = append([]HistoryEntry(nil), entry.History...)
	return entry
}

func severityDistance(from, to Severity) int {
	levels := map[Severity]int{SeverityInfo: 0, SeverityWarning: 1, SeverityError: 2}
	fromLevel, fromOK := levels[from]
	toLevel, toOK := levels[to]
	if !fromOK || !toOK {
		return -1
	}
	if fromLevel > toLevel {
		return fromLevel - toLevel
	}
	return toLevel - fromLevel
}

func mustRegistry() error {
	if registryErr != nil {
		return errors.New("embedded diagnostic registry invalid: " + registryErr.Error())
	}
	return nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Code < entries[j].Code })
}

func init() {
	if err := mustRegistry(); err != nil {
		panic(err)
	}
}
