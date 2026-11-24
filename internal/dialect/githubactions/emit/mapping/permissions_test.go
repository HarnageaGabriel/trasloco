package mapping

import (
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// The deny-all top-level block is emitter scaffolding (INV-02), so the
// permissions mapper claims no ID and contributes no fragment: every node
// keeps its emitter disposition and conservation is unaffected. A present
// job-level grant stays unclaimed too; that mapping is a later tranche.
func TestPermissionsMapperClaimsNothing(t *testing.T) {
	pipeline := newPipeline("ci").withTrigger("t1", ir.TriggerPush).withJob("build", "s1").build()
	pipeline.Jobs[0].Permissions.Set("pull-requests", "write")

	out, err := Run(pipeline, permissionsMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Outcomes) != 0 {
		t.Errorf("permissions mapper claimed IDs, want none: %+v", out.Outcomes)
	}
	if len(out.Fragments) != 1 {
		t.Fatalf("fragments = %d, want 1", len(out.Fragments))
	}
	if diff := cmp.Diff(Fragment{}, out.Fragments[0]); diff != "" {
		t.Errorf("permissions fragment mismatch (-want +got):\n%s", diff)
	}
}

func TestPermissionsMapperConcern(t *testing.T) {
	if got := (permissionsMapper{}).Concern(); got != Permissions {
		t.Errorf("Concern = %d, want Permissions (%d)", got, Permissions)
	}
}
