// Package vars evaluates Azure Pipelines variable bindings after template expansion.
// It is deliberately pure: callers provide every non-YAML input and receive an
// ordered binding graph plus diagnostics.
package vars

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// Phase is the point at which a variable reference is consumed.
type Phase uint8

const (
	// PhaseCompile resolves template expressions before runtime inputs exist.
	PhaseCompile Phase = iota + 1
	// PhaseRuntime resolves runtime expressions and dependency outputs.
	PhaseRuntime
	// PhaseMacro resolves task and script macro references.
	PhaseMacro
)

// SourceKind identifies where a candidate originated.
type SourceKind string

const (
	// SourceSystem is a predefined/system variable.
	SourceSystem SourceKind = "system"
	// SourceUI is a pipeline-settings variable.
	SourceUI SourceKind = "ui"
	// SourceQueue is a queue-time assignment.
	SourceQueue SourceKind = "queue"
	// SourceYAML is a direct YAML declaration.
	SourceYAML SourceKind = "yaml"
	// SourceGroup is a library variable-group member.
	SourceGroup SourceKind = "group"
	// SourceLogging is a task.setvariable event.
	SourceLogging SourceKind = "logging"
)

// Scope identifies one consumer. Empty stage/job values denote implicit scopes.
// Leg names a matrix leg or a deployment lifecycle hook variant (for example
// "debugJob" or "deploy_10"); it is empty for ordinary jobs.
type Scope struct {
	Stage string
	Job   string
	Step  int
	Level string
	Leg   string
}

// SecretRef is an opaque provider reference. It never contains secret material.
type SecretRef struct {
	Provider string
	ID       string
}

// Candidate is one definition retained in the binding graph.
type Candidate struct {
	Name                string
	ComparisonKey       string
	Source              SourceKind
	SourceID            string
	DeclarationIndex    int
	Phase               Phase
	Scope               Scope
	Value               string
	SecretRef           *SecretRef
	Secret              bool
	Readonly            bool
	SettableAtQueueTime bool
	CompileVisible      bool
	Available           bool
	Origin              diag.Source
	Shadowed            bool
	// Unknown marks a value only a running shell can produce (a dynamic logging
	// command value, or one derived from a masked value); Value is then empty.
	Unknown bool
	// Unordered marks a group member whose declaration order is not proven.
	Unordered bool
	// Contested is set when this candidate collides with an earlier candidate of
	// the same scope whose relative order cannot be proven (TRL-VAR-0004).
	Contested bool
}

// Binding retains all same-name candidates in precedence order, low to high.
type Binding struct {
	Name       string
	Candidates []Candidate
	Effective  int
	// Ambiguous mirrors the effective candidate's Contested flag: a later
	// unambiguous candidate (for example from a more local scope) clears it.
	Ambiguous bool
	// top and topCompile hold 1 + the index of the highest Available candidate
	// (and the highest Available, CompileVisible one); zero means none. They keep
	// effective lookups O(1) however many unavailable candidates precede.
	top, topCompile int
}

// note records the candidate at index i in the lookup caches.
func (b *Binding) note(i int) {
	if b.Candidates[i].Available {
		b.top = i + 1
		if b.Candidates[i].CompileVisible {
			b.topCompile = i + 1
		}
	}
}

// Table is the ordered binding table for one consumer scope.
type Table struct {
	Scope        Scope
	Bindings     []Binding
	bindingIndex map[string]int //trasloco:unordered-ok lookup-only acceleration
	// sharedIndex marks bindingIndex as still owned by the table this one was
	// cloned from. Bindings only ever grow at the end, so every position the
	// clone copied keeps its key: a lookup validates the position before
	// trusting it, and the first mutation copies the shared map before writing
	// (copy-on-write).
	sharedIndex bool
	// Env holds the step-level env: mapping, with values kept as written.
	Env []EnvMapping
	// MissingGroups lists imported groups the context did not supply; names
	// those groups define resolve to lower-precedence values, or to nothing.
	MissingGroups []string
}

// evaluationWork supports deterministic scaling tests for evaluator-owned
// scans and binding updates.
type evaluationWork struct {
	lineBytes int
	// scanBytes counts bytes examined by per-marker substring searches.
	scanBytes int
	// bindingScans counts bindings visited by linear scans of a table.
	bindingScans int
	// candidateSteps counts candidates touched by effective lookups and by
	// shadowing updates.
	candidateSteps int
}

// scan records n bytes examined by a substring search; a nil counter is a no-op.
func (w *evaluationWork) scan(n int) {
	if w != nil {
		w.scanBytes += n
	}
}

func (w *evaluationWork) total() int {
	return w.lineBytes + w.scanBytes + w.bindingScans + w.candidateSteps
}

// EnvMapping is one entry of a step's env: mapping. The key is never
// macro-expanded; the value is expanded when the environment is projected.
type EnvMapping struct{ Name, Value string }

// Output records an isOutput logging mutation without widening its visibility.
type Output struct {
	Stage, Job, StepName, Name string
	// Deployment is true for deployment jobs, whose output keys carry a job prefix.
	Deployment bool
	// Leg is the matrix leg or deployment hook variant that produced the output.
	Leg       string
	Candidate Candidate
	// KeyPrefix is the documented deployment qualifier of the output key
	// (Deploy_<resource> for runOnce on a resource, <hook>_<resource> for
	// rolling); it replaces the job and leg prefixes when set.
	KeyPrefix string
}

// Key is the dependency-output key within a job: step.name, or
// job.step.name for a deployment job.
func (o Output) Key() string {
	if o.KeyPrefix != "" {
		return o.KeyPrefix + "." + o.StepName + "." + o.Name
	}
	if o.Leg != "" {
		return o.Leg + "." + o.StepName + "." + o.Name
	}
	if o.Deployment {
		return o.Job + "." + o.StepName + "." + o.Name
	}
	return o.StepName + "." + o.Name
}

// Node is one stage or job with its dependency edges taken from the AST.
type Node struct {
	// Level is "stage" or "job".
	Level      string
	Stage, Job string
	// DependsOn lists the producer names. For a stage without dependsOn it holds
	// the immediately preceding stage (Implicit is then true).
	DependsOn []string
	// Explicit is true when dependsOn was written, including an empty list.
	Explicit bool
	Implicit bool
}

// Graph is the deterministic result of evaluation.
type Graph struct {
	Tables  []Table
	Outputs []Output
	Nodes   []Node
	// secretsWithheld is true for a fork build whose pipeline secrets are not
	// available: no provider-backed secret may be resolved from any source.
	secretsWithheld bool
	// agentOS is the Context's agent operating system, folded to lower case.
	agentOS string
}

// PlainVariable supplies a non-secret external variable.
type PlainVariable struct {
	Name, Value, ID string
	Settable        bool
	Readonly        bool
	CompileVisible  bool
	Origin          diag.Source
}

// SecretVariable supplies only a reference to secret material.
type SecretVariable struct {
	Name, ID, Provider string
	Settable           bool
	Readonly           bool
	Origin             diag.Source
}

// VariableGroup is provider material in declaration order.
type VariableGroup struct {
	Name        string
	Authorized  bool
	OrderProven bool
	Plain       []PlainVariable
	Secrets     []SecretVariable
}

// QueueAssignment is a queue-time assignment. A name that is a UI or group
// secret, or an assignment flagged Secret, never keeps its value in the graph.
type QueueAssignment struct {
	Name, Value string
	Secret      bool
}

// Context contains all inputs that do not live in the resolved YAML AST.
type Context struct {
	UIPlain                 []PlainVariable
	UISecrets               []SecretVariable
	Groups                  []VariableGroup
	Queue                   []QueueAssignment
	LimitQueueTimeVariables bool
	Predefined              []PlainVariable
	// ForkPullRequest marks a build of a pull request from a fork.
	ForkPullRequest bool
	// SecretsAvailableToForks is the effective "make secrets available to builds
	// of forks" policy. Azure withholds pipeline secrets from fork builds by
	// default, so the zero value withholds them: UI, variable-group, queue-time
	// and provider-backed output secrets are all dropped while ForkPullRequest is
	// set and this is false
	// (learn.microsoft.com/en-us/azure/devops/pipelines/repos/github#contributions-from-forks).
	SecretsAvailableToForks bool
	// AgentOS is the operating system of the agent that runs the script steps:
	// AgentOSWindows, AgentOSLinux or AgentOSMacOS. The zero value is unknown,
	// and a script whose shell depends on it is then recorded as unknown.
	AgentOS string
}

// Agent operating systems a Context may name.
const (
	AgentOSWindows = "windows"
	AgentOSLinux   = "linux"
	AgentOSMacOS   = "macos"
)

// withholdsSecrets reports whether the context removes every provider secret.
func (c Context) withholdsSecrets() bool { return c.ForkPullRequest && !c.SecretsAvailableToForks }

// fold is the one case-fold used for every name comparison. It mirrors the
// expression layer's ordinal fold so graph and expression lookups agree.
func fold(name string) string { return strings.ToUpper(name) }

func key(name string) string { return fold(name) }

func sameName(a, b string) bool { return fold(a) == fold(b) }

func cloneCandidate(in Candidate) Candidate {
	out := in
	if in.SecretRef != nil {
		ref := *in.SecretRef
		out.SecretRef = &ref
	}
	return out
}

// cloneTable deep-copies a table's bindings and shares its lookup index
// read-only. Positions are append-only, so the prefix the clone copied keeps the
// same key -> position mapping; a lookup validates the position and falls back to
// a scan on a mismatch, which is what stops a position the parent appended after
// the clone was taken from naming a binding the clone does not have. Sharing
// keeps the per-step snapshots the evaluator stores from each paying for a
// second index, and the first mutation of a table whose index is shared copies
// it first (copy-on-write), so neither side observes the other's additions.
func cloneTable(in Table) Table {
	out := Table{Scope: in.Scope, Bindings: make([]Binding, len(in.Bindings)), bindingIndex: in.bindingIndex, sharedIndex: in.bindingIndex != nil, Env: append([]EnvMapping(nil), in.Env...), MissingGroups: append([]string(nil), in.MissingGroups...)}
	for i, binding := range in.Bindings {
		out.Bindings[i] = Binding{Name: binding.Name, Effective: binding.Effective, Ambiguous: binding.Ambiguous, Candidates: make([]Candidate, len(binding.Candidates))}
		for j, candidate := range binding.Candidates {
			out.Bindings[i].Candidates[j] = cloneCandidate(candidate)
			out.Bindings[i].note(j)
		}
	}
	return out
}
