package mapping

import "github.com/HarnageaGabriel/trasloco/internal/ir"

// pipelineBuilder assembles a small valid IR graph for mapper tests. Later
// mapping tasks use it from this package: chain the With calls and take
// Build. Every collection keeps the order of the calls (INV-01).
type pipelineBuilder struct {
	pipeline ir.Pipeline
}

// newPipeline starts a pipeline with the given ID and the current schema.
func newPipeline(id string) *pipelineBuilder {
	return &pipelineBuilder{pipeline: ir.Pipeline{ID: id, Name: id, SchemaVersion: ir.SchemaVersion}}
}

// withTrigger appends a trigger of the given kind.
func (b *pipelineBuilder) withTrigger(id string, kind ir.TriggerKind) *pipelineBuilder {
	b.pipeline.Triggers = append(b.pipeline.Triggers, ir.Trigger{ID: id, Kind: kind})
	return b
}

// withJob appends a job with one run step per stepID, and one stage holding
// it, so the stage and job graphs stay consistent.
func (b *pipelineBuilder) withJob(id string, stepIDs ...string) *pipelineBuilder {
	job := ir.Job{ID: id, Name: id}
	for _, stepID := range stepIDs {
		job.Steps = append(job.Steps, ir.Step{ID: stepID, Name: stepID, Kind: ir.StepRun})
	}
	b.pipeline.Jobs = append(b.pipeline.Jobs, job)
	b.pipeline.Stages = append(b.pipeline.Stages, ir.Stage{ID: "stage-" + id, Name: id, Jobs: []string{id}})
	return b
}

// build returns the assembled pipeline.
func (b *pipelineBuilder) build() *ir.Pipeline { return &b.pipeline }
