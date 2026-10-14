package ir

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/redact"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// Validate checks provider-neutral structural and graph invariants. Adapter
// capability checks belong after this function and do not weaken these rules.
func Validate(pipeline *Pipeline, redactors ...*redact.Redactor) []diag.Diagnostic {
	var r *redact.Redactor
	if len(redactors) > 0 {
		r = redactors[0]
	}
	validator := irValidator{pipeline: pipeline, diagnostics: diag.NewCollector(), redactor: r}
	validator.indexOrigins()
	validator.validate()
	return validator.diagnostics.List()
}

type irValidator struct {
	pipeline    *Pipeline
	diagnostics *diag.Collector
	redactor    *redact.Redactor
	origins     map[string]*Origin
	jobs        map[string]*Job
	jobStage    map[string]string
	outputs     map[OutputRef]bool
}

func (v *irValidator) validate() {
	if v.pipeline == nil {
		v.add("TRL-VAL-0007", "pipeline", "pipeline is nil")
		return
	}
	v.metadata("pipeline", v.pipeline.Metadata)
	if v.pipeline.SchemaVersion != SchemaVersion {
		v.add("TRL-VAL-0007", "schemaVersion", fmt.Sprintf("schemaVersion is %d, want %d", v.pipeline.SchemaVersion, SchemaVersion))
	}
	if v.pipeline.ID == "" {
		v.add("TRL-VAL-0007", "id", "pipeline id is required")
	}
	if v.pipeline.Name == "" {
		v.add("TRL-VAL-0007", "name", "pipeline name is required")
	}

	v.uniquePipelineIDs()
	v.indexJobsAndStages()
	v.validateStages()
	v.validateStageDAG()
	v.validateTriggersAndInputs()
	v.validateVariables("variables", v.pipeline.Variables)
	if v.pipeline.Concurrency != nil {
		if !oneOf(v.pipeline.Concurrency.Mode, ConcurrencyCancelInProgress, ConcurrencyQueueLatest, ConcurrencySerialize) {
			v.add("TRL-VAL-0012", "concurrency.mode", "invalid concurrency mode")
		}
		v.expr("concurrency.group", &v.pipeline.Concurrency.Group, "")
	}
	for index := range v.pipeline.Jobs {
		v.validateJob(index, &v.pipeline.Jobs[index])
	}
	v.validateDAG()
}

func (v *irValidator) uniquePipelineIDs() {
	check := func(namespace string, ids []string) {
		seen := make(map[string]bool, len(ids))
		for index, id := range ids {
			path := fmt.Sprintf("%s[%d].id", namespace, index)
			if id == "" {
				v.add("TRL-VAL-0007", path, "id is required")
				continue
			}
			if seen[id] {
				v.add("TRL-VAL-0008", path, fmt.Sprintf("duplicate %s id %q", namespace, id))
			}
			seen[id] = true
		}
	}
	inputs := make([]string, len(v.pipeline.Inputs))
	for index := range v.pipeline.Inputs {
		inputs[index] = v.pipeline.Inputs[index].ID
	}
	triggers := make([]string, len(v.pipeline.Triggers))
	for index := range v.pipeline.Triggers {
		triggers[index] = v.pipeline.Triggers[index].ID
	}
	jobs := make([]string, len(v.pipeline.Jobs))
	for index := range v.pipeline.Jobs {
		jobs[index] = v.pipeline.Jobs[index].ID
	}
	stages := make([]string, len(v.pipeline.Stages))
	for index := range v.pipeline.Stages {
		stages[index] = v.pipeline.Stages[index].ID
	}
	check("inputs", inputs)
	check("triggers", triggers)
	check("jobs", jobs)
	check("stages", stages)
}

func (v *irValidator) indexJobsAndStages() {
	v.jobs = make(map[string]*Job, len(v.pipeline.Jobs))
	for index := range v.pipeline.Jobs {
		job := &v.pipeline.Jobs[index]
		if _, exists := v.jobs[job.ID]; !exists {
			v.jobs[job.ID] = job
		}
	}
	v.jobStage = make(map[string]string, len(v.pipeline.Jobs))
	for stageIndex := range v.pipeline.Stages {
		stage := &v.pipeline.Stages[stageIndex]
		for jobIndex, jobID := range stage.Jobs {
			path := fmt.Sprintf("stages[%d].jobs[%d]", stageIndex, jobIndex)
			if _, exists := v.jobs[jobID]; !exists {
				v.add("TRL-VAL-0011", path, fmt.Sprintf("stage references unknown job %q", jobID))
				continue
			}
			if previous, exists := v.jobStage[jobID]; exists {
				v.add("TRL-VAL-0011", path, fmt.Sprintf("job %q belongs to stages %q and %q", jobID, previous, stage.ID))
				continue
			}
			v.jobStage[jobID] = stage.ID
		}
	}
	for _, jobID := range slices.Sorted(maps.Keys(v.jobs)) {
		if _, exists := v.jobStage[jobID]; !exists {
			v.add("TRL-VAL-0011", v.jobPath(jobID), fmt.Sprintf("job %q does not belong to a stage", jobID))
		}
	}
}

func (v *irValidator) validateStages() {
	if len(v.pipeline.Stages) == 0 && len(v.pipeline.Jobs) > 0 {
		v.add("TRL-VAL-0011", "stages", "non-empty jobs require a stage partition")
	}
	stageIDs := make(map[string]struct{}, len(v.pipeline.Stages))
	for index := range v.pipeline.Stages {
		stageIDs[v.pipeline.Stages[index].ID] = struct{}{}
	}
	for index := range v.pipeline.Stages {
		path := fmt.Sprintf("stages[%d]", index)
		stage := &v.pipeline.Stages[index]
		v.metadata(path, stage.Metadata)
		if stage.If != nil {
			v.cond(path+".if", stage.If, "")
		}
		v.validateVariables(path+".variables", stage.Variables)
		for i, dependency := range stage.DependsOn {
			if _, found := stageIDs[dependency]; !found {
				v.add("TRL-VAL-0010", fmt.Sprintf("%s.dependsOn[%d]", path, i), "stage dependency must reference an existing stage")
			}
		}
		if v.pipeline.Stages[index].Name == "" {
			v.add("TRL-VAL-0007", path+".name", "stage name is required")
		}
	}
}

func (v *irValidator) validateTriggersAndInputs() {
	for index := range v.pipeline.Inputs {
		input := &v.pipeline.Inputs[index]
		path := fmt.Sprintf("inputs[%d]", index)
		v.metadata(path, input.Metadata)
		if input.Name == "" {
			v.add("TRL-VAL-0007", path+".name", "input name is required")
		}
		if !oneOf(input.Type, InputString, InputBool, InputNumber, InputChoice) {
			v.add("TRL-VAL-0012", path+".type", "invalid input type")
		}
		if input.Type == InputChoice && len(input.Choices) == 0 {
			v.add("TRL-VAL-0007", path+".choices", "choice input requires choices")
		}
		if input.Default != nil {
			v.literal(path+".default", input.Default)
			if !inputDefaultMatches(input.Type, input.Choices, input.Default) {
				v.add("TRL-VAL-0012", path+".default", "input default does not match input type or choices")
			}
		}
	}
	for index := range v.pipeline.Triggers {
		trigger := &v.pipeline.Triggers[index]
		path := fmt.Sprintf("triggers[%d]", index)
		v.metadata(path, trigger.Metadata)
		if !oneOf(trigger.Kind, TriggerPush, TriggerPR, TriggerSchedule, TriggerManual, TriggerUpstream, TriggerWebhook, TriggerOther) {
			v.add("TRL-VAL-0012", path+".kind", "invalid trigger kind")
		}
		if trigger.Binding.Synthetic && trigger.Binding.InYAML {
			v.add("TRL-VAL-0012", path+".binding", "synthetic trigger cannot also be source YAML")
		}
		if (trigger.Kind == TriggerSchedule) != (trigger.Schedule != nil) {
			v.add("TRL-VAL-0012", path+".schedule", "schedule payload must appear exactly on schedule triggers")
		}
		if trigger.Schedule != nil && len(strings.Fields(trigger.Schedule.Cron)) != 5 {
			v.add("TRL-VAL-0012", path+".schedule.cron", "schedule cron must have five fields")
		}
		if trigger.Schedule != nil && (trigger.Schedule.Timezone == "" || trigger.Schedule.Original == "") {
			v.add("TRL-VAL-0007", path+".schedule", "schedule timezone and original spelling are required")
		}
		if (trigger.Kind == TriggerOther) != (trigger.Other != nil) {
			v.add("TRL-VAL-0012", path+".other", "other payload must appear exactly on other triggers")
		}
		if trigger.Other != nil {
			v.raw(path+".other", trigger.Other)
		}
	}
}

func (v *irValidator) validateJob(index int, job *Job) {
	path := fmt.Sprintf("jobs[%d]", index)
	v.metadata(path, job.Metadata)
	if job.Name == "" {
		v.add("TRL-VAL-0007", path+".name", "job name is required")
	}
	if job.If == nil {
		v.add("TRL-VAL-0007", path+".if", "job condition is required so the success gate is explicit")
	} else {
		v.cond(path+".if", job.If, job.ID)
	}
	if !oneOf(job.Result, ResultSucceeded, ResultSucceededWithIssues, ResultFailed, ResultCanceled, ResultSkipped) {
		v.add("TRL-VAL-0012", path+".result", "invalid result model")
	}
	for needIndex, need := range job.Needs {
		if _, exists := v.jobs[need]; !exists {
			v.add("TRL-VAL-0010", fmt.Sprintf("%s.needs[%d]", path, needIndex), fmt.Sprintf("dependency job %q does not exist", need))
		}
	}
	v.validateVariables(path+".variables", job.Variables)
	v.concurrency(path+".concurrency", job.Concurrency, job.ID)
	if job.Uses != nil {
		if job.Uses.Ref == "" {
			v.add("TRL-VAL-0007", path+".uses.ref", "reusable workflow ref is required")
		}
		if len(job.Steps) > 0 {
			v.add("TRL-VAL-0012", path, "reusable workflow call cannot contain steps")
		}
		for key, expr := range job.Uses.With.All() {
			v.expr(path+".uses.with."+key, &expr, job.ID)
		}
		for key, secret := range job.Uses.Secrets.All() {
			if secret.Name == "" {
				v.add("TRL-VAL-0007", path+".uses.secrets."+key, "secret reference name is required")
			}
		}
	}
	for key, permission := range job.Permissions.All() {
		if key == "" || !oneOf(permission, "read", "write", "none") {
			v.add("TRL-VAL-0012", path+".permissions."+key, "permission requires a name and read, write, or none")
		}
	}
	for needIndex, need := range job.Needs {
		if v.crossesBarrier(job.ID, need) {
			v.add("TRL-VAL-0010", fmt.Sprintf("%s.needs[%d]", path, needIndex), "dependency targets a later barrier stage")
		}
	}
	if job.Container != nil && job.Container.Image == "" {
		v.add("TRL-VAL-0007", path+".container.image", "container image is required")
	}
	for key, expr := range job.ContainerEnv().All() {
		v.expr(path+".container.env."+key, &expr, job.ID)
	}
	if job.Matrix != nil {
		v.validateMatrix(path+".matrix", job.Matrix)
	}
	if job.Deployment != nil && job.Deployment.Environment == "" {
		v.add("TRL-VAL-0007", path+".deployment.environment", "deployment environment is required")
	}
	serviceIDs := make(map[string]bool, len(job.Services))
	for serviceIndex := range job.Services {
		service := &job.Services[serviceIndex]
		servicePath := fmt.Sprintf("%s.services[%d]", path, serviceIndex)
		if service.ID == "" || service.Container.Image == "" {
			v.add("TRL-VAL-0007", servicePath, "service id and container image are required")
		} else if serviceIDs[service.ID] {
			v.add("TRL-VAL-0008", servicePath+".id", fmt.Sprintf("duplicate service id %q", service.ID))
		}
		serviceIDs[service.ID] = true
	}
	stepIDs := make(map[string]bool, len(job.Steps))
	v.outputs = make(map[OutputRef]bool)
	for stepIndex := range job.Steps {
		step := &job.Steps[stepIndex]
		stepPath := fmt.Sprintf("%s.steps[%d]", path, stepIndex)
		if step.ID == "" {
			v.add("TRL-VAL-0007", stepPath+".id", "step id is required")
		} else if stepIDs[step.ID] {
			v.add("TRL-VAL-0008", stepPath+".id", fmt.Sprintf("duplicate step id %q", step.ID))
		}
		stepIDs[step.ID] = true
		v.validateStep(stepPath, job.ID, step)
	}
	for outputIndex := range job.Outputs {
		output := &job.Outputs[outputIndex]
		if output.Name == "" {
			v.add("TRL-VAL-0007", fmt.Sprintf("%s.outputs[%d].name", path, outputIndex), "output name is required")
		}
		v.outputRef(fmt.Sprintf("%s.outputs[%d].from", path, outputIndex), job.ID, output.From)
	}
}

// ContainerEnv avoids a nil check in the validation traversal.
func (j *Job) ContainerEnv() OrderedMap[Expr] {
	if j.Container == nil {
		return nil
	}
	return j.Container.Env
}

func (v *irValidator) validateMatrix(path string, matrix *Matrix) {
	if matrix.Opaque != nil {
		v.raw(path+".opaque", matrix.Opaque)
		if len(matrix.Legs) != 0 {
			v.add("TRL-VAL-0012", path, "matrix must contain legs or opaque, never both")
		}
	}
	if len(matrix.Legs) == 0 && matrix.Opaque == nil {
		v.add("TRL-VAL-0007", path+".legs", "matrix requires at least one explicit leg")
	}
	seen := make(map[string]bool, len(matrix.Legs))
	for index := range matrix.Legs {
		leg := &matrix.Legs[index]
		legPath := fmt.Sprintf("%s.legs[%d]", path, index)
		if leg.ID == "" {
			v.add("TRL-VAL-0007", legPath+".id", "matrix leg id is required")
		} else if seen[leg.ID] {
			v.add("TRL-VAL-0008", legPath+".id", fmt.Sprintf("duplicate matrix leg id %q", leg.ID))
		}
		seen[leg.ID] = true
		for key, literal := range leg.Values.All() {
			v.literal(legPath+".values."+key, &literal)
		}
	}
	if matrix.MaxParallel < 0 {
		v.add("TRL-VAL-0012", path+".maxParallel", "maxParallel cannot be negative")
	}
}

func (v *irValidator) validateStep(path, jobID string, step *Step) {
	v.metadata(path, step.Metadata)
	if step.Name == "" {
		v.add("TRL-VAL-0007", path+".name", "step name is required")
	}
	if step.Retry < 0 {
		v.add("TRL-VAL-0012", path+".retry", "retry cannot be negative")
	}
	if step.If == nil {
		v.add("TRL-VAL-0007", path+".if", "step condition is required so the success gate is explicit")
	} else {
		v.cond(path+".if", step.If, jobID)
	}
	for key, expr := range step.Env.All() {
		v.expr(path+".env."+key, &expr, jobID)
	}
	payloads := 0
	for _, present := range []bool{
		step.Run != nil, step.Checkout != nil, step.ArtifactPublish != nil,
		step.ArtifactRestore != nil, step.Cache != nil, step.Action != nil,
		step.Operation != nil, step.Raw != nil,
	} {
		if present {
			payloads++
		}
	}
	if payloads != 1 || !stepPayloadMatches(step) {
		v.add("TRL-VAL-0012", path, "step kind must select exactly one matching payload")
	}
	if step.Run != nil {
		if step.Run.Shell == "" {
			v.add("TRL-VAL-0007", path+".run.shell", "run shell is required; defaults must be explicit")
		}
		for macroIndex := range step.Run.MacroRefs {
			v.macroRef(fmt.Sprintf("%s.run.macroRefs[%d]", path, macroIndex), &step.Run.MacroRefs[macroIndex])
		}
		for operationIndex := range step.Run.Operations {
			v.operation(fmt.Sprintf("%s.run.operations[%d]", path, operationIndex), jobID, step.ID, &step.Run.Operations[operationIndex])
		}
	}
	if step.Checkout != nil && step.Checkout.Ref.Kind == "" {
		v.add("TRL-VAL-0007", path+".checkout.ref", "checkout ref is required; PR merge refs must be explicit")
	}
	if step.Checkout != nil {
		if !oneOf(step.Checkout.RefSource, CheckoutRefSource(""), CheckoutRefLiteral, CheckoutRefSourceVersion) {
			v.add("TRL-VAL-0012", path+".checkout.refSource", "invalid checkout ref source")
		}
		if step.Checkout.FetchDepth < 0 {
			v.add("TRL-VAL-0012", path+".checkout.fetchDepth", "fetch depth must be non-negative")
		}
		// Depth 0 without fetchDepthSet means unspecified (VERIFY-13), so a
		// non-zero depth is only meaningful when the reader marked it
		// authoritative.
		if step.Checkout.FetchDepth != 0 && !step.Checkout.FetchDepthSet {
			v.add("TRL-VAL-0012", path+".checkout.fetchDepth", "a non-zero fetch depth requires fetchDepthSet")
		}
		v.expr(path+".checkout.ref", &step.Checkout.Ref, jobID)
	}
	if step.ArtifactPublish != nil {
		v.expr(path+".artifactPublish.path", &step.ArtifactPublish.Path, jobID)
	}
	if step.ArtifactRestore != nil {
		v.expr(path+".artifactRestore.path", &step.ArtifactRestore.Path, jobID)
	}
	if step.Cache != nil {
		v.expr(path+".cache.key", &step.Cache.Key, jobID)
		for index := range step.Cache.RestoreKeys {
			v.expr(fmt.Sprintf("%s.cache.restoreKeys[%d]", path, index), &step.Cache.RestoreKeys[index], jobID)
		}
		for index := range step.Cache.Paths {
			v.expr(fmt.Sprintf("%s.cache.paths[%d]", path, index), &step.Cache.Paths[index], jobID)
		}
	}
	if step.Action != nil {
		if step.Action.Provider == "" || step.Action.Name == "" || step.Action.Version == "" {
			v.add("TRL-VAL-0007", path+".action", "action provider, name, and version are required")
		}
		for key, expr := range step.Action.With.All() {
			v.expr(path+".action.with."+key, &expr, jobID)
		}
	}
	if step.Operation != nil {
		v.operation(path+".operation", jobID, step.ID, step.Operation)
	}
	if step.Raw != nil {
		v.raw(path+".raw", step.Raw)
	}
}

func stepPayloadMatches(step *Step) bool {
	switch step.Kind {
	case StepRun:
		return step.Run != nil
	case StepCheckout:
		return step.Checkout != nil
	case StepArtifactPublish:
		return step.ArtifactPublish != nil
	case StepArtifactRestore:
		return step.ArtifactRestore != nil
	case StepCache:
		return step.Cache != nil
	case StepAction:
		return step.Action != nil
	case StepSetOutput, StepSetEnv, StepSetPipelineVar, StepAddPath, StepMask, StepAnnotate:
		return step.Operation != nil && step.Operation.Kind == step.Kind
	case StepRaw:
		return step.Raw != nil
	default:
		return false
	}
}

func (v *irValidator) operation(path, jobID, stepID string, operation *ProtocolOperation) {
	if !oneOf(operation.Kind, StepSetOutput, StepSetEnv, StepSetPipelineVar, StepAddPath, StepMask, StepAnnotate) {
		v.add("TRL-VAL-0012", path+".kind", "invalid protocol operation kind")
	}
	switch operation.Kind {
	case StepSetOutput, StepSetEnv, StepSetPipelineVar:
		if operation.Name == "" || operation.Value == nil {
			v.add("TRL-VAL-0007", path, "setter operation requires name and value")
		}
	case StepAddPath, StepMask:
		if operation.Value == nil {
			v.add("TRL-VAL-0007", path+".value", "operation value is required")
		}
	case StepAnnotate:
		if !oneOf(operation.Level, "error", "warning") || operation.Message == nil {
			v.add("TRL-VAL-0012", path, "annotation requires error/warning level and message")
		}
	}
	if operation.Value != nil {
		v.expr(path+".value", operation.Value, jobID)
	}
	if operation.Message != nil {
		v.expr(path+".message", operation.Message, jobID)
	}
	if operation.Kind == StepSetOutput && operation.Name != "" {
		ref := OutputRef{Stage: v.jobStage[jobID], Job: jobID, Step: stepID, Name: operation.Name}
		v.outputs[ref] = true
	}
}

func (v *irValidator) validateVariables(path string, variables []Variable) {
	for index := range variables {
		variable := &variables[index]
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		v.metadata(itemPath, variable.Metadata)
		if variable.Name == "" {
			v.add("TRL-VAL-0007", itemPath+".name", "variable name is required")
		}
		if !oneOf(variable.Scope, ScopeOrganization, ScopeRepository, ScopePipeline, ScopeStage, ScopeJob, ScopeStep, ScopeEnvironment, ScopeGroup) {
			v.add("TRL-VAL-0012", itemPath+".scope", "invalid variable scope")
		}
		if variable.Secret != (variable.SecretRef != nil) || variable.Secret == (variable.Value != nil) {
			v.add("TRL-VAL-0012", itemPath, "variable must contain exactly one value or secretRef matching secret")
		}
		if variable.Value != nil {
			v.expr(itemPath+".value", variable.Value, "")
		}
		if variable.SecretRef != nil && variable.SecretRef.Name == "" {
			v.add("TRL-VAL-0007", itemPath+".secretRef.name", "secret reference name is required")
		}
	}
}

func (v *irValidator) cond(path string, condition *Cond, jobID string) {
	if !oneOf(condition.Phase, PhaseCompile, PhaseCreation, PhaseRuntime) {
		v.add("TRL-VAL-0012", path+".phase", "invalid condition phase")
	}
	if (condition.Expr == nil) == (condition.Opaque == nil) {
		v.add("TRL-VAL-0012", path, "condition requires exactly one of expr or opaque")
		return
	}
	if condition.Expr != nil {
		v.expr(path+".expr", condition.Expr, jobID)
	}
	if condition.Opaque != nil {
		v.raw(path+".opaque", condition.Opaque)
	}
}

func (v *irValidator) expr(path string, expression *Expr, jobID string) {
	if expression == nil {
		v.add("TRL-VAL-0007", path, "expression is required")
		return
	}
	payloads := 0
	for _, present := range []bool{expression.Literal != nil, expression.Ref != nil, expression.Call != nil, expression.Macro != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 || (expression.Kind == ExprLiteral && expression.Literal == nil) ||
		(expression.Kind == ExprRef && expression.Ref == nil) || (expression.Kind == ExprCall && expression.Call == nil) ||
		(expression.Kind == ExprMacro && expression.Macro == nil) {
		v.add("TRL-VAL-0012", path, "expression kind must select exactly one matching payload")
		return
	}
	switch expression.Kind {
	case ExprLiteral:
		v.literal(path+".literal", expression.Literal)
	case ExprRef:
		if !oneOf(expression.Ref.Context, RefVariables, RefParameters, RefEnv, RefSecrets, RefDependencyOutputs, RefMatrix, RefRunner, RefRun) {
			v.add("TRL-VAL-0012", path+".ref.context", "invalid reference context")
		}
		if expression.Ref.Context == RefDependencyOutputs {
			if expression.Ref.Output == nil {
				v.add("TRL-VAL-0007", path+".ref.output", "dependency output reference is required")
			} else {
				v.outputRef(path+".ref.output", jobID, *expression.Ref.Output)
			}
		} else if expression.Ref.Output != nil || len(expression.Ref.Path) == 0 {
			v.add("TRL-VAL-0012", path+".ref", "non-output reference requires a path and no output")
		}
	case ExprCall:
		if !validFunction(expression.Call.Function) {
			v.add("TRL-VAL-0012", path+".call.function", "function is outside the portable set")
		}
		for index := range expression.Call.Args {
			v.expr(fmt.Sprintf("%s.call.args[%d]", path, index), &expression.Call.Args[index], jobID)
		}
	case ExprMacro:
		v.macroExpr(path+".macro", expression.Macro)
	default:
		v.add("TRL-VAL-0012", path+".kind", "invalid expression kind")
	}
}

func (v *irValidator) macroExpr(path string, macro *MacroExpr) {
	if macro.Name == "" {
		v.add("TRL-VAL-0007", path+".name", "macro name is required")
	}
	if macro.Secret && !macro.Declared {
		v.add("TRL-VAL-0012", path, "secret macro must be declared")
	}
}

func (v *irValidator) macroRef(path string, macro *MacroRef) {
	if macro.Name == "" {
		v.add("TRL-VAL-0007", path+".name", "macro name is required")
	}
	if macro.Secret && !macro.Declared {
		v.add("TRL-VAL-0012", path, "secret macro must be declared")
	}
}

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (v *irValidator) literal(path string, literal *Literal) {
	if literal != nil && literal.Type == LiteralNumber && !decimalPattern.MatchString(literal.Number) {
		v.add("TRL-VAL-0012", path+".number", "number must be an exact decimal string")
	}
	if literal == nil || !oneOf(literal.Type, LiteralNull, LiteralString, LiteralBool, LiteralNumber) {
		v.add("TRL-VAL-0012", path+".type", "invalid literal type")
	}
}

func (v *irValidator) outputRef(path, consumerJob string, reference OutputRef) {
	if reference.Stage == "" || reference.Job == "" || reference.Step == "" || reference.Name == "" {
		v.add("TRL-VAL-0010", path, "output reference must contain stage, job, step, and name")
		return
	}
	if stage, exists := v.jobStage[reference.Job]; !exists || stage != reference.Stage {
		v.add("TRL-VAL-0010", path, fmt.Sprintf("output job %q is not in stage %q", reference.Job, reference.Stage))
		return
	}
	if consumerJob != "" && consumerJob != reference.Job && !contains(v.jobs[consumerJob].Needs, reference.Job) {
		v.add("TRL-VAL-0010", path, fmt.Sprintf("job %q does not depend on output job %q", consumerJob, reference.Job))
	}
	producer := v.jobs[reference.Job]
	resolved := false
	for stepIndex := range producer.Steps {
		step := &producer.Steps[stepIndex]
		if step.ID != reference.Step {
			continue
		}
		if step.Operation != nil && step.Operation.Kind == StepSetOutput && step.Operation.Name == reference.Name {
			resolved = true
		}
		if step.Run != nil {
			for operationIndex := range step.Run.Operations {
				operation := &step.Run.Operations[operationIndex]
				if operation.Kind == StepSetOutput && operation.Name == reference.Name {
					resolved = true
				}
			}
		}
	}
	if !resolved {
		v.add("TRL-VAL-0010", path, fmt.Sprintf("output %s/%s/%s/%s does not exist", reference.Stage, reference.Job, reference.Step, reference.Name))
	}
}

func (v *irValidator) validateDAG() {
	state := make(map[string]uint8, len(v.jobs))
	var visit func(string)
	visit = func(jobID string) {
		if state[jobID] == 2 {
			return
		}
		if state[jobID] == 1 {
			v.add("TRL-VAL-0009", v.jobPath(jobID), fmt.Sprintf("dependency cycle reaches job %q", jobID))
			return
		}
		state[jobID] = 1
		job := v.jobs[jobID]
		for _, dependency := range job.Needs {
			if _, exists := v.jobs[dependency]; exists {
				visit(dependency)
			}
		}
		state[jobID] = 2
	}
	for index := range v.pipeline.Jobs {
		visit(v.pipeline.Jobs[index].ID)
	}
}

func (v *irValidator) metadata(path string, metadata Metadata) {
	if !oneOf(metadata.Fidelity, diag.FidelityExact, diag.FidelityApproximated, diag.FidelityNeedsManual, diag.FidelityUnsupported) {
		v.add("TRL-VAL-0012", path+".fidelity", "invalid or missing fidelity")
	}
	if !oneOf(metadata.Provenance, diag.ProvenanceDeterministic, diag.ProvenanceLLM) {
		v.add("TRL-VAL-0012", path+".provenance", "invalid or missing provenance")
	}
	for _, key := range slices.Sorted(maps.Keys(metadata.Extensions)) {
		if !strings.HasPrefix(key, "x-") || len(key) < 3 {
			v.add("TRL-VAL-0012", path+".extensions."+key, "extension key must be x-<provider>")
		}
		if !json.Valid(metadata.Extensions[key]) {
			v.add("TRL-VAL-0012", path+".extensions."+key, "extension value must be valid JSON")
		}
	}
	if metadata.Origin != nil {
		origin := metadata.Origin
		if origin.Source.Platform == "" || origin.Source.File == "" || origin.OriginalTextHash == "" {
			v.add("TRL-VAL-0007", path+".origin", "origin provider, file, and originalTextHash are required")
		}
	}
}

func (v *irValidator) raw(path string, raw *Raw) {
	if raw.Provider == "" || raw.Kind == "" || raw.Format == "" {
		v.add("TRL-VAL-0007", path, "raw provider, kind, and format are required")
	}
	if (raw.Text == "") == (len(raw.JSON) == 0) {
		v.add("TRL-VAL-0012", path, "raw requires exactly one of text or json")
	}
	if len(raw.JSON) > 0 && !json.Valid(raw.JSON) {
		v.add("TRL-VAL-0012", path+".json", "raw json must be valid JSON")
	}
}

func (v *irValidator) add(code, path, message string) {
	options := []diag.Option{
		diag.WithMessage(message), diag.WithParams(map[string]any{"path": path}),
		diag.WithResource(diag.Resource{Kind: "pipeline", Key: pipelineKey(v.pipeline), Name: pipelineName(v.pipeline)}),
		diag.WithRedactor(v.redactor),
	}
	if origin := v.originAt(path); origin != nil {
		options = append(options, diag.WithSource(origin.Source))
	}
	v.diagnostics.Add(diag.MustNew(code, options...))
}

func pipelineKey(pipeline *Pipeline) string {
	if pipeline == nil {
		return ""
	}
	return pipeline.ID
}

func pipelineName(pipeline *Pipeline) string {
	if pipeline == nil {
		return ""
	}
	return pipeline.Name
}

func validFunction(function Function) bool {
	return oneOf(function,
		FunctionAnd, FunctionOr, FunctionNot, FunctionEqual, FunctionNotEqual,
		FunctionLess, FunctionLessOrEqual, FunctionGreater, FunctionGreaterOrEqual,
		FunctionContains, FunctionStartsWith, FunctionEndsWith, FunctionFormat, FunctionJoin,
		FunctionSuccess, FunctionFailure, FunctionAlways, FunctionCancelled, FunctionSucceededWithIssues,
	)
}

func inputDefaultMatches(inputType InputType, choices []string, literal *Literal) bool {
	if literal == nil {
		return true
	}
	switch inputType {
	case InputString:
		return literal.Type == LiteralString
	case InputBool:
		return literal.Type == LiteralBool
	case InputNumber:
		return literal.Type == LiteralNumber
	case InputChoice:
		return literal.Type == LiteralString && contains(choices, literal.String)
	default:
		return false
	}
}

func oneOf[T comparable](value T, allowed ...T) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
