// Package catalog loads and queries task and action catalog data.
package catalog

// TasksFile is the generated Azure Pipelines task-facts document.
type TasksFile struct {
	SchemaVersion int        `yaml:"schemaVersion"`
	Kind          string     `yaml:"kind"`
	Source        TaskSource `yaml:"source"`
	Tasks         []Task     `yaml:"tasks"`
}

// TaskSource identifies the pinned task metadata source.
type TaskSource struct {
	Repo    string `yaml:"repo"`
	Commit  string `yaml:"commit"`
	License string `yaml:"license"`
}

// Task groups all cataloged majors of an Azure Pipelines task.
type Task struct {
	Name     string        `yaml:"name"`
	ID       string        `yaml:"id"`
	Versions []TaskVersion `yaml:"versions"`
}

// TaskVersion contains factual metadata for one task major.
type TaskVersion struct {
	Major                 int      `yaml:"major"`
	Minor                 int      `yaml:"minor"`
	Patch                 int      `yaml:"patch"`
	Dir                   string   `yaml:"dir"`
	Deprecated            bool     `yaml:"deprecated,omitempty"`
	HasDeprecationMessage bool     `yaml:"hasDeprecationMessage,omitempty"`
	RemovalDate           string   `yaml:"removalDate,omitempty"`
	Preview               bool     `yaml:"preview,omitempty"`
	Handlers              []string `yaml:"handlers,omitempty"`
	PreJobHandlers        []string `yaml:"preJobHandlers,omitempty"`
	PostJobHandlers       []string `yaml:"postJobHandlers,omitempty"`
	Groups                []string `yaml:"groups,omitempty"`
	Inputs                []Input  `yaml:"inputs,omitempty"`
}

// Input describes one task input without descriptive prose.
type Input struct {
	Name           string   `yaml:"name"`
	Aliases        []string `yaml:"aliases,omitempty"`
	Type           string   `yaml:"type"`
	Default        string   `yaml:"default,omitempty"`
	DefaultOmitted bool     `yaml:"defaultOmitted,omitempty"`
	Required       bool     `yaml:"required,omitempty"`
	Options        []string `yaml:"options,omitempty"`
	VisibleRule    string   `yaml:"visibleRule,omitempty"`
	Group          string   `yaml:"group,omitempty"`
}

// RetirementFile is the generated official task-retirement document.
type RetirementFile struct {
	SchemaVersion  int              `yaml:"schemaVersion"`
	Kind           string           `yaml:"kind"`
	Source         RetirementSource `yaml:"source"`
	RetirementDate string           `yaml:"retirementDate"`
	RemovalDate    string           `yaml:"removalDate"`
	Tasks          []RetiredTask    `yaml:"tasks"`
}

// RetirementSource identifies the official retirement page.
type RetirementSource struct {
	URL string `yaml:"url"`
}

// RetiredTask records retirement and replacement facts for one task major.
type RetiredTask struct {
	Task            string   `yaml:"task"`
	Name            string   `yaml:"name"`
	Major           int      `yaml:"major"`
	DeprecatedOn    string   `yaml:"deprecatedOn,omitempty"`
	LegacyBuiltin   bool     `yaml:"legacyBuiltin,omitempty"`
	ReplacementKind string   `yaml:"replacementKind"`
	Replacements    []string `yaml:"replacements,omitempty"`
}

// ModernizationFile is the reviewed modernization guidance for retired tasks.
type ModernizationFile struct {
	SchemaVersion  int                 `yaml:"schemaVersion"`
	Kind           string              `yaml:"kind"`
	Source         ModernizationSource `yaml:"source"`
	RetirementDate string              `yaml:"retirementDate"`
	RemovalDate    string              `yaml:"removalDate"`
	Tasks          []Modernization     `yaml:"tasks"`
}

// ModernizationSource identifies the specification and official retirement page.
type ModernizationSource struct {
	Spec          string `yaml:"spec"`
	RetirementURL string `yaml:"retirementUrl"`
}

// Modernization records reviewed guidance for one retired task major.
type Modernization struct {
	Task        string   `yaml:"task"`
	GUID        string   `yaml:"guid"`
	Replacement []string `yaml:"replacement"`
	Action      string   `yaml:"action"`
	Class       string   `yaml:"class"`
	Program     string   `yaml:"program"`
	Sources     []string `yaml:"sources"`
	Verify      []string `yaml:"verify,omitempty"`
}

// Mapping describes a hand-written conversion for one task major.
type Mapping struct {
	SchemaVersion int           `yaml:"schemaVersion"`
	Kind          string        `yaml:"kind"`
	Task          string        `yaml:"task"`
	Source        string        `yaml:"source"`
	Fidelity      string        `yaml:"fidelity"`
	Conditions    []Condition   `yaml:"conditions,omitempty"`
	Target        Target        `yaml:"target"`
	Inputs        []InputMap    `yaml:"inputs,omitempty"`
	Tests         []MappingTest `yaml:"tests"`
}

// Condition limits when a mapping applies.
type Condition struct {
	When string `yaml:"when"`
	Note string `yaml:"note,omitempty"`
}

// Target is the GitHub Actions construct produced by a mapping.
type Target struct {
	Kind  string `yaml:"kind"`
	Run   string `yaml:"run,omitempty"`
	Uses  string `yaml:"uses,omitempty"`
	Shell string `yaml:"shell,omitempty"`
}

// InputMap maps one Azure Pipelines input to the target construct.
type InputMap struct {
	From      string `yaml:"from"`
	To        string `yaml:"to,omitempty"`
	Transform string `yaml:"transform,omitempty"`
	Default   string `yaml:"default,omitempty"`
	Drop      bool   `yaml:"drop,omitempty"`
}

// MappingTest is an inline example that validates a hand-written mapping.
type MappingTest struct {
	Name   string         `yaml:"name"`
	Inputs map[string]any `yaml:"inputs"`
	Expect map[string]any `yaml:"expect"`
}

// ActionsLock contains reproducibly resolved GitHub Actions.
type ActionsLock struct {
	SchemaVersion int              `yaml:"schemaVersion"`
	Kind          string           `yaml:"kind"`
	Actions       []LockedAction   `yaml:"actions"`
	Rejected      []RejectedAction `yaml:"rejected,omitempty"`
}

// LockedAction pins an allowed action tag to a commit and runtime.
type LockedAction struct {
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
	SHA       string `yaml:"sha"`
	RunsUsing string `yaml:"runsUsing"`
}

// RejectedAction records an action excluded by the runtime policy.
type RejectedAction struct {
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
	RunsUsing string `yaml:"runsUsing"`
}

// RuntimeAllowed reports whether a runs.using value meets catalog policy.
func RuntimeAllowed(runtime string) bool {
	switch runtime {
	case "node24", "composite", "docker":
		return true
	default:
		return false
	}
}
