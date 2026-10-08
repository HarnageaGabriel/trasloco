// Package ir defines the provider-neutral pipeline intermediate representation.
package ir

import (
	"encoding/json"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// SchemaVersion is the only IR schema version understood by this build.
const SchemaVersion = 1

// Origin identifies the source spelling and location of a construct.
type Origin struct {
	Source           diag.Source   `json:"source"`
	Chain            []diag.Source `json:"chain,omitempty"`
	OriginalTextHash string        `json:"originalTextHash"`
}

// Extensions holds provider-owned JSON. Keys are x-<provider>. Key order is
// semantically irrelevant; validation and canonical output sort keys.
type Extensions map[string]json.RawMessage

// Metadata is embedded by semantic nodes for traceability and loss accounting.
type Metadata struct {
	Origin     *Origin         `json:"origin,omitempty"`
	Fidelity   diag.Fidelity   `json:"fidelity"`
	Provenance diag.Provenance `json:"provenance"`
	Extensions Extensions      `json:"extensions,omitempty"`
}

// Pipeline is a normalized pipeline with an explicit job DAG.
type Pipeline struct {
	Metadata
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Inputs        []Input           `json:"inputs,omitempty"`
	Triggers      []Trigger         `json:"triggers,omitempty"`
	Variables     []Variable        `json:"variables,omitempty"`
	Jobs          []Job             `json:"jobs"`
	Stages        []Stage           `json:"stages"`
	Concurrency   *Concurrency      `json:"concurrency,omitempty"`
	Diagnostics   []diag.Diagnostic `json:"diagnostics,omitempty"`
}

// InputType is the portable pipeline input type.
type InputType string

// Supported input types.
const (
	InputString InputType = "string"
	InputBool   InputType = "bool"
	InputNumber InputType = "number"
	InputChoice InputType = "choice"
)

// Input declares a pipeline input. Default uses the portable Literal model.
type Input struct {
	Metadata
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        InputType `json:"type"`
	Required    bool      `json:"required"`
	Default     *Literal  `json:"default,omitempty"`
	Choices     []string  `json:"choices,omitempty"`
	Description string    `json:"description,omitempty"`
}

// VariableScope identifies where a variable is visible.
type VariableScope string

// Supported variable scopes.
const (
	ScopeOrganization VariableScope = "organization"
	ScopeRepository   VariableScope = "repository"
	ScopePipeline     VariableScope = "pipeline"
	ScopeStage        VariableScope = "stage"
	ScopeJob          VariableScope = "job"
	ScopeStep         VariableScope = "step"
	ScopeEnvironment  VariableScope = "environment"
	ScopeGroup        VariableScope = "group"
)

// Variable is a non-secret value or a secret reference with explicit scope.
type Variable struct {
	Metadata
	Name      string        `json:"name"`
	Scope     VariableScope `json:"scope"`
	ScopeID   string        `json:"scopeId,omitempty"`
	Value     *Expr         `json:"value,omitempty"`
	Secret    bool          `json:"secret"`
	SecretRef *SecretRef    `json:"secretRef,omitempty"`
}

// SecretRef identifies secret material without containing its value.
type SecretRef struct {
	Name    string        `json:"name"`
	Scope   VariableScope `json:"scope"`
	ScopeID string        `json:"scopeId,omitempty"`
}

// TriggerKind is a normalized pipeline creation event.
type TriggerKind string

// Supported trigger kinds.
const (
	TriggerPush     TriggerKind = "push"
	TriggerPR       TriggerKind = "pr"
	TriggerSchedule TriggerKind = "schedule"
	TriggerManual   TriggerKind = "manual"
	TriggerUpstream TriggerKind = "upstream"
	TriggerWebhook  TriggerKind = "webhook"
	TriggerOther    TriggerKind = "other"
)

// Trigger describes one normalized creation event and its filters.
type Trigger struct {
	Metadata
	ID       string         `json:"id"`
	Kind     TriggerKind    `json:"kind"`
	Filters  TriggerFilters `json:"filters,omitempty"`
	Binding  TriggerBinding `json:"binding"`
	Schedule *Schedule      `json:"schedule,omitempty"`
	Upstream string         `json:"upstream,omitempty"`
	Other    *Raw           `json:"other,omitempty"`
}

// TriggerBinding records whether the trigger lives in pipeline source.
type TriggerBinding struct {
	InYAML bool `json:"inYaml"`
}

// TriggerFilters uses one normalized glob dialect while retaining spellings.
type TriggerFilters struct {
	Branches []Glob   `json:"branches,omitempty"`
	Tags     []Glob   `json:"tags,omitempty"`
	Paths    []Glob   `json:"paths,omitempty"`
	Actions  []string `json:"actions,omitempty"`
}

// Glob is a gitignore-style normalized pattern and its source spelling.
type Glob struct {
	Pattern  string `json:"pattern"`
	Original string `json:"original"`
}

// Schedule is a POSIX five-field cron in an IANA time zone.
type Schedule struct {
	Cron          string `json:"cron"`
	Timezone      string `json:"timezone"`
	Original      string `json:"original"`
	OnlyIfChanged bool   `json:"onlyIfChanged"`
}

// Stage partitions jobs. Barrier requires all jobs in earlier stages to finish.
type Stage struct {
	Metadata
	If        *Cond      `json:"if,omitempty"`
	DependsOn []string   `json:"dependsOn,omitempty"`
	Variables []Variable `json:"variables,omitempty"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Jobs      []string   `json:"jobs"`
	Barrier   bool       `json:"barrier"`
}

// Job is one DAG vertex.
type Job struct {
	Metadata
	Uses         *ReusableCall      `json:"uses,omitempty"`
	Permissions  OrderedMap[string] `json:"permissions,omitempty"`
	Concurrency  *Concurrency       `json:"concurrency,omitempty"`
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Needs        []string           `json:"needs,omitempty"`
	If           *Cond              `json:"if,omitempty"`
	Runner       RunnerReq          `json:"runner"`
	Container    *Container         `json:"container,omitempty"`
	Services     []Service          `json:"services,omitempty"`
	Variables    []Variable         `json:"variables,omitempty"`
	Secrets      []SecretRef        `json:"secrets,omitempty"`
	Outputs      []OutputDef        `json:"outputs,omitempty"`
	Steps        []Step             `json:"steps"`
	Matrix       *Matrix            `json:"matrix,omitempty"`
	Timeout      *Duration          `json:"timeout,omitempty"`
	AllowFailure bool               `json:"allowFailure"`
	Deployment   *DeploymentIntent  `json:"deployment,omitempty"`
	Result       ResultModel        `json:"result"`
}

// ReusableCall binds inputs and secret references to a reusable workflow.
type ReusableCall struct {
	Ref     string                `json:"ref"`
	With    OrderedMap[Expr]      `json:"with,omitempty"`
	Secrets OrderedMap[SecretRef] `json:"secrets,omitempty"`
}

// ResultModel is the portable terminal result taxonomy.
type ResultModel string

// Supported terminal results.
const (
	ResultSucceeded           ResultModel = "succeeded"
	ResultSucceededWithIssues ResultModel = "succeededWithIssues"
	ResultFailed              ResultModel = "failed"
	ResultCanceled            ResultModel = "canceled"
	ResultSkipped             ResultModel = "skipped"
)

// OutputDef declares a downstream-readable step output.
type OutputDef struct {
	Name string    `json:"name"`
	From OutputRef `json:"from"`
}

// OutputRef is a fully qualified output reference.
type OutputRef struct {
	Stage string `json:"stage"`
	Job   string `json:"job"`
	Step  string `json:"step"`
	Name  string `json:"name"`
}

// Matrix is a fully expanded set of execution legs.
type Matrix struct {
	Opaque      *Raw        `json:"opaque,omitempty"`
	Legs        []MatrixLeg `json:"legs"`
	MaxParallel int         `json:"maxParallel,omitempty"`
	FailFast    bool        `json:"failFast"`
}

// MatrixLeg is one explicit assignment set.
type MatrixLeg struct {
	ID     string              `json:"id"`
	Values OrderedMap[Literal] `json:"values"`
}

// ConcurrencyMode states the scheduling intent.
type ConcurrencyMode string

// Supported concurrency intents.
const (
	ConcurrencyCancelInProgress ConcurrencyMode = "cancel-in-progress"
	ConcurrencyQueueLatest      ConcurrencyMode = "queue-latest"
	ConcurrencySerialize        ConcurrencyMode = "serialize"
)

// Concurrency identifies mutually exclusive runs and desired queue behavior.
type Concurrency struct {
	Group Expr            `json:"group"`
	Mode  ConcurrencyMode `json:"mode"`
}

// DeploymentIntent captures deployment semantics outside ordinary execution.
type DeploymentIntent struct {
	Environment string `json:"environment"`
	Strategy    string `json:"strategy,omitempty"`
	Hook        string `json:"hook,omitempty"`
}

// RunnerReq is the provider-neutral runner constraint set.
type RunnerReq struct {
	OS       string   `json:"os,omitempty"`
	Arch     string   `json:"arch,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Image    string   `json:"image,omitempty"`
	ImagePin string   `json:"imagePin,omitempty"`
	Pool     string   `json:"pool,omitempty"`
	Demands  []string `json:"demands,omitempty"`
}

// Container describes the job container.
type Container struct {
	Image       string            `json:"image"`
	ImagePin    string            `json:"imagePin,omitempty"`
	Env         OrderedMap[Expr]  `json:"env,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Options     []string          `json:"options,omitempty"`
	Credentials *ContainerAuthRef `json:"credentials,omitempty"`
}

// ContainerAuthRef names credential bindings without embedding values.
type ContainerAuthRef struct {
	Username *Expr      `json:"username,omitempty"`
	Password *SecretRef `json:"password,omitempty"`
}

// Service is a named sidecar container.
type Service struct {
	ID        string         `json:"id"`
	Container Container      `json:"container"`
	Health    *ServiceHealth `json:"health,omitempty"`
}

// ServiceHealth captures portable health-check intent.
type ServiceHealth struct {
	Command  []string  `json:"command"`
	Interval *Duration `json:"interval,omitempty"`
	Timeout  *Duration `json:"timeout,omitempty"`
	Retries  int       `json:"retries,omitempty"`
}

// StepKind selects exactly one step payload.
type StepKind string

// Supported step and protocol-operation kinds.
const (
	StepRun             StepKind = "run"
	StepCheckout        StepKind = "checkout"
	StepArtifactPublish StepKind = "artifactPublish"
	StepArtifactRestore StepKind = "artifactRestore"
	StepCache           StepKind = "cache"
	StepAction          StepKind = "action"
	StepSetOutput       StepKind = "setOutput"
	StepSetEnv          StepKind = "setEnv"
	StepSetPipelineVar  StepKind = "setPipelineVar"
	StepAddPath         StepKind = "addPath"
	StepMask            StepKind = "mask"
	StepAnnotate        StepKind = "annotate"
	StepRaw             StepKind = "raw"
)

// Step is a discriminated union. Its Kind must match the sole non-nil payload.
type Step struct {
	Metadata
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Kind            StepKind             `json:"kind"`
	If              *Cond                `json:"if,omitempty"`
	Env             OrderedMap[Expr]     `json:"env,omitempty"`
	Timeout         *Duration            `json:"timeout,omitempty"`
	ContinueOnError bool                 `json:"continueOnError"`
	Retry           int                  `json:"retry"`
	Run             *RunStep             `json:"run,omitempty"`
	Checkout        *CheckoutStep        `json:"checkout,omitempty"`
	ArtifactPublish *ArtifactPublishStep `json:"artifactPublish,omitempty"`
	ArtifactRestore *ArtifactRestoreStep `json:"artifactRestore,omitempty"`
	Cache           *CacheStep           `json:"cache,omitempty"`
	Action          *ActionStep          `json:"action,omitempty"`
	Operation       *ProtocolOperation   `json:"operation,omitempty"`
	Raw             *Raw                 `json:"raw,omitempty"`
}

// RunStep executes a script under an explicit shell.
type RunStep struct {
	Script           string              `json:"script"`
	Shell            string              `json:"shell"`
	WorkingDirectory string              `json:"workingDirectory,omitempty"`
	Operations       []ProtocolOperation `json:"operations,omitempty"`
}

// CheckoutStep makes source acquisition explicit.
type CheckoutStep struct {
	Repository         string `json:"repository"`
	Ref                Expr   `json:"ref"`
	Path               string `json:"path,omitempty"`
	FetchDepth         int    `json:"fetchDepth,omitempty"`
	Submodules         bool   `json:"submodules"`
	LFS                bool   `json:"lfs"`
	PersistCredentials bool   `json:"persistCredentials"`
	PRMergeRef         string `json:"prMergeRef,omitempty"`
}

// ArtifactPublishStep publishes files under a portable artifact name.
type ArtifactPublishStep struct {
	Name string `json:"name"`
	Path Expr   `json:"path"`
}

// ArtifactRestoreStep explicitly downloads an artifact.
type ArtifactRestoreStep struct {
	Name string `json:"name"`
	Path Expr   `json:"path"`
}

// CacheStep restores and optionally saves a cache.
type CacheStep struct {
	Key         Expr   `json:"key"`
	RestoreKeys []Expr `json:"restoreKeys,omitempty"`
	Paths       []Expr `json:"paths"`
	Save        bool   `json:"save"`
}

// ActionStep invokes a provider action/task retained as a portable reference.
type ActionStep struct {
	Provider string           `json:"provider"`
	Name     string           `json:"name"`
	Version  string           `json:"version"`
	Digest   string           `json:"digest,omitempty"`
	With     OrderedMap[Expr] `json:"with,omitempty"`
}

// ProtocolOperation is one statically recognized runner command.
type ProtocolOperation struct {
	Kind     StepKind  `json:"kind"`
	Name     string    `json:"name,omitempty"`
	Value    *Expr     `json:"value,omitempty"`
	Secret   bool      `json:"secret,omitempty"`
	Level    string    `json:"level,omitempty"`
	Message  *Expr     `json:"message,omitempty"`
	Location *Location `json:"location,omitempty"`
}

// Location is an optional annotation location.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
}

// Duration serializes as a Go duration string, never a provider-specific unit.
type Duration struct {
	time.Duration
}

// MarshalJSON implements JSON string encoding for Duration.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON implements JSON string decoding for Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

// Phase is the expression evaluation phase.
type Phase string

// Supported evaluation phases.
const (
	PhaseCompile  Phase = "compile"
	PhaseCreation Phase = "creation"
	PhaseRuntime  Phase = "runtime"
)

// Cond contains either a portable expression or an opaque source fragment.
type Cond struct {
	Phase  Phase `json:"phase"`
	Expr   *Expr `json:"expr,omitempty"`
	Opaque *Raw  `json:"opaque,omitempty"`
}

// ExprKind selects one expression payload.
type ExprKind string

// Supported expression node kinds.
const (
	ExprLiteral ExprKind = "literal"
	ExprRef     ExprKind = "ref"
	ExprCall    ExprKind = "call"
)

// Expr is the closed portable expression AST.
type Expr struct {
	Kind    ExprKind  `json:"kind"`
	Literal *Literal  `json:"literal,omitempty"`
	Ref     *RefExpr  `json:"ref,omitempty"`
	Call    *CallExpr `json:"call,omitempty"`
}

// LiteralType identifies a JSON scalar literal.
type LiteralType string

// Supported literal scalar types.
const (
	LiteralNull   LiteralType = "null"
	LiteralString LiteralType = "string"
	LiteralBool   LiteralType = "bool"
	LiteralNumber LiteralType = "number"
)

// Literal stores exactly one scalar selected by Type.
type Literal struct {
	Type   LiteralType `json:"type"`
	String string      `json:"string,omitempty"`
	Bool   bool        `json:"bool,omitempty"`
	Number string      `json:"number,omitempty"`
}

// RefContext is a closed portable context namespace.
type RefContext string

// Supported portable reference contexts.
const (
	RefVariables         RefContext = "variables"
	RefParameters        RefContext = "parameters"
	RefEnv               RefContext = "env"
	RefSecrets           RefContext = "secrets"
	RefDependencyOutputs RefContext = "dependencyOutputs"
	RefMatrix            RefContext = "matrix"
	RefRunner            RefContext = "runner"
	RefRun               RefContext = "run"
)

// RefExpr references a context path or a typed dependency output.
type RefExpr struct {
	Context RefContext `json:"context"`
	Path    []string   `json:"path,omitempty"`
	Output  *OutputRef `json:"output,omitempty"`
}

// Function is a member of the closed portable function set.
type Function string

// Supported portable functions.
const (
	FunctionAnd                 Function = "and"
	FunctionOr                  Function = "or"
	FunctionNot                 Function = "not"
	FunctionEqual               Function = "eq"
	FunctionNotEqual            Function = "ne"
	FunctionLess                Function = "lt"
	FunctionLessOrEqual         Function = "le"
	FunctionGreater             Function = "gt"
	FunctionGreaterOrEqual      Function = "ge"
	FunctionContains            Function = "contains"
	FunctionStartsWith          Function = "startsWith"
	FunctionEndsWith            Function = "endsWith"
	FunctionFormat              Function = "format"
	FunctionJoin                Function = "join"
	FunctionSuccess             Function = "success"
	FunctionFailure             Function = "failure"
	FunctionAlways              Function = "always"
	FunctionCancelled           Function = "cancelled"
	FunctionSucceededWithIssues Function = "succeededWithIssues"
)

// CallExpr invokes a closed portable function.
type CallExpr struct {
	Function Function `json:"function"`
	Args     []Expr   `json:"args,omitempty"`
}

// Raw is the explicit escape hatch for an unmodeled source fragment.
type Raw struct {
	Provider string          `json:"provider"`
	Kind     string          `json:"kind"`
	Format   string          `json:"format"`
	Text     string          `json:"text,omitempty"`
	JSON     json.RawMessage `json:"json,omitempty"`
	Origin   *Origin         `json:"origin,omitempty"`
}
