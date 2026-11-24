package mapping

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// fakeMapper claims a fixed list of IDs as dropped.
type fakeMapper struct {
	concern  Concern
	ids      []string
	fragment Fragment
}

func (f fakeMapper) Concern() Concern { return f.concern }

func (f fakeMapper) Map(_ *ir.Pipeline, claimed func(string) bool) Result {
	var result Result
	for _, id := range f.ids {
		if claimed(id) {
			continue
		}
		result.Outcomes = append(result.Outcomes, Outcome{
			Disposition: Disposition{ID: id, Kind: "dropped", Fidelity: diag.FidelityUnsupported},
		})
	}
	result.Fragment = f.fragment
	return result
}

// outcomeMapper returns one fixed outcome, ignoring the claimed callback.
type outcomeMapper struct {
	concern Concern
	outcome Outcome
}

func (m outcomeMapper) Concern() Concern { return m.concern }

func (m outcomeMapper) Map(*ir.Pipeline, func(string) bool) Result {
	return Result{Outcomes: []Outcome{m.outcome}}
}

func TestRunSortsOutcomesByID(t *testing.T) {
	pipeline := newPipeline("ci").withTrigger("t1", ir.TriggerPush).withJob("build", "s1").build()
	first := fakeMapper{concern: Triggers, ids: []string{QualifyID("trigger", "t1")}}
	second := fakeMapper{concern: Runners, ids: []string{QualifyID("job", "build")}}

	out, err := Run(pipeline, first, nil, second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := make([]string, 0, len(out.Outcomes))
	for _, outcome := range out.Outcomes {
		got = append(got, outcome.Disposition.ID)
	}
	want := []string{"job:build", "trigger:t1"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("outcome order mismatch (-want +got):\n%s", diff)
	}
}

func TestRunKeepsFragmentOrder(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	first := fakeMapper{concern: Triggers, fragment: Fragment{Workflow: []WorkflowFragment{{
		Env: []Entry{{Node: "pipeline:ci", Key: "A", Value: model.String("1")}},
	}}}}
	second := fakeMapper{concern: Variables, fragment: Fragment{Workflow: []WorkflowFragment{{
		Env: []Entry{{Node: "pipeline:ci", Key: "B", Value: model.String("2")}},
	}}}}
	out, err := Run(pipeline, first, second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Fragments) != 2 || out.Fragments[0].Workflow[0].Env[0].Key != "A" || out.Fragments[1].Workflow[0].Env[0].Key != "B" {
		t.Errorf("fragments lost the mapper run order: %+v", out.Fragments)
	}
}

func TestRunRejectsDoubleClaim(t *testing.T) {
	// A mapper that ignores the claimed callback claims the same ID twice.
	id := QualifyID("job", "build")
	pipeline := newPipeline("ci").withJob("build").build()
	_, err := Run(pipeline, fakeMapper{concern: Runners, ids: []string{id}}, outcomeMapper{
		concern: Steps,
		outcome: Outcome{Disposition: Disposition{ID: id, Kind: "dropped", Fidelity: diag.FidelityUnsupported}},
	})
	if err == nil || !strings.Contains(err.Error(), "claimed more than once") {
		t.Fatalf("Run error = %v, want a double-claim error", err)
	}
}

func TestRunRejectsJobClaimOutsideRunners(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	_, err := Run(pipeline, fakeMapper{concern: Steps, ids: []string{QualifyID("job", "build")}})
	if err == nil || !strings.Contains(err.Error(), "Runners") {
		t.Fatalf("Run error = %v, want a Runners-ownership error", err)
	}
}

func TestRunRejectsInvalidIDs(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	for _, id := range []string{"build", "ghost:x", "job:", "job:#container", "job:x#", "job:x#a#b"} {
		_, err := Run(pipeline, outcomeMapper{concern: Runners, outcome: Outcome{
			Disposition: Disposition{ID: id, Kind: "dropped", Fidelity: diag.FidelityUnsupported},
		}})
		if err == nil {
			t.Errorf("Run accepted invalid ID %q", id)
		}
	}
}

func TestRunRejectsInvalidKindAndFidelity(t *testing.T) {
	pipeline := newPipeline("ci").withJob("build").build()
	id := QualifyID("job", "build")
	cases := []Disposition{
		{ID: id, Kind: "bogus", Fidelity: diag.FidelityUnsupported},
		{ID: id, Kind: "dropped", Fidelity: "bogus"},
		{ID: id, Kind: "dropped", Fidelity: ""},
	}
	for _, disposition := range cases {
		_, err := Run(pipeline, outcomeMapper{concern: Runners, outcome: Outcome{Disposition: disposition}})
		if err == nil {
			t.Errorf("Run accepted %+v", disposition)
		}
	}
}

func TestRunWithoutMappersClaimsNothing(t *testing.T) {
	out, err := Run(newPipeline("ci").withJob("build", "s1").build())
	if err != nil || len(out.Outcomes) != 0 {
		t.Errorf("Run = %v, %v; want no outcomes and no error", out, err)
	}
}
