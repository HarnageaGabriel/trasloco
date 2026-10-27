package emit

import (
	"sort"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// requirement represents one entry of the conservation contract
// (docs/spec/emit-gha.md 2.3): an IR node, a present out-of-scope feature of
// section 1.2, or a carried extension feature.
type requirement struct {
	// ID is the qualified stable ID the disposition is reported under.
	ID string
	// Kind is the IR node kind the requirement belongs to. It is also the
	// diagnostic resource kind.
	Kind string
	// feature is the section 1.2 feature name, empty for the node itself.
	Feature string
	// carried reports an extension feature: the reader already recorded the
	// loss in extensions, so the emitter adds no diagnostic (2.3, carried).
	Carried bool
	// Fidelity is the reader-recorded fidelity, used by carried features.
	Fidelity diag.Fidelity
	// Source is the node origin diagnostics are attributed to (ADR 0003).
	Source *diag.Source
	// TriggerKind is the kind of a trigger node, empty for other kinds.
	TriggerKind ir.TriggerKind
	// StepKind is the payload kind of a step node, empty for other kinds.
	StepKind ir.StepKind
	// unrepresentableName reports a node display name containing the GitHub
	// expression delimiter (emit-gha.md section 5.13).
	unrepresentableName bool
	// seq keeps the construction order inside an ID tiebreak.
	seq int
}

// isFeature reports whether the requirement is a feature of an IR node
// rather than the node itself.
func (r requirement) isFeature() bool { return r.Feature != "" }

// walkPipeline walks the validated IR and produces one requirement per IR
// node: pipeline, input, trigger, variable, stage, job and step, plus every
// present out-of-scope feature of emit-gha.md section 1.2 under
// `<node>:<nodeID>#<feature>`, plus one feature per extension key recorded by
// the reader. Node IDs use the scheme of section 2.3. The result is sorted by
// ID in byte order.
func walkPipeline(pipeline *ir.Pipeline) []requirement {
	if pipeline == nil {
		return nil
	}
	requirements := make([]requirement, 0, 16)
	add := func(r requirement) {
		r.seq = len(requirements)
		requirements = append(requirements, r)
	}
	addNodeKind := func(kind string, id string, name string, features []string, metadata ir.Metadata) {
		unrepresentableName := strings.Contains(name, "${{")
		add(requirement{
			ID:                  id,
			Kind:                kind,
			Fidelity:            metadata.Fidelity,
			Source:              originSource(metadata.Origin),
			unrepresentableName: unrepresentableName,
		})
		for _, feature := range features {
			add(requirement{
				ID:       id + "#" + feature,
				Kind:     kind,
				Feature:  feature,
				Fidelity: metadata.Fidelity,
				Source:   originSource(metadata.Origin),
			})
		}
		addExtensionFeatures(add, kind, id, metadata)
	}
	addNodeKind("pipeline", qualifyID("pipeline", pipeline.ID), pipeline.Name,
		pipelineFeatureNames(pipeline), pipeline.Metadata)
	for _, input := range pipeline.Inputs {
		addNodeKind("input", qualifyID("input", input.ID), "", nil, input.Metadata)
	}
	for _, trigger := range pipeline.Triggers {
		node := requirement{
			ID:          qualifyID("trigger", trigger.ID),
			Kind:        "trigger",
			TriggerKind: trigger.Kind,
			Fidelity:    trigger.Fidelity,
			Source:      originSource(trigger.Origin),
		}
		add(node)
		addExtensionFeatures(add, "trigger", node.ID, trigger.Metadata)
	}
	appendVariableRequirements(add, "pipeline:"+pipeline.ID, pipeline.Variables)
	for _, stage := range pipeline.Stages {
		addNodeKind("stage", qualifyID("stage", stage.ID), "", stageFeatureNames(&stage), stage.Metadata)
		appendVariableRequirements(add, "stage:"+stage.ID, stage.Variables)
	}
	for _, job := range pipeline.Jobs {
		addNodeKind("job", qualifyID("job", job.ID), job.Name, jobFeatureNames(&job), job.Metadata)
		appendVariableRequirements(add, "job:"+job.ID, job.Variables)
		for _, step := range job.Steps {
			node := requirement{
				ID:       qualifyID("step", job.ID, step.ID),
				Kind:     "step",
				StepKind: step.Kind,
				Fidelity: step.Fidelity,
				Source:   originSource(step.Origin),
			}
			add(node)
			for _, feature := range stepFeatureNames(&step) {
				add(requirement{
					ID:       node.ID + "#" + feature,
					Kind:     "step",
					Feature:  feature,
					Fidelity: step.Fidelity,
					Source:   originSource(step.Origin),
				})
			}
			addExtensionFeatures(add, "step", node.ID, step.Metadata)
		}
	}
	sortRequirements(requirements)
	return requirements
}

// appendVariableRequirements appends the variable requirements declared by
// one owner. The declaration index keeps IDs unique when one owner declares a
// name twice (emit-gha.md 2.3, R9); it is deterministic because IR
// collections are ordered.
func appendVariableRequirements(add func(requirement), owner string, variables []ir.Variable) {
	for index, variable := range variables {
		id := qualifyID("variable", owner, strconv.Itoa(index), variable.Name)
		add(requirement{
			ID:       id,
			Kind:     "variable",
			Fidelity: variable.Fidelity,
			Source:   originSource(variable.Origin),
		})
		addExtensionFeatures(add, "variable", id, variable.Metadata)
	}
}

// addExtensionFeatures records one carried feature per extension key the
// reader stored on the node. Extension keys are sorted so the result never
// depends on Go map iteration. Extension features carry no diagnostic
// (emit-gha.md 2.3, carried).
func addExtensionFeatures(add func(requirement), kind, id string, metadata ir.Metadata) {
	keys := make([]string, 0, len(metadata.Extensions))
	for key := range metadata.Extensions { //trasloco:unordered-ok keys are sorted before requirements are built
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		add(requirement{
			ID:       id + "#carried/" + key,
			Kind:     kind,
			Feature:  "carried/" + key,
			Carried:  true,
			Fidelity: metadata.Fidelity,
		})
	}
}

// pipelineFeatureNames returns the present out-of-scope features of a
// pipeline.
func pipelineFeatureNames(pipeline *ir.Pipeline) []string {
	var features []string
	if pipeline.Concurrency != nil {
		features = append(features, "concurrency")
	}
	return features
}

// stageFeatureNames returns the present stage features the first emitter
// subset does not render. A non-default stage condition is one (emit-gha.md
// section 5.1, stage row): its jobs become placeholders.
func stageFeatureNames(stage *ir.Stage) []string {
	if stage.If != nil {
		return []string{"if"}
	}
	return nil
}

// jobFeatureNames returns the present out-of-scope features of a job
// (emit-gha.md 1.2). The members are checked in list order, so the feature
// names are appended in the order of that list and never depend on Go map
// iteration.
func jobFeatureNames(job *ir.Job) []string {
	var features []string
	ordered := []struct {
		name    string
		present bool
	}{
		{"concurrency", job.Concurrency != nil},
		{"container", job.Container != nil},
		{"deployment", job.Deployment != nil},
		{"matrix", job.Matrix != nil},
		{"outputs", len(job.Outputs) > 0},
		{"permissions", job.Permissions.Len() > 0},
		{"services", len(job.Services) > 0},
		{"uses", job.Uses != nil},
	}
	for _, candidate := range ordered {
		if candidate.present {
			features = append(features, candidate.name)
		}
	}
	return features
}

// stepFeatureNames returns the present out-of-scope features of a step
// (emit-gha.md 1.2, sections 5.6-5.8).
func stepFeatureNames(step *ir.Step) []string {
	var features []string
	ordered := []struct {
		name    string
		present bool
	}{
		{"checkout-path", step.Checkout != nil && step.Checkout.Path != ""},
		{"checkout-repository", step.Checkout != nil && step.Checkout.Repository != "self"},
		{"run-operations", step.Run != nil && len(step.Run.Operations) > 0},
	}
	for _, candidate := range ordered {
		if candidate.present {
			features = append(features, candidate.name)
		}
	}
	return features
}

// sortRequirements orders requirements by ID in byte order, keeping the
// construction order for equal IDs.
func sortRequirements(requirements []requirement) {
	sort.SliceStable(requirements, func(i, j int) bool {
		if requirements[i].ID != requirements[j].ID {
			return requirements[i].ID < requirements[j].ID
		}
		return requirements[i].seq < requirements[j].seq
	})
}

// qualifyID builds an unambiguous node ID: each component escapes "%" and "/"
// before the components are joined by "/". No component can therefore contain
// the separator, and the kind plus component path is unique (emit-gha.md 2.3,
// ruling R9).
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

// originSource returns the source position a node's diagnostics are
// attributed to, or nil when the node has no origin.
func originSource(origin *ir.Origin) *diag.Source {
	if origin == nil {
		return nil
	}
	source := origin.Source
	return &source
}
