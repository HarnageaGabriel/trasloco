// This file tests the naming package beyond the spec table: value semantics of
// the per-pipeline state, error shapes and the pipeline inventory.
package naming

import (
	"errors"
	"slices"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
)

// TestResolverScopeIsolation checks collision handling step 11: collisions are
// detected within one scope key, never across scopes.
func TestResolverScopeIsolation(t *testing.T) {
	entries := []Entry{
		{Kind: KindSecret, Platform: "ado", StableID: "1", SourceName: "db.password", ScopeKey: "github:repo:1"},
		{Kind: KindSecret, Platform: "ado", StableID: "2", SourceName: "db-password", ScopeKey: "github:repo:2"},
	}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	for _, entry := range entries {
		decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
		if !ok {
			t.Fatalf("no decision for %s in %s", entry.StableID, entry.ScopeKey)
		}
		if decision.TargetName != "DB_PASSWORD" {
			t.Errorf("%s in %s = %q, want DB_PASSWORD", entry.StableID, entry.ScopeKey, decision.TargetName)
		}
		if slices.Contains(decision.DiagnosticCodes, "TRL-NAME-0002") {
			t.Errorf("%s in %s reports a collision although the scopes differ", entry.StableID, entry.ScopeKey)
		}
	}
}

// TestResolverIsAValue checks that resolution neither mutates its inventory nor
// leaks state between instances: CONVENTIONS.md forbids package-level state.
func TestResolverIsAValue(t *testing.T) {
	entries := []Entry{
		{Kind: KindSecret, Platform: "ado", StableID: "a", SourceName: "db.password", ScopeKey: "github:repo:1"},
		{Kind: KindSecret, Platform: "ado", StableID: "b", SourceName: "db-password", ScopeKey: "github:repo:1"},
	}
	snapshot := append([]Entry(nil), entries...)
	first, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if diff := cmp.Diff(snapshot, entries); diff != "" {
		t.Errorf("NewResolver changed its input (-before +after):\n%s", diff)
	}
	second, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	decision, ok := first.Lookup(KindSecret, "github:repo:1", "b")
	if !ok {
		t.Fatal("no decision for the disambiguated source")
	}
	again, ok := second.Lookup(KindSecret, "github:repo:1", "b")
	if !ok {
		t.Fatal("no decision for the disambiguated source in the second resolver")
	}
	if decision.TargetName != again.TargetName {
		t.Errorf("two resolvers disagree: %q then %q", decision.TargetName, again.TargetName)
	}
	// Lookup hands out a copy: mutating it cannot corrupt the resolver.
	decision.DiagnosticCodes = append(decision.DiagnosticCodes, "TRL-NAME-9999")
	third, ok := first.Lookup(KindSecret, "github:repo:1", "b")
	if !ok {
		t.Fatal("no decision after the returned copy was mutated")
	}
	if slices.Contains(third.DiagnosticCodes, "TRL-NAME-9999") {
		t.Error("Lookup returned the resolver's own slice")
	}
}

// TestNormalizeErrorShapes checks the two failure modes of normalization: a
// source error for invalid UTF-8 (step 1) and the registered empty-name stop
// (step 9).
func TestNormalizeErrorShapes(t *testing.T) {
	if _, err := NormalizeSecretName("1", "\xff\xfe", 0); err == nil {
		t.Error("invalid UTF-8 was accepted and silently replaced")
	}
	for _, testCase := range []struct {
		kind Kind
		name string
	}{
		{KindSecret, "   "},
		{KindJobID, "\t"},
		{KindStepID, "\u00a0"},
		{KindWorkflowFile, "--"},
	} {
		_, err := normalizeFor(testCase.kind, "1", testCase.name, 0)
		var finding *diag.Error
		if !errors.As(err, &finding) {
			t.Errorf("%s %q: error = %v, want a registered TRL-NAME-0005 finding", testCase.kind, testCase.name, err)
			continue
		}
		if finding.Finding.Code != "TRL-NAME-0005" {
			t.Errorf("%s %q: code = %s, want TRL-NAME-0005", testCase.kind, testCase.name, finding.Finding.Code)
		}
	}
}

// TestNormalizeLeavesValidNamesAlone checks the table's rule that names already
// valid still pass collision and reserved-name checks unchanged.
func TestNormalizeLeavesValidNamesAlone(t *testing.T) {
	for _, testCase := range []struct {
		kind Kind
		name string
	}{
		{KindSecret, "DB_PASSWORD"},
		{KindJobID, "_deploy"},
		{KindStepID, "build_2"},
		{KindWorkflowFile, "build.yml"},
	} {
		decision, err := normalizeFor(testCase.kind, "1", testCase.name, 64)
		if err != nil {
			t.Errorf("%s %q: %v", testCase.kind, testCase.name, err)
			continue
		}
		if decision.TargetName != testCase.name {
			t.Errorf("%s %q = %q, want it unchanged", testCase.kind, testCase.name, decision.TargetName)
		}
		if len(decision.DiagnosticCodes) != 0 {
			t.Errorf("%s %q codes = %v, want none", testCase.kind, testCase.name, decision.DiagnosticCodes)
		}
	}
}

// TestNamerReadsPipelineInventory checks the per-pipeline inventory of Namer
// and that an unknown name is an error instead of an invention.
func TestNamerReadsPipelineInventory(t *testing.T) {
	secret := ir.SecretRef{Name: "db.password", Scope: ir.ScopeRepository, ScopeID: "1842"}
	pipeline := &ir.Pipeline{
		ID:   "ci",
		Name: "Build and Test",
		Variables: []ir.Variable{{
			Name:      "cache-token",
			Scope:     ir.ScopeEnvironment,
			ScopeID:   "prod",
			Secret:    true,
			SecretRef: &ir.SecretRef{Name: "cache-token", Scope: ir.ScopeEnvironment, ScopeID: "prod"},
		}},
		Jobs: []ir.Job{{
			ID:      "Build",
			Steps:   []ir.Step{{ID: "step-1"}},
			Secrets: []ir.SecretRef{secret},
		}},
	}
	namer, err := New(pipeline, DefaultPolicy())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if name, err := namer.WorkflowFile(pipeline.Name); err != nil || name != "build-and-test.yml" {
		t.Errorf("WorkflowFile = %q, %v; want build-and-test.yml", name, err)
	}
	if name, err := namer.JobID("", "Build"); err != nil || name != "Build" {
		t.Errorf("JobID = %q, %v; want Build", name, err)
	}
	if name, err := namer.StepID("Build", "step-1"); err != nil || name != "step-1" {
		t.Errorf("StepID = %q, %v; want step-1", name, err)
	}
	if name, err := namer.SecretName(secret); err != nil || name != "DB_PASSWORD" {
		t.Errorf("SecretName = %q, %v; want DB_PASSWORD", name, err)
	}
	if name, err := namer.SecretName(ir.SecretRef{Name: "cache-token", Scope: ir.ScopeEnvironment, ScopeID: "prod"}); err != nil || name != "CACHE_TOKEN" {
		t.Errorf("SecretName of a secret variable = %q, %v; want CACHE_TOKEN", name, err)
	}
	if _, err := namer.WorkflowFile("Other"); err == nil {
		t.Error("WorkflowFile invented a name outside the inventory")
	}
	if _, err := namer.JobID("Build", "Missing"); err == nil {
		t.Error("JobID invented a name outside the inventory")
	}
	if _, err := namer.StepID("Build", "step-9"); err == nil {
		t.Error("StepID invented a name outside the inventory")
	}
	if _, err := namer.SecretName(ir.SecretRef{Name: "missing"}); err == nil {
		t.Error("SecretName invented a name outside the inventory")
	}
}

// TestNewRejectsMissingPipeline keeps the constructor honest about its input.
func TestNewRejectsMissingPipeline(t *testing.T) {
	if _, err := New(nil, DefaultPolicy()); err == nil {
		t.Error("New(nil) accepted a missing pipeline")
	}
}
