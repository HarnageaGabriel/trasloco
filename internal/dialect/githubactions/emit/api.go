// Package emit defines the public API of the GitHub Actions emitter.
//
// # Purity
//
// The package is pure: it performs no I/O, reads no clock, takes no
// context.Context (emission neither blocks nor waits) and holds no
// package-level mutable state (CONVENTIONS.md). The action lock and the naming
// policy are values, the emitter appends its findings to the collector the
// caller owns, and no observable order depends on Go map iteration.
//
// # Status
//
// Tranche T7b implements the machinery of docs/spec/emit-gha.md: the typed
// document model, the YAML writer with its element recorder, the conservation
// walker and the INV-12 checks. The mapping rules themselves are still
// missing, so Emit writes the section 2.5 placeholder document: the fixed
// scaffolding plus one placeholder job per IR job whose step names the
// registered code of the content the emitter cannot convert. Every IR node
// and present section 1.2 feature keeps exactly one not-yet-mapped
// disposition. The mapping rules land in T7c to T7f (triggers, runners,
// checkout, run steps, variables, conditions); they change this file's body,
// not its caller.
package emit

import (
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// Registered diagnostic codes of the emitter (docs/spec/emit-gha.md section
// 6). Every code is a fixed identifier of internal/diag/registry.yaml.
const (
	// approximationCode records an approximation with the difference given in
	// params (emit-gha.md applies it to the pipeline inputs row of the
	// master table).
	approximationCode = "TRL-CONV-0001"
	// unsupportedConstructCode records a present construct with no target
	// equivalent at all (emit-gha.md, Pipeline.concurrency).
	unsupportedConstructCode = "TRL-CONV-0002"
	// manualConversionCode is the registered diagnostic of a node the emitter
	// cannot convert safely. A node that cannot be converted becomes a
	// placeholder instead of being omitted, so that a migrated workflow fails
	// loudly rather than silently doing less.
	manualConversionCode = "TRL-CONV-0003"
	// triggerWithoutTargetCode records a trigger kind GitHub has no
	// equivalent for (emit-gha.md, trigger rows of section 5.1).
	triggerWithoutTargetCode = "TRL-CONV-0005"
	// actionTaskCode records a task step, whose catalog is M2-C.
	actionTaskCode = "TRL-CAT-0001"
	// emitterAdjustmentCode records an output the emitter had to adjust: a
	// synthesized event, a pipeline without jobs, or a file refused in
	// strict mode (emit-gha.md section 6).
	emitterAdjustmentCode = "TRL-EMIT-0001"
	// permissionWideningCode records a job-level token grant the emitter did
	// not render (emit-gha.md section 5.12, INV-02).
	permissionWideningCode = "TRL-EMIT-0003"
	// expressionDelimiterCode records a literal that contains the GitHub
	// expression delimiter and can therefore not be written
	// (emit-gha.md section 5.13, INV-05).
	expressionDelimiterCode = "TRL-EMIT-0006"
	// internalEmitterCode is the registered code of an emitter defect: a
	// conservation violation (INV-12), a naming inventory miss, or a file
	// that fails to re-parse. Such a result never yields a file (INV-10).
	internalEmitterCode = "TRL-EMIT-0002"
)

// TargetVisibility is the visibility of the repository tree the workflow is
// written into. The caller supplies it; the emitter never guesses it
// (emit-gha.md section 5.5).
type TargetVisibility string

const (
	// VisibilityUnknown means the caller does not know the target visibility. It
	// is the default and must not unlock public-only runner labels.
	VisibilityUnknown TargetVisibility = "unknown"
	// VisibilityPublic means every repository the workflow runs in is public, so
	// public-only runner labels are available.
	VisibilityPublic TargetVisibility = "public"
	// VisibilityPrivate means the repositories are private, so public-only runner
	// labels are not available.
	VisibilityPrivate TargetVisibility = "private"
)

// Options is the caller-supplied emitter policy. Its zero value is the
// documented default: no permission widening, unknown visibility, lenient.
type Options struct {
	// AllowPermissionWidening permits a job-level token grant above the checkout
	// baseline; without it such a grant is dropped with TRL-EMIT-0003.
	AllowPermissionWidening bool
	// TargetVisibility gates public-only runner labels. An empty value means
	// the same as VisibilityUnknown: the caller never implies a public target.
	TargetVisibility TargetVisibility
	// Strict suppresses the emitted file when a conversion diagnostic of error
	// severity was reported for the pipeline.
	Strict bool
}

// Naming is the naming service the emitter consumes. It is defined here, not in
// the package that implements it (CONVENTIONS.md): internal/naming satisfies it
// structurally. Every method returns the target name of one source name; an
// error means the inventory has no decision for that source, never a silently
// invented name.
type Naming interface {
	// WorkflowFile returns the filename of the pipeline's workflow file.
	WorkflowFile(name string) (string, error)
	// JobID returns the GitHub job identifier of one IR job.
	JobID(stageID, jobID string) (string, error)
	// StepID returns the GitHub step identifier of one IR step of a job.
	StepID(jobID, stepID string) (string, error)
	// SecretName returns the GitHub Actions secret name of one secret reference.
	SecretName(secret ir.SecretRef) (string, error)
}

// OutputElement is one element of the emitted document, in file traversal order.
type OutputElement struct {
	// Path is the output file path plus a JSON Pointer (RFC 6901) into its
	// typed document, joined to the file path by "#".
	Path string
	// NodeID is the qualified IR node ID the element was written for.
	NodeID string
}

// File is one emitted file.
type File struct {
	// Path is the repository-relative output path.
	Path string
	// Data is the complete file content, with LF line endings.
	Data []byte
}

// DispositionKind is the disposition of one IR node (emit-gha.md section 2.3).
type DispositionKind string

const (
	// DispositionEmitted means the node is rendered with the semantics of its
	// mapping rule and carries that rule's fidelity grade.
	DispositionEmitted DispositionKind = "emitted"
	// DispositionDefault means the node is not rendered because the GitHub
	// default is identical; its fidelity grade is exact.
	DispositionDefault DispositionKind = "default"
	// DispositionPlaceholder means the body of the node is replaced by the
	// failing placeholder of emit-gha.md section 2.5; needs-manual.
	DispositionPlaceholder DispositionKind = "placeholder"
	// DispositionDropped means the node is not rendered and a diagnostic states
	// what behavior is lost.
	DispositionDropped DispositionKind = "dropped"
	// DispositionCarried means the reader already recorded the loss in
	// extensions, so the emitter renders the rest and adds no diagnostic.
	DispositionCarried DispositionKind = "carried"
)

// Disposition is the outcome of exactly one IR node under its stable ID.
type Disposition struct {
	// ID is the qualified IR node ID, including its kind and owning scope.
	ID string
	// Kind is the disposition the emitter applied.
	Kind DispositionKind
	// Fidelity is the fidelity grade this node contributes to the report.
	Fidelity diag.Fidelity
}

// Result is the complete outcome of emitting one pipeline.
type Result struct {
	// Files are the emitted files: zero or one workflow file.
	Files []File
	// Diagnostics are the findings of this emission, in emission order. The same
	// values were added to the caller's collector when one was supplied.
	Diagnostics []diag.Diagnostic
	// Dispositions holds exactly one entry per IR node, sorted by ID in byte
	// order.
	Dispositions []Disposition
	// OutputElements are the writer records of the emitted file, in file
	// traversal order.
	OutputElements []OutputElement
	// RequiredSecrets is the sorted, de-duplicated list of secrets the file
	// references. It never contains a value.
	RequiredSecrets []string
	// Blocked reports that the result must not be applied as a converted
	// workflow: either a conversion diagnostic of error severity was reported, or
	// no mapping rule could run yet.
	Blocked bool
}

// Emit converts one validated pipeline into GitHub Actions output.
//
// The pipeline is validated input: Emit does not re-validate it and never
// panics. A nil pipeline carries no nodes, so the result is blocked and empty.
//
// T7b implements no mapping rules (see the package comment). Emit therefore
// renders the section 2.5 placeholder file: the scaffolding of section 4.1
// (name, the synthesized workflow_dispatch event, the deny-all permissions,
// jobs) and one placeholder job per IR job. Every IR node keeps the
// not-yet-mapped disposition its spec rules prescribe; every present
// out-of-scope feature keeps the disposition its master-table row assigns.
// A pipeline without jobs yields no file (TRL-EMIT-0001, section 2.2);
// Options.Strict suppresses the file when an error-severity diagnostic was
// collected. T7b results are always blocked, because
// no node is truly converted yet: the emitted pipeline disposition only
// claims the scaffolding.
//
// A conservation violation (INV-12), a naming inventory miss or an
// unparseable file is an internal defect: the result reports TRL-EMIT-0002,
// yields no file and blocks (never a panic, never a silent drop).
// Diagnostics are attributed to the origin of the IR node that caused them
// (ADR 0003) and carry identifiers only, never literal source values (INV-05).
//
//nolint:revive // The lock is consumed by later mapping tranches; T7b renders none of its keys.
func Emit(pipeline *ir.Pipeline, actions catalog.ActionsLock, naming Naming, options Options, findings *diag.Collector) Result {
	if pipeline == nil {
		return Result{Blocked: true}
	}
	requirements := walkPipeline(pipeline)
	nameUnrepresentable := pipeline.Name != "" && strings.Contains(pipeline.Name, "${{")

	noFile := func(note pipelineNoteKind, message string) Result {
		dispositions, diagnostics := requirementOutcomes(requirements, outcomeContext{
			note: note, message: message, noFile: true, nameUnrepresentable: nameUnrepresentable,
		})
		return assembledResult(nil, nil, dispositions, diagnostics, findings)
	}
	if len(pipeline.Jobs) == 0 {
		return noFile(pipelineNoteNoJobs, "")
	}

	files, elements, failure := renderPlaceholderDocument(pipeline, naming)
	if failure != nil {
		// The detail may embed source names (INV-05); it stays out of the
		// diagnostic.
		return noFile(pipelineNoteInternal, internalFailureMessage)
	}
	dispositions, diagnostics := requirementOutcomes(requirements, outcomeContext{
		note: pipelineNoteEmitted, nameUnrepresentable: nameUnrepresentable,
	})
	if options.Strict && hasErrorDiagnostic(diagnostics) {
		return noFile(pipelineNoteStrict, "")
	}
	if err := checkConservation(requirements, dispositions, elements, len(files)); err != nil {
		return noFile(pipelineNoteInternal, internalFailureMessage)
	}
	return assembledResult(files, elements, dispositions, diagnostics, findings)
}

// internalFailureMessage is the fixed message of an internal emitter defect.
const internalFailureMessage = "internal emitter error; no workflow file was written"

// hasErrorDiagnostic reports whether any collected diagnostic has error
// severity (the Options.Strict trigger, emit-gha.md 2.2).
func hasErrorDiagnostic(diagnostics []diag.Diagnostic) bool {
	for _, finding := range diagnostics {
		if finding.Severity == diag.SeverityError {
			return true
		}
	}
	return false
}

// outcomeContext carries what a disposition needs to know about the output of
// this emission: why the file was or was not written, and whether the
// pipeline name is unrepresentable (emit-gha.md section 5.13).
type outcomeContext struct {
	// note is the pipeline outcome of this emission.
	note pipelineNoteKind
	// message is the internal defect text of a pipelineNoteInternal result.
	message string
	// noFile reports that the result carries no file, so no node has a
	// placeholder.
	noFile bool
	// nameUnrepresentable reports a pipeline name containing the GitHub
	// expression delimiter (emit-gha.md section 5.13).
	nameUnrepresentable bool
}

// pipelineNoteKind says why a result has no emitted file, if it has none at
// all (docs/spec/emit-gha.md sections 2.2, 2.5, 5.2).
type pipelineNoteKind int

const (
	// pipelineNoteEmitted: the placeholder document was written, so the
	// pipeline scaffolding is emitted.
	pipelineNoteEmitted pipelineNoteKind = iota
	// pipelineNoteNoJobs: the pipeline has no jobs, so nothing can be written
	// (TRL-EMIT-0001).
	pipelineNoteNoJobs
	// pipelineNoteStrict: Options.Strict refused the placeholder file
	// (TRL-EMIT-0001).
	pipelineNoteStrict
	// pipelineNoteInternal: an internal defect stopped the emission
	// (TRL-EMIT-0002).
	pipelineNoteInternal
)

// requirementOutcomes produces the dispositions and diagnostics of every
// requirement, in ID byte order. Because the walker output is already sorted
// by ID and diagnostics are emitted in the same order, both lists are sorted
// (INV-01).
func requirementOutcomes(requirements []requirement, context outcomeContext) ([]Disposition, []diag.Diagnostic) {
	dispositions := make([]Disposition, 0, len(requirements))
	diagnostics := make([]diag.Diagnostic, 0, len(requirements)+8)
	for _, r := range requirements {
		dispositions = append(dispositions, requirementDisposition(r, context))
		diagnostics = append(diagnostics, requirementFindings(r, context)...)
	}
	return dispositions, diagnostics
}

// requirementDisposition returns the disposition of one requirement (2.3).
// The pipeline node is special: its scaffolding is in the file if a file was
// written (emitted, approximate because the event was synthesized), and
// simply absent otherwise (dropped).
func requirementDisposition(r requirement, context outcomeContext) Disposition {
	if r.isFeature() {
		if r.Carried {
			return Disposition{ID: r.ID, Kind: DispositionCarried, Fidelity: r.Fidelity}
		}
		rule := ruleForFeature(r.Kind, r.Feature)
		if context.noFile && rule.kind == DispositionPlaceholder {
			return noFilePlaceholder(r)
		}
		return Disposition{ID: r.ID, Kind: rule.kind, Fidelity: rule.fidelity}
	}
	switch r.Kind {
	case "pipeline":
		if context.note == pipelineNoteEmitted {
			return Disposition{ID: r.ID, Kind: DispositionEmitted, Fidelity: diag.FidelityApproximated}
		}
		return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityApproximated}
	case "input":
		return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityApproximated}
	case "trigger":
		switch r.TriggerKind {
		case ir.TriggerUpstream, ir.TriggerWebhook, ir.TriggerOther:
			return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityUnsupported}
		default:
			return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityNeedsManual}
		}
	case "variable", "stage":
		return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityNeedsManual}
	case "job", "step":
		if context.noFile {
			return noFilePlaceholder(r)
		}
		return Disposition{ID: r.ID, Kind: DispositionPlaceholder, Fidelity: diag.FidelityNeedsManual}
	default:
		// Unreachable: the walker only produces the kinds above. The branch
		// keeps the contract total; it is reported, never silent (2.3).
		return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityNeedsManual}
	}
}

// noFilePlaceholder is the disposition of a node whose placeholder would
// live in a file when the result has no file. docs/spec/emit-gha.md 2.3
// defines no rule for this case beyond "no-file results have no emitted
// dispositions", so the node is dropped as unsupported: there is no
// placeholder to review, and the node diagnostic (resource = node ID) still
// states the loss. Spec gap, reported to the caller.
func noFilePlaceholder(r requirement) Disposition {
	return Disposition{ID: r.ID, Kind: DispositionDropped, Fidelity: diag.FidelityUnsupported}
}

// requirementFindings returns the diagnostics of one requirement. Every node
// states why it is not really converted yet, unless the reader already owns
// the loss (carried, no diagnostic); a node may carry several findings
// (emit-gha.md 2.4).
func requirementFindings(r requirement, context outcomeContext) []diag.Diagnostic {
	if r.isFeature() {
		if r.Carried {
			return nil
		}
		rule := ruleForFeature(r.Kind, r.Feature)
		return []diag.Diagnostic{newFinding(rule.code, r)}
	}
	switch r.Kind {
	case "pipeline":
		var opts []diag.Option
		if message, ok := pipelineNoteMessage(context); ok {
			opts = append(opts, diag.WithMessage(message))
		}
		findings := []diag.Diagnostic{newFinding(codePipelineNote(context), r, opts...)}
		if context.nameUnrepresentable || r.unrepresentableName {
			findings = append(findings, newFinding(expressionDelimiterCode, r,
				diag.WithParams(map[string]any{"field": "name"})))
		}
		return findings
	case "input":
		return []diag.Diagnostic{newFinding(approximationCode, r, diag.WithParams(map[string]any{
			"difference": "pipeline inputs are not exposed as workflow inputs in M2-A",
		}))}
	case "trigger":
		switch r.TriggerKind {
		case ir.TriggerUpstream, ir.TriggerWebhook, ir.TriggerOther:
			return []diag.Diagnostic{newFinding(triggerWithoutTargetCode, r)}
		default:
			return []diag.Diagnostic{newFinding(manualConversionCode, r)}
		}
	case "variable", "stage":
		return []diag.Diagnostic{newFinding(manualConversionCode, r)}
	case "job":
		findings := []diag.Diagnostic{newFinding(manualConversionCode, r)}
		if r.unrepresentableName {
			findings = append(findings, newFinding(expressionDelimiterCode, r,
				diag.WithParams(map[string]any{"field": "name"})))
		}
		return findings
	case "step":
		if r.StepKind == ir.StepAction {
			return []diag.Diagnostic{newFinding(actionTaskCode, r)}
		}
		return []diag.Diagnostic{newFinding(manualConversionCode, r)}
	default:
		return []diag.Diagnostic{newFinding(manualConversionCode, r)}
	}
}

// codePipelineNote returns the diagnostic code of the pipeline outcome of
// this emission.
func codePipelineNote(context outcomeContext) string {
	if context.note == pipelineNoteInternal {
		return internalEmitterCode
	}
	return emitterAdjustmentCode
}

// pipelineNoteMessage overrides the finding message of the pipeline outcome.
func pipelineNoteMessage(context outcomeContext) (string, bool) {
	if context.message != "" {
		return context.message, true
	}
	switch context.note {
	case pipelineNoteNoJobs:
		return "the pipeline has no jobs; no workflow file was written", true
	case pipelineNoteStrict:
		return "Options.Strict refused the placeholder workflow file", true
	default:
		return "", false
	}
}

// featureRule returns the disposition and diagnostic of one present
// out-of-scope feature (emit-gha.md section 5.1 master table). The default is
// the most conservative spec-defined option for unmodelled content: dropped
// with the manual-conversion diagnostic, never silent (2.3).
type featureRule struct {
	kind     DispositionKind
	code     string
	fidelity diag.Fidelity
}

func ruleForFeature(nodeKind string, feature string) featureRule {
	switch {
	case nodeKind == "pipeline" && feature == "concurrency":
		return featureRule{DispositionDropped, unsupportedConstructCode, diag.FidelityUnsupported}
	case nodeKind == "job" && feature == "permissions":
		return featureRule{DispositionDropped, permissionWideningCode, diag.FidelityApproximated}
	case nodeKind == "job":
		// container, services, matrix, deployment, outputs, uses, concurrency
		// (emit-gha.md master table, job features row: the job placeholder
		// replaces the whole job body).
		return featureRule{DispositionPlaceholder, manualConversionCode, diag.FidelityNeedsManual}
	case nodeKind == "step":
		// checkout-path, checkout-repository, run-operations (sections 5.6 to
		// 5.8: the step placeholder replaces the step).
		return featureRule{DispositionPlaceholder, manualConversionCode, diag.FidelityNeedsManual}
	case nodeKind == "stage":
		// A non-default stage condition is not rendered; its jobs are
		// placeholders (section 5.1, stage row and plan table).
		return featureRule{DispositionDropped, manualConversionCode, diag.FidelityNeedsManual}
	default:
		return featureRule{DispositionDropped, manualConversionCode, diag.FidelityNeedsManual}
	}
}

// newFinding builds a registered finding attributed to the requirement's node
// and origin (ADR 0003). The resource carries identifiers only, never
// secrets (INV-04).
func newFinding(code string, r requirement, options ...diag.Option) diag.Diagnostic {
	opts := []diag.Option{diag.WithResource(diag.Resource{Kind: r.Kind, Key: r.ID})}
	if r.Source != nil {
		opts = append(opts, diag.WithSource(*r.Source))
	}
	opts = append(opts, options...)
	return diag.MustNew(code, opts...)
}

// namingMaps holds the precomputed target names the document needs: the
// workflow filename and the normalized GitHub job ID of every IR job.
type namingMaps struct {
	fileName string
	jobIDs   map[string]string
}

// internalFailure is an internal defect: an emission that cannot produce a
// file. It is never an input property; INV-10 forbids shipping its result as
// a file.
type internalFailure struct{ err error }

// placeholderRunsOn is the pinned label of a placeholder job
// (docs/spec/emit-gha.md 2.5: the mapped runner label, or ubuntu-24.04 when
// the runner itself is the problem; in T7b no runner is mapped).
const placeholderRunsOn = "ubuntu-24.04"

// renderPlaceholderDocument writes the placeholder document of section 2.5:
// the scaffolding of section 4.1 and one placeholder job per IR job. It
// returns the file plus its output elements, in file traversal order, or the
// internal defect that stopped it.
func renderPlaceholderDocument(pipeline *ir.Pipeline, naming Naming) ([]File, []OutputElement, *internalFailure) {
	namingMaps := namingMaps{}
	fileName, err := naming.WorkflowFile(pipeline.Name)
	if err != nil {
		return nil, nil, &internalFailure{fmt.Errorf("workflow file name: %w", err)}
	}
	namingMaps.fileName = fileName
	jobs := make(map[string]string, len(pipeline.Jobs))
	for _, job := range pipeline.Jobs {
		target, err := naming.JobID(stageIDOf(pipeline, job.ID), job.ID)
		if err != nil {
			return nil, nil, &internalFailure{fmt.Errorf("job %q ID: %w", job.ID, err)}
		}
		jobs[job.ID] = target
	}
	namingMaps.jobIDs = jobs

	needs := make([][]string, len(pipeline.Jobs))
	for index, job := range pipeline.Jobs {
		edges, failure := jobNeeds(pipeline, job, jobs)
		if failure != nil {
			return nil, nil, failure
		}
		needs[index] = edges
	}

	writer := newFileWriter(".github/workflows/" + namingMaps.fileName)
	pipelineID := qualifyID("pipeline", pipeline.ID)
	if pipeline.Name != "" && !strings.Contains(pipeline.Name, "${{") {
		writer.writeName(pipelineID, pipeline.Name)
	}
	writer.writeWorkflowDispatch(pipelineID)
	writer.writeWorkflowPermissions(pipelineID)
	writer.startJobs(pipelineID)
	for index, job := range pipeline.Jobs {
		writer.writePlaceholderJob(qualifyID("job", job.ID), namingMaps.jobIDs[job.ID], model.Job{
			Name:        jobDisplayName(job),
			Permissions: model.EmptyMap(),
			Needs:       needs[index],
			RunsOn:      placeholderRunsOn,
		}, manualConversionCode, originLine(job.Origin))
	}
	data, elements, err := writer.finalize()
	if err != nil {
		return nil, nil, &internalFailure{err}
	}
	path := ".github/workflows/" + namingMaps.fileName
	return []File{{Path: path, Data: data}}, elements, nil
}

// jobDisplayName is the display name key of 4.1 rule 5: the reader defaults
// the display name to the IR ID, so the key differs only when the display
// name differs. A name containing the expression delimiter is omitted
// (emit-gha.md section 5.13).
func jobDisplayName(job ir.Job) string {
	if job.Name == job.ID || strings.Contains(job.Name, "${{") {
		return ""
	}
	return job.Name
}

// jobNeeds computes the dependency list of one job (5.4 rule 2): the IR
// needs entries of the job first, then the jobs of every stage of its stage
// dependsOn list in that order, then every job of all preceding stages when
// the barrier is set. Entries keep the first occurrence, and every entry is
// rewritten with the naming inventory (5.4 rule 1). A reference the pipeline
// does not declare is an internal defect of the input contract: validated
// input never contains one.
func jobNeeds(pipeline *ir.Pipeline, job ir.Job, jobIDs map[string]string) ([]string, *internalFailure) {
	var edges []string
	appendEdge := func(referenced string) *internalFailure {
		target, ok := jobIDs[referenced]
		if !ok {
			return &internalFailure{fmt.Errorf("needs entry %q is not a declared job", referenced)}
		}
		for _, existing := range edges {
			if existing == target {
				return nil
			}
		}
		edges = append(edges, target)
		return nil
	}
	for _, need := range job.Needs {
		if failure := appendEdge(need); failure != nil {
			return nil, failure
		}
	}
	stageIndex, hasStage := jobStageIndex(pipeline, job.ID)
	if !hasStage {
		return edges, nil
	}
	stage := pipeline.Stages[stageIndex]
	for _, dependency := range stage.DependsOn {
		dependencyIndex, ok := stageIndexByName(pipeline, dependency)
		if !ok {
			return nil, &internalFailure{fmt.Errorf("stage dependsOn %q is not a declared stage", dependency)}
		}
		for _, member := range pipeline.Stages[dependencyIndex].Jobs {
			if failure := appendEdge(member); failure != nil {
				return nil, failure
			}
		}
	}
	if stage.Barrier {
		for index := 0; index < stageIndex; index++ {
			for _, member := range pipeline.Stages[index].Jobs {
				if failure := appendEdge(member); failure != nil {
					return nil, failure
				}
			}
		}
	}
	return edges, nil
}

// jobStageIndex returns the index of the IR stage the job is a member of.
func jobStageIndex(pipeline *ir.Pipeline, jobID string) (int, bool) {
	for index, stage := range pipeline.Stages {
		for _, member := range stage.Jobs {
			if member == jobID {
				return index, true
			}
		}
	}
	return 0, false
}

// stageIDOf returns the IR stage ID a job is a member of.
func stageIDOf(pipeline *ir.Pipeline, jobID string) string {
	if index, ok := jobStageIndex(pipeline, jobID); ok {
		return pipeline.Stages[index].ID
	}
	return ""
}

// stageIndexByName returns the index of one IR stage ID.
func stageIndexByName(pipeline *ir.Pipeline, stageID string) (int, bool) {
	for index, stage := range pipeline.Stages {
		if stage.ID == stageID {
			return index, true
		}
	}
	return 0, false
}

// originLine is the start line of one node origin, zero for none.
func originLine(origin *ir.Origin) int {
	if origin == nil {
		return 0
	}
	return origin.Source.Line
}

// assembledResult adds every finding to the caller's collector and returns
// the result. Dispositions and diagnostics were produced in ID byte order
// (INV-01), and T7b results are always blocked: no node is really converted
// yet.
func assembledResult(files []File, elements []OutputElement, dispositions []Disposition, diagnostics []diag.Diagnostic, findings *diag.Collector) Result {
	if findings != nil {
		for _, finding := range diagnostics {
			findings.Add(finding)
		}
	}
	return Result{
		Files:           files,
		Diagnostics:     diagnostics,
		Dispositions:    dispositions,
		OutputElements:  elements,
		RequiredSecrets: nil,
		Blocked:         true,
	}
}
