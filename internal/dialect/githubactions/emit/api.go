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
// This is the API skeleton of tranche T7a. No mapping rule of
// docs/spec/emit-gha.md is implemented yet, so Emit emits no file, reports one
// needs-manual placeholder per IR node and blocks the result. The mapping rules
// land in T7b to T7e; they change this file's body, not its caller.
package emit

import (
	"sort"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// manualConversionCode is the registered diagnostic of a node the emitter
// cannot convert safely (emit-gha.md section 6). A node that cannot be converted
// becomes a placeholder instead of being omitted, so that a migrated workflow
// fails loudly rather than silently doing less.
const manualConversionCode = "TRL-CONV-0003"

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
	// TargetVisibility gates public-only runner labels. An empty value means the
	// same as VisibilityUnknown: the caller never implies a public target.
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
	// Path is the output file path plus a JSON Pointer into its typed document.
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
// T7a does not implement the mapping rules of docs/spec/emit-gha.md, so Emit
// emits no file, reports every IR node once as a needs-manual placeholder with
// manualConversionCode, and returns a blocked result. It never pretends to have
// emitted a node: section 1.2 feature IDs and the writer records of section 2.3
// are added by T7b, and the mappings by T7b to T7e.
//
// The actions lock, the naming policy and the options belong to the frozen
// emitter contract. T7a reads none of them because it renders nothing; the
// mapping tranches consume them in the rules that need them.
//
//nolint:revive // The lock, the naming policy and the options are consumed by T7b to T7e; T7a renders nothing.
func Emit(pipeline *ir.Pipeline, actions catalog.ActionsLock, naming Naming, options Options, findings *diag.Collector) Result {
	if pipeline == nil {
		return Result{Blocked: true}
	}
	nodes := pipelineNodes(pipeline)
	result := Result{
		Diagnostics:  make([]diag.Diagnostic, 0, len(nodes)),
		Dispositions: make([]Disposition, 0, len(nodes)),
		Blocked:      true,
	}
	for _, node := range nodes {
		result.Dispositions = append(result.Dispositions, Disposition{
			ID:       node.id,
			Kind:     DispositionPlaceholder,
			Fidelity: diag.FidelityNeedsManual,
		})
		findingOptions := []diag.Option{diag.WithResource(diag.Resource{Kind: node.kind, Key: node.id})}
		if node.source != nil {
			findingOptions = append(findingOptions, diag.WithSource(*node.source))
		}
		finding := diag.MustNew(manualConversionCode, findingOptions...)
		result.Diagnostics = append(result.Diagnostics, finding)
		if findings != nil {
			findings.Add(finding)
		}
	}
	return result
}

// irNode is one IR node of the conservation contract, with its qualified stable
// ID and the origin its diagnostics are attributed to (ADR 0003).
type irNode struct {
	id     string
	kind   string
	source *diag.Source
	seq    int
}

func pipelineNodes(pipeline *ir.Pipeline) []irNode {
	nodes := []irNode{{id: qualifyID("pipeline", pipeline.ID), kind: "pipeline", source: originSource(pipeline.Origin)}}
	for _, input := range pipeline.Inputs {
		nodes = append(nodes, irNode{id: qualifyID("input", input.ID), kind: "input", source: originSource(input.Origin)})
	}
	for _, trigger := range pipeline.Triggers {
		nodes = append(nodes, irNode{id: qualifyID("trigger", trigger.ID), kind: "trigger", source: originSource(trigger.Origin)})
	}
	nodes = appendVariableNodes(nodes, "pipeline:"+pipeline.ID, pipeline.Variables)
	for _, stage := range pipeline.Stages {
		nodes = append(nodes, irNode{id: qualifyID("stage", stage.ID), kind: "stage", source: originSource(stage.Origin)})
		nodes = appendVariableNodes(nodes, "stage:"+stage.ID, stage.Variables)
	}
	for _, job := range pipeline.Jobs {
		nodes = append(nodes, irNode{id: qualifyID("job", job.ID), kind: "job", source: originSource(job.Origin)})
		nodes = appendVariableNodes(nodes, "job:"+job.ID, job.Variables)
		for _, step := range job.Steps {
			nodes = append(nodes, irNode{id: qualifyID("step", job.ID, step.ID), kind: "step", source: originSource(step.Origin)})
		}
	}
	for index := range nodes {
		nodes[index].seq = index
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].id != nodes[j].id {
			return nodes[i].id < nodes[j].id
		}
		if nodes[i].kind != nodes[j].kind {
			return nodes[i].kind < nodes[j].kind
		}
		return nodes[i].seq < nodes[j].seq
	})
	return nodes
}

// appendVariableNodes appends the variable nodes declared by one owner. The
// declaration index keeps IDs unique when one owner declares a name twice; it
// is deterministic because IR collections are ordered.
func appendVariableNodes(nodes []irNode, owner string, variables []ir.Variable) []irNode {
	for index, variable := range variables {
		nodes = append(nodes, irNode{
			id:     qualifyID("variable", owner, strconv.Itoa(index), variable.Name),
			kind:   "variable",
			source: originSource(variable.Origin),
		})
	}
	return nodes
}

// qualifyID builds an unambiguous node ID: each component escapes "%" and "/"
// before the components are joined by "/". No component can therefore contain
// the separator, and the kind plus component path is unique.
func qualifyID(kind string, parts ...string) string {
	escaped := make([]string, len(parts))
	for index, part := range parts {
		escaped[index] = escapeIDPart(part)
	}
	return kind + ":" + strings.Join(escaped, "/")
}

func escapeIDPart(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), "/", "%2F")
}

// originSource returns the source position a node's diagnostics are attributed
// to, or nil when the node has no origin.
func originSource(origin *ir.Origin) *diag.Source {
	if origin == nil {
		return nil
	}
	source := origin.Source
	return &source
}
