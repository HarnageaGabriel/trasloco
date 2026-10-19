// This file tests the naming package beyond the spec table: value semantics of
// the per-pipeline state, error shapes and the pipeline inventory.
package naming

import (
	"errors"
	"slices"
	"strings"
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
	decision.DiagnosticCodes = append(decision.DiagnosticCodes, "test-mutation")
	third, ok := first.Lookup(KindSecret, "github:repo:1", "b")
	if !ok {
		t.Fatal("no decision after the returned copy was mutated")
	}
	if slices.Contains(third.DiagnosticCodes, "test-mutation") {
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

// TestKindLabels pins the stable labels used by the truncation and collision
// digests (Ruling R11). Changing one invalidates every plan output.
func TestKindLabels(t *testing.T) {
	for _, testCase := range []struct {
		kind Kind
		want string
	}{
		{KindWorkflowFile, "workflow"},
		{KindJobID, "github-job"},
		{KindStepID, "github-step"},
		{KindSecret, "github-secret"},
	} {
		if string(testCase.kind) != testCase.want {
			t.Errorf("kind label = %q, want %q", testCase.kind, testCase.want)
		}
	}
}

func expressionSecret(name string) ir.Expr {
	return ir.Expr{Kind: ir.ExprRef, Ref: &ir.RefExpr{Context: ir.RefSecrets, Path: []string{name}}}
}

func expressionSecretPtr(name string) *ir.Expr {
	expr := expressionSecret(name)
	return &expr
}

func secretVariable(name string, scope ir.VariableScope, scopeID string) ir.Variable {
	return ir.Variable{
		Name: name, Scope: scope, ScopeID: scopeID, Secret: true,
		SecretRef: &ir.SecretRef{Name: name, Scope: scope, ScopeID: scopeID},
	}
}

// TestNamerEnumeratesEverySecretRef checks Ruling R4: every SecretRef location
// in the IR enters the inventory, including stage- and group-scoped variables,
// reusable-call secrets, container/service credentials and expressions.
func TestNamerEnumeratesEverySecretRef(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID: "ci", Name: "Build",
		Variables: []ir.Variable{
			secretVariable("pipeline.secret", ir.ScopePipeline, "ci"),
			secretVariable("group.token", ir.ScopeGroup, "grp"),
		},
		Stages: []ir.Stage{{
			ID:        "st1",
			Variables: []ir.Variable{secretVariable("stage.secret", ir.ScopeStage, "st1")},
		}},
		Jobs: []ir.Job{{
			ID:        "job1",
			Variables: []ir.Variable{secretVariable("job.var", ir.ScopeJob, "job1")},
			Secrets: []ir.SecretRef{
				{Name: "job.secret", Scope: ir.ScopeJob, ScopeID: "job1"},
				{Name: "shared.secret", Scope: ir.ScopeRepository, ScopeID: "repo"},
			},
			Uses: &ir.ReusableCall{Secrets: ir.OrderedMap[ir.SecretRef]{
				{Key: "TOKEN", Value: ir.SecretRef{Name: "uses.secret", Scope: ir.ScopeRepository, ScopeID: "repo"}},
			}},
			Container: &ir.Container{
				Env: ir.OrderedMap[ir.Expr]{{Key: "E", Value: expressionSecret("container.env")}},
				Credentials: &ir.ContainerAuthRef{
					Username: expressionSecretPtr("container.username"),
					Password: &ir.SecretRef{Name: "container.password", Scope: ir.ScopeRepository, ScopeID: "repo"},
				},
			},
			Services: []ir.Service{{
				ID: "svc",
				Container: ir.Container{
					Env: ir.OrderedMap[ir.Expr]{{Key: "E", Value: expressionSecret("service.env")}},
					Credentials: &ir.ContainerAuthRef{
						Username: expressionSecretPtr("service.username"),
						Password: &ir.SecretRef{Name: "service.password", Scope: ir.ScopeRepository, ScopeID: "repo"},
					},
				},
			}},
			Steps: []ir.Step{{
				ID:  "step1",
				Env: ir.OrderedMap[ir.Expr]{{Key: "E", Value: expressionSecret("step.env")}},
				Action: &ir.ActionStep{With: ir.OrderedMap[ir.Expr]{
					{Key: "W", Value: expressionSecret("step.with")},
					{Key: "S", Value: expressionSecret("shared.secret")},
				}},
			}},
		}},
	}
	namer, err := New(pipeline, DefaultPolicy())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, secret := range []ir.SecretRef{
		{Name: "pipeline.secret", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "group.token", Scope: ir.ScopeGroup, ScopeID: "grp"},
		{Name: "stage.secret", Scope: ir.ScopeStage, ScopeID: "st1"},
		{Name: "job.var", Scope: ir.ScopeJob, ScopeID: "job1"},
		{Name: "job.secret", Scope: ir.ScopeJob, ScopeID: "job1"},
		{Name: "uses.secret", Scope: ir.ScopeRepository, ScopeID: "repo"},
		{Name: "container.password", Scope: ir.ScopeRepository, ScopeID: "repo"},
		{Name: "service.password", Scope: ir.ScopeRepository, ScopeID: "repo"},
		{Name: "container.username", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "service.username", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "container.env", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "service.env", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "step.env", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "step.with", Scope: ir.ScopePipeline, ScopeID: "ci"},
		{Name: "shared.secret", Scope: ir.ScopeRepository, ScopeID: "repo"},
	} {
		if name, err := namer.SecretName(secret); err != nil || name == "" {
			t.Errorf("SecretName(%+v) = %q, %v; want an inventory decision", secret, name, err)
		}
	}
	// One declared secret referenced from two places keeps one name.
	declared, err := namer.SecretName(ir.SecretRef{Name: "shared.secret", Scope: ir.ScopeRepository, ScopeID: "repo"})
	if err != nil {
		t.Fatalf("SecretName(shared declaration): %v", err)
	}
	resolved := resolveSecretRef("shared.secret", declaredSecretRefs(pipeline), pipeline.ID, "")
	used, err := namer.SecretName(resolved)
	if err != nil {
		t.Fatalf("SecretName(shared use): %v", err)
	}
	if declared != used {
		t.Errorf("one secret referenced twice got %q and %q", declared, used)
	}
}

// TestRepositorySecretNamespace checks Ruling R2: different source scopes share
// one repository namespace, while the same source identity keeps one name.
func TestRepositorySecretNamespace(t *testing.T) {
	stage := ir.SecretRef{Name: "db.password", Scope: ir.ScopeStage, ScopeID: "st1"}
	job := ir.SecretRef{Name: "db-password", Scope: ir.ScopeJob, ScopeID: "job1"}
	same := stage
	entries := []Entry{secretEntry(stage), secretEntry(job), secretEntry(same)}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	stageDecision, ok := resolver.Lookup(KindSecret, entries[0].ScopeKey, entries[0].StableID)
	if !ok {
		t.Fatal("no decision for the stage secret")
	}
	jobDecision, ok := resolver.Lookup(KindSecret, entries[1].ScopeKey, entries[1].StableID)
	if !ok {
		t.Fatal("no decision for the job secret")
	}
	sameDecision, ok := resolver.Lookup(KindSecret, entries[2].ScopeKey, entries[2].StableID)
	if !ok {
		t.Fatal("no decision for the repeated stage secret")
	}
	if stageDecision.TargetName == jobDecision.TargetName {
		t.Errorf("two source identities shared the repository name %q", stageDecision.TargetName)
	}
	if stageDecision.TargetName != sameDecision.TargetName {
		t.Errorf("one source identity got %q and %q", stageDecision.TargetName, sameDecision.TargetName)
	}
	if !slices.Contains(jobDecision.DiagnosticCodes, "TRL-NAME-0002") && !slices.Contains(stageDecision.DiagnosticCodes, "TRL-NAME-0002") {
		t.Errorf("neither colliding secret reported TRL-NAME-0002")
	}
}

// TestSuffixedNameNeverEqualsLiteralName checks Ruling R3 directly on the
// resolved inventory: a literal candidate owns the first suffixed spelling, so
// the suffixed entry must grow its token.
func TestSuffixedNameNeverEqualsLiteralName(t *testing.T) {
	token := collisionToken(KindSecret, "b", 8)
	literal := "FOO" + collisionSeparator(KindSecret) + token
	candidates := []candidate{
		{entry: Entry{Kind: KindSecret, StableID: "a", ScopeKey: "github:repository"}, name: "FOO", compare: "foo", seq: 0},
		{entry: Entry{Kind: KindSecret, StableID: "b", ScopeKey: "github:repository"}, name: "FOO", compare: "foo", seq: 1},
		{entry: Entry{Kind: KindSecret, StableID: "c", ScopeKey: "github:repository"}, name: literal, compare: strings.ToLower(literal), seq: 2},
	}
	decisions, err := resolveInventory(candidates, DefaultPolicy(), collisionToken)
	if err != nil {
		t.Fatalf("resolveInventory: %v", err)
	}
	byID := make(map[string]string, len(decisions))
	for _, decision := range decisions {
		byID[decision.Entry.StableID] = decision.TargetName
	}
	wantSuffix := "FOO" + collisionSeparator(KindSecret) + collisionToken(KindSecret, "b", 10)
	if byID["b"] != wantSuffix {
		t.Errorf("suffixed entry = %q, want the extended token %q", byID["b"], wantSuffix)
	}
	if byID["c"] != literal {
		t.Errorf("literal entry = %q, want %q", byID["c"], literal)
	}
	if byID["b"] == byID["c"] {
		t.Errorf("suffixed entry equals the literal entry: %q", byID["b"])
	}
}

// TestSmallLimitsFailClosed checks Ruling R10.
func TestSmallLimitsFailClosed(t *testing.T) {
	_, err := NormalizeSecretName("1", "db.password", 3)
	var finding *diag.Error
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-NAME-0008" {
		t.Errorf("secret at limit 3 = %v, want TRL-NAME-0008", err)
	}
	_, err = NormalizeWorkflowFile("1", "a", 4)
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-NAME-0008" {
		t.Errorf("workflow at limit 4 = %v, want TRL-NAME-0008", err)
	}
	for limit := 5; limit <= 12; limit++ {
		for _, name := range []string{"a", "-a", "a-", "--", "db.password", "a/b"} {
			for _, kind := range fuzzKinds {
				decision, err := normalizeFor(kind, "1", name, limit)
				if err != nil {
					if errors.As(err, &finding) && finding.Finding.Code == "TRL-NAME-0003" {
						t.Errorf("%s %q at limit %d reported exhaustion instead of a limit diagnostic", kind, name, limit)
					}
					continue
				}
				if strings.HasPrefix(decision.TargetName, "-") {
					t.Errorf("%s %q at limit %d produced %q with a leading dash", kind, name, limit, decision.TargetName)
				}
				if len(decision.TargetName) > limit {
					t.Errorf("%s %q at limit %d produced the longer %q", kind, name, limit, decision.TargetName)
				}
			}
		}
	}
}

// TestNilNamerIsTotallySafe checks that a nil *Namer and a nil *Resolver never
// panic.
func TestNilNamerIsTotallySafe(t *testing.T) {
	var namer *Namer
	if _, err := namer.WorkflowFile("build"); err == nil {
		t.Error("nil Namer.WorkflowFile succeeded")
	}
	if _, err := namer.JobID("", "job"); err == nil {
		t.Error("nil Namer.JobID succeeded")
	}
	if _, err := namer.StepID("job", "step"); err == nil {
		t.Error("nil Namer.StepID succeeded")
	}
	if _, err := namer.SecretName(ir.SecretRef{Name: "secret"}); err == nil {
		t.Error("nil Namer.SecretName succeeded")
	}
	var resolver *Resolver
	if _, ok := resolver.Lookup(KindSecret, "scope", "id"); ok {
		t.Error("nil Resolver.Lookup reported a decision")
	}
}

// TestEmptyPipelineNameIsValid checks Ruling R10 and emit-gha.md 5.1: an empty
// pipeline name is valid and does not fail the whole inventory.
func TestEmptyPipelineNameIsValid(t *testing.T) {
	pipeline := &ir.Pipeline{
		ID:   "ci",
		Jobs: []ir.Job{{ID: "build", Steps: []ir.Step{{ID: "checkout"}}}},
	}
	namer, err := New(pipeline, DefaultPolicy())
	if err != nil {
		t.Fatalf("New with an empty pipeline name: %v", err)
	}
	if name, err := namer.JobID("", "build"); err != nil || name != "build" {
		t.Errorf("JobID = %q, %v; want build", name, err)
	}
}

// TestEncodingTokensAreNotSeparators checks Ruling R7: separators that came
// from an encoding token are preserved, and spellings that normalize equally
// still collide into distinct outputs instead of silently merging.
func TestEncodingTokensAreNotSeparators(t *testing.T) {
	const source = "a°b"
	encoded, err := NormalizeSecretName("1", source, 0)
	if err != nil {
		t.Fatalf("NormalizeSecretName(%q): %v", source, err)
	}
	if encoded.TargetName != "A_UC2_UB0B" {
		t.Errorf("encoding token = %q, want A_UC2_UB0B", encoded.TargetName)
	}
	names := []string{"a_東", "a東", "a 東"}
	entries := make([]Entry, len(names))
	for index, name := range names {
		entries[index] = Entry{Kind: KindSecret, Platform: "ado", StableID: string(rune('a' + index)), SourceName: name, ScopeKey: "github:repository"}
	}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	seen := make(map[string]bool, len(names))
	for _, entry := range entries {
		decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
		if !ok {
			t.Fatalf("no decision for %q", entry.SourceName)
		}
		if seen[decision.TargetName] {
			t.Errorf("spelling %q silently reused %q", entry.SourceName, decision.TargetName)
		}
		seen[decision.TargetName] = true
	}
}

// TestSuffixedSecretIsNeverReserved checks that a collision suffix cannot turn
// an allowed base into a reserved GITHUB_ name.
func TestSuffixedSecretIsNeverReserved(t *testing.T) {
	entries := []Entry{
		{Kind: KindSecret, Platform: "ado", StableID: "a", SourceName: "githuB", ScopeKey: "github:repository"},
		{Kind: KindSecret, Platform: "ado", StableID: "b", SourceName: "githuB", ScopeKey: "github:repository"},
	}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
		if !ok {
			t.Fatalf("no decision for %q", entry.StableID)
		}
		if strings.HasPrefix(strings.ToUpper(decision.TargetName), "GITHUB_") {
			t.Errorf("%s received the reserved name %q", entry.StableID, decision.TargetName)
		}
		if seen[decision.TargetName] {
			t.Errorf("two entries received %q", decision.TargetName)
		}
		seen[decision.TargetName] = true
	}
}

// TestRunnerPrefixIsNotASecretWarning pins Ruling R8: TRL-NAME-0007 is a
// variable warning, not a secret warning.
func TestRunnerPrefixIsNotASecretWarning(t *testing.T) {
	decision, err := NormalizeSecretName("1", "RUNNER_TOKEN", 0)
	if err != nil {
		t.Fatalf("NormalizeSecretName: %v", err)
	}
	if slices.Contains(decision.DiagnosticCodes, "TRL-NAME-0007") {
		t.Errorf("secret RUNNER_TOKEN reported TRL-NAME-0007")
	}
}
