package vars

import (
	"fmt"
)

// Table returns the exact consumer table, if present.
func (g *Graph) Table(scope Scope) (*Table, bool) {
	if g == nil {
		return nil, false
	}
	for i := range g.Tables {
		if g.Tables[i].Scope == scope {
			return &g.Tables[i], true
		}
	}
	return nil, false
}

// Resolve performs case-insensitive, phase-aware lookup. A candidate that
// carries a secret reference or an unknown value has an empty Value.
func (g *Graph) Resolve(scope Scope, name string, phase Phase) (Candidate, bool) {
	table, ok := g.Table(scope)
	if !ok {
		return Candidate{}, false
	}
	return lookup(table, name, phase)
}

func lookup(table *Table, name string, phase Phase) (Candidate, bool) {
	return lookupWork(table, name, phase, nil)
}

func lookupWork(table *Table, name string, phase Phase, work *evaluationWork) (Candidate, bool) {
	if i, ok := bindingIndex(table, key(name), work); ok {
		return effectiveWork(&table.Bindings[i], phase, work)
	}
	return Candidate{}, false
}

// effective picks the highest-precedence candidate visible in the phase. An
// unordered collision on that candidate makes the name unresolvable (never a
// guess); a later unambiguous candidate has already replaced it.
func effective(binding *Binding, phase Phase) (Candidate, bool) {
	return effectiveWork(binding, phase, nil)
}

// effectiveWork reads the cached index of the highest candidate visible in the
// phase, so a long run of unavailable candidates costs nothing.
func effectiveWork(binding *Binding, phase Phase, work *evaluationWork) (Candidate, bool) {
	top := binding.top
	if phase == PhaseCompile {
		top = binding.topCompile
	}
	if top == 0 {
		return Candidate{}, false
	}
	if work != nil {
		work.candidateSteps++
	}
	candidate := &binding.Candidates[top-1]
	if candidate.Contested {
		return Candidate{}, false
	}
	return cloneCandidate(*candidate), true
}

func (g *Graph) node(level, stage, job string) *Node {
	if g == nil {
		return nil
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Level == level && sameName(n.Stage, stage) && sameName(n.Job, job) {
			return n
		}
	}
	return nil
}

// visibleOutput applies the dependency edges taken from the AST: a job sees
// outputs of the jobs in its dependsOn within its stage and of the stages in
// its stage's dependsOn; a stage sees outputs of its dependsOn stages. A stage
// with no dependsOn key depends on the preceding stage; an explicit (even
// empty) dependsOn replaces that implicit edge. Visibility is one hop.
func (g *Graph) visibleOutput(consumer Scope, o Output) bool {
	switch consumer.Level {
	case "pipeline":
		return false
	case "stage":
		stage := g.node("stage", consumer.Stage, "")
		return stage != nil && containsFold(stage.DependsOn, o.Stage)
	}
	if sameName(o.Stage, consumer.Stage) {
		job := g.node("job", consumer.Stage, consumer.Job)
		return job != nil && containsFold(job.DependsOn, o.Job)
	}
	stage := g.node("stage", consumer.Stage, "")
	return stage != nil && containsFold(stage.DependsOn, o.Stage)
}

// ResolveOutput resolves an output key (step.name, job.step.name for a
// deployment job, leg.step.name for a matrix leg or hook variant) produced by
// producerJob in producerStage, enforcing the dependency edges of the graph.
// The last writer of the key wins. Secret outputs return a reference.
func (g *Graph) ResolveOutput(consumer Scope, producerStage, producerJob, outputKey string) (Candidate, bool) {
	if g == nil {
		return Candidate{}, false
	}
	for i := len(g.Outputs) - 1; i >= 0; i-- {
		output := g.Outputs[i]
		if !sameName(output.Stage, producerStage) || !sameName(output.Job, producerJob) || !sameName(output.Key(), outputKey) {
			continue
		}
		if !g.visibleOutput(consumer, output) || g.withheldOutput(output) {
			return Candidate{}, false
		}
		return cloneCandidate(output.Candidate), true
	}
	return Candidate{}, false
}

// withheldOutput reports an output that carries a provider-backed secret
// reference in a build whose pipeline secrets are withheld (fork policy). A
// secret a step itself declared with a logging command is not provider-backed.
func (g *Graph) withheldOutput(o Output) bool {
	if !g.secretsWithheld || !o.Candidate.Secret {
		return false
	}
	ref := o.Candidate.SecretRef
	return ref == nil || ref.Provider != "logging-command"
}

// EnvEntry is one process-environment variable of a step.
type EnvEntry struct {
	Name, Value string
	// Masked marks a value derived from a secret; Value is then empty and
	// SecretRefs names the references an emitter must bind instead.
	Masked     bool
	SecretRefs []SecretRef
	// Unknown marks a value only a running shell can produce; Value is empty.
	Unknown bool
	// Collision marks a name two variables project to; Value is then empty
	// because the winner would be a guess.
	Collision bool
}

// EnvironmentEntries returns Azure's automatic non-secret projection followed
// by the step's explicit env: mapping. Variables that merely embed a secret are
// projected without a value and flagged Masked: secrets are never mapped
// automatically. Explicit env entries that reference a secret carry the
// references and no value.
func (g *Graph) EnvironmentEntries(scope Scope) []EnvEntry {
	table, ok := g.Table(scope)
	if !ok {
		return nil
	}
	expander := g.newExpander(table, scope)
	result := make([]EnvEntry, 0, len(table.Bindings)+len(table.Env))
	seen := make(map[string]int, len(table.Bindings)) //trasloco:unordered-ok lookups only
	for i := range table.Bindings {
		candidate, found := effective(&table.Bindings[i], PhaseMacro)
		if !found || candidate.Secret {
			continue
		}
		entry := EnvEntry{Name: EnvironmentName(candidate.Name)}
		if candidate.Unknown {
			entry.Unknown = true
		} else {
			var acc accumulator
			value, resolved := expander.value(candidate.Name, nil, &acc)
			switch {
			case !resolved, overflowed(acc.diags):
				entry.Unknown = true
			case len(acc.refs) > 0:
				entry.Masked, entry.SecretRefs = true, acc.refs
			default:
				entry.Value = value
			}
		}
		if at, duplicate := seen[entry.Name]; duplicate {
			result[at] = EnvEntry{Name: entry.Name, Collision: true}
			continue
		}
		seen[entry.Name] = len(result)
		result = append(result, entry)
	}
	for _, mapping := range table.Env {
		expanded := expander.expandText(mapping.Value)
		entry := EnvEntry{Name: mapping.Name, Value: expanded.Text}
		if overflowed(expanded.Diagnostics) {
			// a value cut at the bound is never a valid environment value
			entry.Value, entry.Unknown = "", true
		} else if expanded.Masked {
			entry.Value, entry.Masked, entry.SecretRefs = "", true, expanded.SecretRefs
		}
		result = append(result, entry)
	}
	return result
}

// Environment returns the process environment as name/value pairs. It fails
// when an entry cannot be given a literal value (secret-derived, unknown or
// colliding), because the caller would otherwise write a placeholder as data;
// use EnvironmentEntries to see those entries and bind them explicitly.
func (g *Graph) Environment(scope Scope) ([][2]string, error) {
	entries := g.EnvironmentEntries(scope)
	if entries == nil {
		return nil, nil
	}
	result := make([][2]string, 0, len(entries))
	for _, entry := range entries {
		switch {
		case entry.Masked:
			return nil, fmt.Errorf("environment %s is derived from a secret; bind its secret reference", entry.Name)
		case entry.Unknown:
			return nil, fmt.Errorf("environment %s has a value only a shell can produce", entry.Name)
		case entry.Collision:
			return nil, fmt.Errorf("environment %s is projected by more than one variable", entry.Name)
		}
		result = append(result, [2]string{entry.Name, entry.Value})
	}
	return result, nil
}
