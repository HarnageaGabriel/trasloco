package ast

// Shape is the top-level form of a pipeline file.
type Shape uint8

const (
	// ShapeNone is a file with none of stages, jobs, steps or extends (for example
	// a variables template).
	ShapeNone Shape = iota
	// ShapeStages has a stages list.
	ShapeStages
	// ShapeJobs has a jobs list and one implicit stage.
	ShapeJobs
	// ShapeSteps has a steps list, one implicit stage and one implicit job.
	ShapeSteps
	// ShapeExtends extends a template.
	ShapeExtends
)

// String returns the lower-case shape name.
func (s Shape) String() string {
	switch s {
	case ShapeStages:
		return "stages"
	case ShapeJobs:
		return "jobs"
	case ShapeSteps:
		return "steps"
	case ShapeExtends:
		return "extends"
	default:
		return "none"
	}
}

// Pipeline is the root of an Azure Pipelines definition or template file.
type Pipeline struct {
	Pos
	Shape                        Shape
	Name                         *Scalar
	AppendCommitMessageToRunName *Bool
	Trigger                      *Trigger
	PR                           *Trigger
	Schedules                    *List[*Schedule]
	LockBehavior                 *Enum
	Resources                    *Resources
	Parameters                   *Parameters
	Variables                    *Variables
	Pool                         *Pool
	Extends                      *Extends
	Stages                       *List[Stage]
	Jobs                         *List[Job]
	Steps                        *List[Step]
	// Strategy, ContinueOnError, Container, Services and Workspace are the job
	// properties allowed at the root of a steps-shaped pipeline.
	Strategy        *Strategy
	ContinueOnError *Bool
	Container       *Container
	Services        *Map[*Scalar]
	ServiceSpecs    *Map[*Any]
	Workspace       *Workspace
	Tail[Pipeline]
}

// TriggerForm identifies which syntax a trigger was written in.
type TriggerForm uint8

const (
	// TriggerFull is the object form.
	TriggerFull TriggerForm = iota
	// TriggerNone is `none`.
	TriggerNone
	// TriggerList is a list of branch names.
	TriggerList
	// TriggerInsertion is a whole-value template expression.
	TriggerInsertion
)

// Trigger is a push (`trigger`) or pull request (`pr`) trigger, also used by
// repository resources.
type Trigger struct {
	Pos
	Insertable
	Form TriggerForm
	// Keyword is the `none` scalar of TriggerNone.
	Keyword *Scalar
	// Batch is the push trigger property.
	Batch *Bool
	// AutoCancel and Drafts are the pull request trigger properties.
	AutoCancel *Bool
	Drafts     *Bool
	Branches   *Filter
	Paths      *Filter
	Tags       *Filter
	Tail[Trigger]
}

// Filter is an include/exclude list; a bare list is a list of includes.
type Filter struct {
	Pos
	Insertable
	// Bare is true when written as a plain list.
	Bare    bool
	Keyword *Scalar
	Include *List[*Scalar]
	Exclude *List[*Scalar]
	Tail[Filter]
}

// Schedule is one cron entry of `schedules`.
type Schedule struct {
	Pos
	Cron        *Scalar
	DisplayName *Scalar
	Branches    *Filter
	Batch       *Bool
	Always      *Bool
	Tail[Schedule]
}

// Resources groups the resource lists of a pipeline.
type Resources struct {
	Pos
	Insertable
	// Legacy is the historical `resources: [ { repo: ... } ]` form.
	Legacy       *List[*Any]
	Builds       *List[*BuildResource]
	Containers   *List[*ContainerResource]
	Pipelines    *List[*PipelineResource]
	Repositories *List[*RepositoryResource]
	Webhooks     *List[*WebhookResource]
	Packages     *List[*PackageResource]
	Tail[Resources]
}

// ResourceTriggerForm identifies the syntax of a resource trigger.
type ResourceTriggerForm uint8

const (
	// ResourceTriggerFull is the object form.
	ResourceTriggerFull ResourceTriggerForm = iota
	// ResourceTriggerNone is `none`.
	ResourceTriggerNone
	// ResourceTriggerTrue is `true`.
	ResourceTriggerTrue
	// ResourceTriggerInsertion is a whole-value template expression.
	ResourceTriggerInsertion
)

// ResourceTrigger is the trigger of a pipeline, container, build or package resource.
type ResourceTrigger struct {
	Pos
	Insertable
	Form ResourceTriggerForm
	// Keyword is the `none` or `true` scalar of the keyword forms.
	Keyword  *Scalar
	Enabled  *Bool
	Branches *Filter
	Stages   *List[*Scalar]
	Tags     *Filter
	Tail[ResourceTrigger]
}

// RepositoryResource is `resources.repositories[]`.
type RepositoryResource struct {
	Pos
	Repository *Scalar
	Endpoint   *Scalar
	Trigger    *Trigger
	Name       *Scalar
	Ref        *Scalar
	Type       *Enum
	Tail[RepositoryResource]
}

// PipelineResource is `resources.pipelines[]`.
type PipelineResource struct {
	Pos
	Pipeline *Scalar
	Project  *Scalar
	Source   *Scalar
	Version  *Scalar
	Branch   *Scalar
	Tags     *List[*Scalar]
	Trigger  *ResourceTrigger
	Tail[PipelineResource]
}

// ContainerSpec holds the properties shared by a container resource and the
// object form of a job container.
type ContainerSpec struct {
	Image           *Scalar
	Endpoint        *Scalar
	Env             *Map[*Scalar]
	MapDockerSocket *Bool
	Options         *Scalar
	Ports           *List[*Scalar]
	Volumes         *List[*Scalar]
	MountReadOnly   *MountReadOnly
}

// MountReadOnly lists the agent directories mounted read-only.
type MountReadOnly struct {
	Pos
	Insertable
	Work      *Bool
	Externals *Bool
	Tools     *Bool
	Tasks     *Bool
	Tail[MountReadOnly]
}

// ContainerResource is `resources.containers[]`.
type ContainerResource struct {
	Pos
	Container *Scalar
	ContainerSpec
	Type              *Scalar
	Trigger           *ResourceTrigger
	AzureSubscription *Scalar
	ResourceGroup     *Scalar
	Registry          *Scalar
	Repository        *Scalar
	LocalImage        *Bool
	Tail[ContainerResource]
}

// BuildResource is `resources.builds[]`.
type BuildResource struct {
	Pos
	Build      *Scalar
	Type       *Scalar
	Connection *Scalar
	Source     *Scalar
	Version    *Scalar
	Branch     *Scalar
	Trigger    *ResourceTrigger
	Tail[BuildResource]
}

// PackageResource is `resources.packages[]`.
type PackageResource struct {
	Pos
	Package    *Scalar
	Type       *Scalar
	Connection *Scalar
	Name       *Scalar
	Version    *Scalar
	Tag        *Scalar
	Trigger    *ResourceTrigger
	Tail[PackageResource]
}

// WebhookResource is `resources.webhooks[]`.
type WebhookResource struct {
	Pos
	Webhook    *Scalar
	Connection *Scalar
	Type       *Scalar
	Filters    *List[*WebhookFilter]
	Tail[WebhookResource]
}

// WebhookFilter is one trigger filter of a webhook resource.
type WebhookFilter struct {
	Pos
	Path  *Scalar
	Value *Scalar
	Tail[WebhookFilter]
}

// ParametersForm identifies the syntax of a parameters block.
type ParametersForm uint8

const (
	// ParametersList is the list-of-definitions form.
	ParametersList ParametersForm = iota
	// ParametersMap is the legacy `name: default` mapping form.
	ParametersMap
	// ParametersInsertion is a whole-value template expression.
	ParametersInsertion
)

// Parameters is a `parameters` block of a pipeline or template file.
type Parameters struct {
	Pos
	Insertable
	Form ParametersForm
	List *List[*Parameter]
	// Map holds the legacy mapping form; values are the defaults.
	Map *Map[*Any]
}

// Parameter is one typed parameter definition.
type Parameter struct {
	Pos
	Name        *Scalar
	DisplayName *Scalar
	Type        *Enum
	Default     *Any
	Values      *List[*Scalar]
	Tail[Parameter]
}

// VariablesForm identifies the syntax of a variables block.
type VariablesForm uint8

const (
	// VariablesMap is the `name: value` mapping form.
	VariablesMap VariablesForm = iota
	// VariablesList is the list of name/group/template items.
	VariablesList
	// VariablesInsertion is a whole-value template expression.
	VariablesInsertion
)

// Variables is a `variables` block.
type Variables struct {
	Pos
	Insertable
	Form VariablesForm
	Map  *Map[*Scalar]
	List *List[Variable]
}

// Variable is one item of the list form: VariableDef, VariableGroup or VariableTemplate.
type Variable interface {
	Node
	variableNode()
}

// VariableDef is `- name: x` with value and readonly.
type VariableDef struct {
	Pos
	Name     *Scalar
	Value    *Scalar
	Readonly *Bool
	Tail[VariableDef]
}

// VariableGroup is `- group: name`.
type VariableGroup struct {
	Pos
	Group *Scalar
	Tail[VariableGroup]
}

// VariableTemplate is `- template: path` with parameters.
type VariableTemplate struct {
	Pos
	TemplateRef
	Tail[VariableTemplate]
}

func (*VariableDef) variableNode()      {}
func (*VariableGroup) variableNode()    {}
func (*VariableTemplate) variableNode() {}

// TemplateRef is the `template` and `parameters` pair shared by template references.
type TemplateRef struct {
	// Template is the reference as written, e.g. `steps/build.yml@templates`.
	Template *Scalar
	// File is the template path without the `@alias` suffix.
	File string
	// Alias is the repository resource alias after `@` (`self` for the current
	// repository); empty when the template lives in the same repository.
	Alias string
	// Dynamic is true when the reference contains a template expression.
	Dynamic    bool
	Parameters *Map[*Any]
}

// Extends is the `extends` block.
type Extends struct {
	Pos
	Insertable
	TemplateRef
	Tail[Extends]
}

// Pool is a pool specification; a bare scalar is the pool name.
type Pool struct {
	Pos
	Insertable
	// Short is true when written as `pool: name`.
	Short   bool
	Name    *Scalar
	VMImage *Scalar
	Demands *List[*Scalar]
	// DemandsScalar is true when demands was a single string.
	DemandsScalar bool
	Tail[Pool]
}

// Workspace is the job workspace options.
type Workspace struct {
	Pos
	Insertable
	Clean *Enum
	Tail[Workspace]
}

// Uses lists resources a job requires that are not referenced elsewhere.
type Uses struct {
	Pos
	Insertable
	Repositories *List[*Scalar]
	Pools        *List[*Scalar]
	Tail[Uses]
}

// Container is a job container: an alias string or the object form.
type Container struct {
	Pos
	Insertable
	// Alias is set for the string form.
	Alias *Scalar
	// Spec is set for the object form.
	Spec *ContainerSpec
	Tail[Container]
}

// DependsOn is a stage or job dependency list; a scalar is a single name.
type DependsOn struct {
	Pos
	Insertable
	// Scalar is true when written as a single string.
	Scalar bool
	// Names is empty for an explicit `dependsOn: []`.
	Names *List[*Scalar]
}

// Strategy is the job strategy: matrix and maxParallel, or parallel.
type Strategy struct {
	Pos
	Insertable
	Matrix      *Map[*Map[*Scalar]]
	MaxParallel *Scalar
	Parallel    *Scalar
	Tail[Strategy]
}

// Stage is one item of a stages list: StageDef or StageTemplate.
type Stage interface {
	Node
	stageNode()
}

// StageDef is `- stage: id`.
type StageDef struct {
	Pos
	Stage           *Scalar
	Group           *Scalar
	DisplayName     *Scalar
	Pool            *Pool
	DependsOn       *DependsOn
	Condition       *Expression
	Variables       *Variables
	Jobs            *List[Job]
	LockBehavior    *Enum
	Trigger         *Enum
	IsSkippable     *Bool
	TemplateContext *Any
	Tail[StageDef]
}

// StageTemplate is `- template: path` in a stages list.
type StageTemplate struct {
	Pos
	TemplateRef
	Tail[StageTemplate]
}

func (*StageDef) stageNode()      {}
func (*StageTemplate) stageNode() {}

// Job is one item of a jobs list: JobDef, Deployment or JobTemplate.
type Job interface {
	Node
	jobNode()
}

// JobDef is `- job: id`.
type JobDef struct {
	Pos
	Job                    *Scalar
	DisplayName            *Scalar
	DependsOn              *DependsOn
	Condition              *Expression
	ContinueOnError        *Bool
	TimeoutInMinutes       *Scalar
	CancelTimeoutInMinutes *Scalar
	Variables              *Variables
	Strategy               *Strategy
	Pool                   *Pool
	Container              *Container
	Services               *Map[*Scalar]
	ServiceSpecs           *Map[*Any]
	Workspace              *Workspace
	Uses                   *Uses
	Steps                  *List[Step]
	TemplateContext        *Any
	Tail[JobDef]
}

// Deployment is `- deployment: id`.
type Deployment struct {
	Pos
	Deployment             *Scalar
	DisplayName            *Scalar
	DependsOn              *DependsOn
	Condition              *Expression
	ContinueOnError        *Bool
	TimeoutInMinutes       *Scalar
	CancelTimeoutInMinutes *Scalar
	Variables              *Variables
	Pool                   *Pool
	Environment            *Environment
	Strategy               *DeploymentStrategy
	Workspace              *Workspace
	Uses                   *Uses
	Container              *Container
	Services               *Map[*Scalar]
	ServiceSpecs           *Map[*Any]
	TemplateContext        *Any
	Tail[Deployment]
}

// JobTemplate is `- template: path` in a jobs list.
type JobTemplate struct {
	Pos
	TemplateRef
	Tail[JobTemplate]
}

func (*JobDef) jobNode()      {}
func (*Deployment) jobNode()  {}
func (*JobTemplate) jobNode() {}

// Environment is a deployment target: a `name.resource` string or the object form.
type Environment struct {
	Pos
	Insertable
	// Short is true when written as a single string.
	Short        bool
	Name         *Scalar
	ResourceName *Scalar
	ResourceID   *Scalar
	ResourceType *Scalar
	Tags         *Scalar
	Tail[Environment]
}

// DeploymentStrategy selects runOnce, rolling or canary.
type DeploymentStrategy struct {
	Pos
	Insertable
	RunOnce *Lifecycle
	Rolling *Lifecycle
	Canary  *Lifecycle
	Tail[DeploymentStrategy]
}

// Lifecycle holds the lifecycle hooks of a deployment strategy.
type Lifecycle struct {
	Pos
	Insertable
	// MaxParallel is only defined for rolling.
	MaxParallel *Scalar
	// Increments is only defined for canary.
	Increments       *List[*Scalar]
	PreDeploy        *Hook
	Deploy           *Hook
	RouteTraffic     *Hook
	PostRouteTraffic *Hook
	On               *OnHooks
	Tail[Lifecycle]
}

// OnHooks holds the on.failure and on.success hooks.
type OnHooks struct {
	Pos
	Insertable
	Failure *Hook
	Success *Hook
	Tail[OnHooks]
}

// Hook is one lifecycle hook: steps and an optional pool.
type Hook struct {
	Pos
	Insertable
	Steps *List[Step]
	Pool  *Pool
	Tail[Hook]
}

// Step is one item of a steps list.
type Step interface {
	Node
	stepNode()
}

// StepProps are the properties common to all steps except template references.
type StepProps struct {
	Name                    *Scalar
	DisplayName             *Scalar
	Condition               *Expression
	ContinueOnError         *Bool
	Enabled                 *Bool
	Env                     *Map[*Scalar]
	TimeoutInMinutes        *Scalar
	RetryCountOnTaskFailure *Scalar
	Target                  *Target
}

// Target is the execution context of a step: a name or the object form.
type Target struct {
	Pos
	Insertable
	// Short is true when written as a single string.
	Short             bool
	Container         *Scalar
	Commands          *Enum
	SettableVariables *SettableVariables
	Tail[Target]
}

// SettableVariables restricts which variables a step can set: `none` or a list.
type SettableVariables struct {
	Pos
	Insertable
	// None is true for the scalar `none`.
	None bool
	List *List[*Scalar]
}

// ScriptKind is the keyword of a script step.
type ScriptKind uint8

const (
	// ScriptCmd is `script`.
	ScriptCmd ScriptKind = iota
	// ScriptBash is `bash`.
	ScriptBash
	// ScriptPwsh is `pwsh`.
	ScriptPwsh
	// ScriptPowerShell is `powershell`.
	ScriptPowerShell
)

// Keyword returns the YAML keyword of the script kind.
func (k ScriptKind) Keyword() string {
	switch k {
	case ScriptBash:
		return "bash"
	case ScriptPwsh:
		return "pwsh"
	case ScriptPowerShell:
		return "powershell"
	default:
		return "script"
	}
}

// ScriptStep is a script, bash, pwsh or powershell step.
type ScriptStep struct {
	Pos
	Kind                  ScriptKind
	Script                *Scalar
	ErrorActionPreference *Scalar
	FailOnStderr          *Scalar
	IgnoreLASTEXITCODE    *Scalar
	WorkingDirectory      *Scalar
	StepProps
	Tail[ScriptStep]
}

// TaskRef is a parsed `Name@version` or `GUID@version` task reference.
type TaskRef struct {
	// Name is the task name or, when IsGUID is true, the task GUID.
	Name   string
	IsGUID bool
	// Version is the text after `@`: a major version (`2`) or a full version (`0.3.1`).
	Version string
	// Major is the major version number, or -1 when unknown.
	Major int
	// Dynamic is true when the reference contains a template expression.
	Dynamic bool
	// Valid is true when the reference was well formed (or dynamic).
	Valid bool
}

// TaskStep is a `task` step. Inputs keep source order; aliases are not resolved.
type TaskStep struct {
	Pos
	Task   *Scalar
	Ref    TaskRef
	Inputs *Map[*Scalar]
	StepProps
	Tail[TaskStep]
}

// CheckoutStep is a `checkout` step.
type CheckoutStep struct {
	Pos
	Checkout                  *Scalar
	Clean                     *Bool
	FetchDepth                *Scalar
	FetchFilter               *Scalar
	FetchTags                 *Scalar
	Lfs                       *Scalar
	PersistCredentials        *Scalar
	Submodules                *Scalar
	Path                      *Scalar
	SparseCheckoutDirectories *Scalar
	SparseCheckoutPatterns    *Scalar
	WorkspaceRepo             *Bool
	StepProps
	Tail[CheckoutStep]
}

// DownloadStep is a `download` step.
type DownloadStep struct {
	Pos
	Download *Scalar
	Artifact *Scalar
	Patterns *Scalar
	StepProps
	Tail[DownloadStep]
}

// DownloadBuildStep is a `downloadBuild` step.
type DownloadBuildStep struct {
	Pos
	DownloadBuild *Scalar
	Artifact      *Scalar
	Path          *Scalar
	Patterns      *Scalar
	StepProps
	Tail[DownloadBuildStep]
}

// GetPackageStep is a `getPackage` step.
type GetPackageStep struct {
	Pos
	GetPackage *Scalar
	Path       *Scalar
	StepProps
	Tail[GetPackageStep]
}

// PublishStep is a `publish` or legacy `upload` step.
type PublishStep struct {
	Pos
	Publish  *Scalar
	Upload   *Scalar
	Artifact *Scalar
	StepProps
	Tail[PublishStep]
}

// ReviewAppStep is a `reviewApp` step.
type ReviewAppStep struct {
	Pos
	ReviewApp *Scalar
	StepProps
	Tail[ReviewAppStep]
}

// StepTemplate is `- template: path` in a steps list.
type StepTemplate struct {
	Pos
	TemplateRef
	Tail[StepTemplate]
}

func (*ScriptStep) stepNode()        {}
func (*TaskStep) stepNode()          {}
func (*CheckoutStep) stepNode()      {}
func (*DownloadStep) stepNode()      {}
func (*DownloadBuildStep) stepNode() {}
func (*GetPackageStep) stepNode()    {}
func (*PublishStep) stepNode()       {}
func (*ReviewAppStep) stepNode()     {}
func (*StepTemplate) stepNode()      {}
