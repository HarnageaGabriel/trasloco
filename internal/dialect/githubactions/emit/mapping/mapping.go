// Package mapping holds the per-concern mapping rules of the GitHub Actions
// emitter (docs/spec/emit-gha.md section 5). Each concern (triggers, runners,
// permissions, ...) is one Mapper in its own file of this package.
//
// # Adding a mapper
//
// A mapper file implements Mapper and registers itself from an init function:
//
//	func init() { Register(triggerMapper{}) }
//
// The registry is a fixed array indexed by Concern, so the run order is the
// declaration order of the Concern constants below and never depends on file
// names, init order or map iteration (INV-01). Adding a mapper therefore
// touches only the new file. Register panics on a nil mapper or a second
// mapper for one concern: both are programming errors found by any test run.
// Tests never touch the registry; they inject mapper lists into Run.
//
// # Contract
//
// A mapper claims IR nodes and features by their qualified ID (QualifyID) and
// returns, per claimed ID, exactly one disposition plus its diagnostics, and
// a typed model fragment. The emitter keeps its not-yet-mapped outcome for
// every ID no mapper claims, then re-checks INV-12 over the merged result, so
// a mapper cannot drop, duplicate or invent an ID. A mapper sees the
// read-only pipeline and the IDs earlier mappers claimed; Run rejects an ID
// claimed twice, an invalid ID, disposition kind or fidelity, and a plain
// job:X claim from any concern but Runners. Run sorts the outcomes by ID byte
// order (INV-01).
//
// The package is pure and sits below emit: it imports the model and the IR,
// never emit.
package mapping

import (
	"fmt"
	"sort"
	"strings"

	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// Concern identifies one mapping concern. The constants are the fixed run
// order of the mappers: earlier concerns run first.
type Concern int

// Concerns in run order. numConcerns must stay last.
const (
	Triggers Concern = iota
	Runners
	Permissions
	Checkout
	Variables
	Secrets
	Steps
	StepOptions
	Conditions
	numConcerns
)

// Disposition is the outcome of one claimed node. Kind is the string value of
// emit.DispositionKind (emitted, default, placeholder, dropped, carried).
type Disposition struct {
	ID       string
	Kind     string
	Fidelity diag.Fidelity
}

// Outcome is everything a mapper decided for one claimed ID.
type Outcome struct {
	Disposition Disposition
	// Diagnostics are the findings of this node, in emission order.
	Diagnostics []diag.Diagnostic
}

// Entry is one key of a dynamic mapping field (on, env, permissions, with).
// Every entry is attributed to its own Node and recorded as its own output
// element. Entries merge key-wise in first-occurrence order; a key written by
// two different nodes is an error, a rewrite by the same node replaces the
// value.
type Entry struct {
	// Node is the qualified ID of the IR node the entry is attributed to.
	Node string
	Key  string
	// Value is the entry value, already built as an ordered yaml node.
	Value *goyaml.Node
}

// Fragment is the typed model contribution of one mapper. The emitter merges
// all fragments in concern order: a scalar key rewritten by the same Node
// replaces the earlier value, a scalar key written by a different Node is an
// error, and map fields merge key-wise per Entry. Every rendered field is
// recorded under the Node that wrote it, so an emitted disposition has its
// output element. Zero fields mean "not contributed".
//
// A job with at least one job or step fragment is rendered from the model
// instead of as the failing placeholder: its steps are the IR steps in order,
// each from its step fragments or, without one, the step placeholder.
type Fragment struct {
	Workflow []WorkflowFragment
	Jobs     []JobFragment
	Steps    []StepFragment
}

// WorkflowFragment sets workflow-level mapping keys. The emitter owns name
// and jobs. Permissions must stay empty: the top-level block is always the
// deny-all mapping of INV-02, and any entry is rejected.
type WorkflowFragment struct {
	On          []Entry
	Permissions []Entry
	Env         []Entry
}

// JobFragment sets the keys of one IR job. Only the scalar fields If,
// RunsOn, TimeoutMinutes and ContinueOnError of Job are read; the emitter
// owns name, needs and steps, and the map fields come from Permissions and
// Env. Unset permissions stay deny-all (INV-02); read-all and write-all
// entries are rejected.
type JobFragment struct {
	// Node is the qualified ID of the IR node the scalar fields are
	// attributed to.
	Node string
	// JobID is the IR job ID.
	JobID string
	Job   model.Job
	// Permissions and Env hold the job-level mapping entries.
	Permissions []Entry
	Env         []Entry
}

// StepFragmentKind is the rendering variant of a step fragment.
type StepFragmentKind int

const (
	// StepCode renders the Step fields (the zero value).
	StepCode StepFragmentKind = iota
	// StepPlaceholder replaces the step by the failing placeholder of
	// emit-gha.md 2.5 with Code as its diagnostic code; other fields are
	// not rendered.
	StepPlaceholder
	// StepOmit renders no step at all; the claim's disposition must not be
	// emitted.
	StepOmit
)

// StepFragment sets the keys of one IR step. Only the scalar fields of Step
// are read; the map fields come from With and Env.
type StepFragment struct {
	// Node is the qualified ID of the IR node the scalar fields are
	// attributed to.
	Node string
	// JobID and StepID are the IR IDs of the owning job and the step.
	JobID, StepID string
	// Kind selects the rendering variant; Code names the diagnostic code
	// of a StepPlaceholder.
	Kind StepFragmentKind
	Code string
	Step model.Step
	// UsesComment is the trailing version comment of uses (emit-gha.md
	// 4.1 rule 6), rendered only when Uses is set.
	UsesComment string
	// With and Env hold the step-level mapping entries.
	With []Entry
	Env  []Entry
}

// Result is the complete output of one mapper.
type Result struct {
	// Outcomes are the claimed IDs with their dispositions.
	Outcomes []Outcome
	Fragment Fragment
}

// Mapper converts one concern of the IR.
type Mapper interface {
	// Concern is the concern the mapper owns; it fixes its run order.
	Concern() Concern
	// Map claims the nodes and features it converts. claimed reports an ID an
	// earlier mapper already claimed. It must not mutate the pipeline.
	Map(pipeline *ir.Pipeline, claimed func(id string) bool) Result
}

// Output is the merged result of all mappers that ran.
type Output struct {
	// Outcomes are the claimed outcomes, sorted by ID byte order (INV-01).
	Outcomes []Outcome
	// Fragments keep the run order of the mappers that returned them.
	Fragments []Fragment
}

// registry holds the registered mappers, indexed by Concern. Only init
// functions write it, through Register.
var registry [numConcerns]Mapper

// Register adds a mapper to the registry; call it from an init function.
func Register(m Mapper) {
	if m == nil {
		panic("mapping: nil mapper")
	}
	concern := m.Concern()
	if concern < 0 || concern >= numConcerns {
		panic(fmt.Sprintf("mapping: unknown concern %d", int(concern)))
	}
	if registry[concern] != nil {
		panic(fmt.Sprintf("mapping: concern %d already has a mapper", int(concern)))
	}
	registry[concern] = m
}

// Dispatch runs the registered mappers in concern order.
func Dispatch(pipeline *ir.Pipeline) (Output, error) {
	return Run(pipeline, registry[:]...)
}

// Run runs mappers in the order given, skipping nil entries. It validates
// every claimed ID, disposition kind and fidelity, rejects an ID claimed
// twice (which would break the INV-12 one-disposition rule) and a plain
// job:X claim from any concern but Runners, and sorts the outcomes by ID
// byte order.
func Run(pipeline *ir.Pipeline, mappers ...Mapper) (Output, error) {
	var out Output
	claimed := make(map[string]bool)
	isClaimed := func(id string) bool { return claimed[id] }
	for _, m := range mappers {
		if m == nil {
			continue
		}
		result := m.Map(pipeline, isClaimed)
		for _, outcome := range result.Outcomes {
			disposition := outcome.Disposition
			if err := validateID(disposition.ID); err != nil {
				return Output{}, err
			}
			if claimed[disposition.ID] {
				return Output{}, fmt.Errorf("mapping: ID %q claimed more than once", disposition.ID)
			}
			if kind, _, _ := strings.Cut(disposition.ID, ":"); kind == "job" &&
				!strings.Contains(disposition.ID, "#") && m.Concern() != Runners {
				return Output{}, fmt.Errorf("mapping: ID %q must be claimed by the Runners concern", disposition.ID)
			}
			if !validDispositionKind(disposition.Kind) {
				return Output{}, fmt.Errorf("mapping: ID %q has unknown disposition kind %q", disposition.ID, disposition.Kind)
			}
			if !validFidelity(disposition.Fidelity) {
				return Output{}, fmt.Errorf("mapping: ID %q has unknown fidelity %q", disposition.ID, disposition.Fidelity)
			}
			claimed[disposition.ID] = true
			out.Outcomes = append(out.Outcomes, outcome)
		}
		out.Fragments = append(out.Fragments, result.Fragment)
	}
	sort.SliceStable(out.Outcomes, func(i, j int) bool {
		return out.Outcomes[i].Disposition.ID < out.Outcomes[j].Disposition.ID
	})
	return out, nil
}

// validateID checks the qualified ID shape of emit-gha.md 2.3: a known node
// kind, a colon and a non-empty node part, optionally followed by one
// non-empty feature suffix.
func validateID(id string) error {
	kind, rest, ok := strings.Cut(id, ":")
	if !ok || rest == "" {
		return fmt.Errorf("mapping: malformed ID %q", id)
	}
	switch kind {
	case "pipeline", "input", "trigger", "variable", "stage", "job", "step":
	default:
		return fmt.Errorf("mapping: ID %q has unknown kind %q", id, kind)
	}
	if node, feature, hasFeature := strings.Cut(rest, "#"); hasFeature {
		if node == "" || feature == "" || strings.Contains(feature, "#") {
			return fmt.Errorf("mapping: malformed feature ID %q", id)
		}
	}
	return nil
}

// validDispositionKind reports whether kind is one of the emit-gha.md 2.3
// disposition values (the string values of emit.DispositionKind).
func validDispositionKind(kind string) bool {
	switch kind {
	case "emitted", "default", "placeholder", "dropped", "carried":
		return true
	}
	return false
}

// validFidelity reports whether fidelity is a registered grade.
func validFidelity(fidelity diag.Fidelity) bool {
	switch fidelity {
	case diag.FidelityExact, diag.FidelityApproximated, diag.FidelityNeedsManual, diag.FidelityUnsupported:
		return true
	}
	return false
}

// QualifyID builds the qualified stable ID of an IR node (emit-gha.md section
// 2.3): the kind, a colon and the "/"-joined parts, with "%" and "/" escaped.
func QualifyID(kind string, parts ...string) string {
	escaped := make([]string, len(parts))
	for index, part := range parts {
		escaped[index] = strings.ReplaceAll(strings.ReplaceAll(part, "%", "%25"), "/", "%2F")
	}
	return kind + ":" + strings.Join(escaped, "/")
}
