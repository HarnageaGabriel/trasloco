package ir

import "github.com/HarnageaGabriel/trasloco/internal/diag"

// NewPipeline returns an empty current-version pipeline with deterministic,
// exact metadata. Callers add jobs and a stage partition before validation.
func NewPipeline(id, name string) *Pipeline {
	return &Pipeline{
		Metadata:      DefaultMetadata(),
		SchemaVersion: SchemaVersion,
		ID:            id,
		Name:          name,
		Jobs:          []Job{},
		Stages:        []Stage{},
	}
}

// DefaultMetadata is the default for constructs produced without AI or loss.
func DefaultMetadata() Metadata {
	return Metadata{Fidelity: diag.FidelityExact, Provenance: diag.ProvenanceDeterministic}
}

// NewJob returns a job with an explicit success gate and result model.
func NewJob(id, name string) Job {
	return Job{
		Metadata: DefaultMetadata(),
		ID:       id,
		Name:     name,
		If:       SuccessCondition(PhaseRuntime),
		Steps:    []Step{},
		Result:   ResultSucceeded,
	}
}

// NewRunStep returns a script step with an explicit success gate and shell.
func NewRunStep(id, name, shell, script string) Step {
	return Step{
		Metadata: DefaultMetadata(),
		ID:       id,
		Name:     name,
		Kind:     StepRun,
		If:       SuccessCondition(PhaseRuntime),
		Run:      &RunStep{Shell: Shell(shell), Script: script},
	}
}

// SuccessCondition makes the normally implicit success gate explicit.
func SuccessCondition(phase Phase) *Cond {
	return &Cond{
		Phase: phase,
		Expr: &Expr{
			Kind: ExprCall,
			Call: &CallExpr{Function: FunctionSuccess},
		},
	}
}

// StringLiteral returns a portable string literal expression.
func StringLiteral(value string) Expr {
	return Expr{Kind: ExprLiteral, Literal: &Literal{Type: LiteralString, String: value}}
}

// AddJob appends a job and assigns it to the named stage. The stage must exist.
func (p *Pipeline) AddJob(stageID string, job Job) bool {
	for i := range p.Stages {
		if p.Stages[i].ID == stageID {
			p.Jobs = append(p.Jobs, job)
			p.Stages[i].Jobs = append(p.Stages[i].Jobs, job.ID)
			return true
		}
	}
	return false
}
