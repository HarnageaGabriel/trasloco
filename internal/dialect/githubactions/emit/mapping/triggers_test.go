package mapping

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/normalize"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// emitSpecPath locates the specification from the package directory.
const emitSpecPath = "../../../../../docs/spec/emit-gha.md"

// triggerVector is one EMITV body of section 7.2: the Azure input and the
// expected GitHub output (fragment or full golden).
type triggerVector struct {
	id  string
	ado string
	gha string
}

func loadTriggerVectors(t *testing.T) []triggerVector {
	t.Helper()
	data, err := os.ReadFile(emitSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "### 7.2 Triggers and filters")
	end := strings.Index(text, "### 7.3 ")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("trigger vector section not found in emit-gha.md")
	}
	vectors := make([]triggerVector, 0, 19)
	for _, block := range strings.Split(text[start:end], "#### EMITV-")[1:] {
		id := "EMITV-" + block[:3]
		ado := fencedBlock(t, block, "yaml ado")
		gha := fencedBlock(t, block, "yaml gha fragment")
		if gha == "" {
			gha = fencedBlock(t, block, "yaml gha full")
		}
		if ado == "" || gha == "" {
			t.Fatalf("%s: missing ado or gha body", id)
		}
		vectors = append(vectors, triggerVector{id: id, ado: ado, gha: gha})
	}
	if len(vectors) != 19 {
		t.Fatalf("loaded %d trigger vectors, want 19", len(vectors))
	}
	return vectors
}

// fencedBlock returns the content of the first fenced block with the given
// info string, or "" when the body has none.
func fencedBlock(t *testing.T, body, info string) string {
	t.Helper()
	marker := "```" + info + "\n"
	start := strings.Index(body, marker)
	if start < 0 {
		return ""
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		t.Fatalf("unterminated %s block", info)
	}
	return rest[:end]
}

// TestTriggerVectors runs the EMITV-001..019 inputs through the reader and
// the trigger mapper and compares the on mapping against the normative
// bodies. Diagnostic codes are the emit-stage codes the trigger concern
// itself raises; row codes of other concerns (for example EMITV-001's
// TRL-CAT-0006) belong to their mappers.
func TestTriggerVectors(t *testing.T) {
	wantCodes := map[string][]string{
		"EMITV-014": {"TRL-EMIT-0007"},
		"EMITV-015": {"TRL-EMIT-0007"},
		"EMITV-018": {"TRL-CONV-0003"},
	}
	for _, vector := range loadTriggerVectors(t) {
		t.Run(vector.id, func(t *testing.T) {
			parsed := parse.Source("azure-pipelines.yml", []byte(vector.ado), diag.NewCollector())
			pipeline, err := normalize.Pipeline(context.Background(), parsed, normalize.Options{
				RepositoryType: normalize.RepositoryGitHub,
			})
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			out, err := Run(pipeline, triggerMapper{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(out.Outcomes) != len(pipeline.Triggers) {
				t.Fatalf("claimed %d triggers, want %d", len(out.Outcomes), len(pipeline.Triggers))
			}
			want := specOnNode(t, vector.gha)
			allowSyntheticDefaults(want, pipeline)
			compareFragment(t, onMapping(out), want)
			var codes []string
			for _, outcome := range out.Outcomes {
				for _, finding := range outcome.Diagnostics {
					codes = append(codes, finding.Code)
				}
			}
			if diff := cmp.Diff(wantCodes[vector.id], codes); diff != "" {
				t.Errorf("emit-stage codes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// onMapping assembles the on document the emitter renders from the mapper
// fragments: the entries in order, or the workflow_dispatch fallback the
// emitter writes when no event remains (emit-gha.md 5.2 rule 2, renderDocument
// in the emit package).
func onMapping(out Output) *goyaml.Node {
	var pairs []model.Pair
	for _, fragment := range out.Fragments {
		for _, workflow := range fragment.Workflow {
			for _, entry := range workflow.On {
				pairs = append(pairs, model.Pair{Key: entry.Key, Value: entry.Value})
			}
		}
	}
	if len(pairs) == 0 {
		return model.Map(model.Pair{Key: "workflow_dispatch", Value: model.Null()})
	}
	return model.Map(pairs...)
}

// specOnNode extracts the on mapping of a vector's expected output.
func specOnNode(t *testing.T, gha string) *goyaml.Node {
	t.Helper()
	var doc goyaml.Node
	if err := goyaml.Unmarshal([]byte(gha), &doc); err != nil {
		t.Fatalf("parse expected output: %v", err)
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "on" {
			return root.Content[i+1]
		}
	}
	t.Fatal("vector output has no on mapping")
	return nil
}

// allowSyntheticDefaults lets the exact comparison accept reader-synthesized
// events that a subset fragment omits (emit-gha.md 7.1): they are expected
// provider defaults, not mapper conversions, so they match !any. EMITV-002/011
// and TestSyntheticTriggerCarriesReaderDefault assert their values exactly.
func allowSyntheticDefaults(on *goyaml.Node, pipeline *ir.Pipeline) {
	for _, trigger := range pipeline.Triggers {
		if !trigger.Binding.Synthetic {
			continue
		}
		var key string
		switch trigger.Kind {
		case ir.TriggerPush:
			key = "push"
		case ir.TriggerPR:
			key = "pull_request"
		default:
			continue
		}
		if lookupKey(on, key) != nil {
			continue
		}
		on.Content = append(on.Content,
			&goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!str", Value: key},
			&goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!any"},
		)
	}
}

// compareFragment asserts the fragment contract of emit-gha.md 7.1: the
// mapping holds exactly the fragment keys, !absent asserts the key is
// missing, {} matches any mapping, sequences match element by element and
// scalars compare by parsed type and value.
func compareFragment(t testing.TB, got, want *goyaml.Node) {
	t.Helper()
	switch want.Kind {
	case goyaml.MappingNode:
		if got == nil || got.Kind != goyaml.MappingNode {
			t.Fatalf("want a mapping, got %v", got)
		}
		if len(want.Content) == 0 {
			return
		}
		for i := 0; i+1 < len(want.Content); i += 2 {
			key, wantValue := want.Content[i].Value, want.Content[i+1]
			gotValue := lookupKey(got, key)
			if wantValue.Tag == "!absent" {
				if gotValue != nil {
					t.Errorf("key %q present, want absent", key)
				}
				continue
			}
			if gotValue == nil {
				t.Errorf("key %q missing", key)
				continue
			}
			compareFragment(t, gotValue, wantValue)
		}
		for i := 0; i+1 < len(got.Content); i += 2 {
			if key := got.Content[i].Value; lookupKey(want, key) == nil {
				t.Errorf("unexpected key %q", key)
			}
		}
	case goyaml.SequenceNode:
		if got == nil || got.Kind != goyaml.SequenceNode || len(got.Content) != len(want.Content) {
			t.Fatalf("sequence mismatch: got %v, want %v", got, want)
		}
		for i := range want.Content {
			compareFragment(t, got.Content[i], want.Content[i])
		}
	default:
		if want.Tag == "!any" {
			return
		}
		if got == nil || got.Tag != want.Tag || got.Value != want.Value {
			t.Errorf("scalar mismatch: got %s %q, want %s %q", got.Tag, got.Value, want.Tag, want.Value)
		}
	}
}

func lookupKey(mapping *goyaml.Node, key string) *goyaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// recordingT captures compareFragment failures without failing the real test.
type recordingT struct {
	testing.TB
	failed bool
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(string, ...any) { r.failed = true }

func (r *recordingT) Fatalf(string, ...any) { r.failed = true; runtime.Goexit() }

func TestCompareFragmentRejectsExtraKeys(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
		fail bool
	}{
		{"exact", "push:\n  branches:\n    - main\n", "push:\n  branches:\n    - main\n", false},
		{"extra event", "push:\nschedule:\n", "push:\n", true},
		{"extra nested key", "push:\n  branches:\n    - main\n  tags:\n    - v1\n", "push:\n  branches:\n    - main\n", true},
		{"absent key present", "push:\nworkflow_dispatch:\n", "push:\nworkflow_dispatch: !absent\n", true},
		{"empty mapping wildcard", "push:\n  branches:\n    - main\n", "push: {}\n", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, want := yamlNode(t, testCase.got), yamlNode(t, testCase.want)
			rec := &recordingT{TB: t}
			done := make(chan struct{})
			go func() {
				defer close(done)
				compareFragment(rec, got, want)
			}()
			<-done
			if rec.failed != testCase.fail {
				t.Errorf("compareFragment failed = %v, want %v", rec.failed, testCase.fail)
			}
		})
	}
}

func yamlNode(t *testing.T, text string) *goyaml.Node {
	t.Helper()
	var doc goyaml.Node
	if err := goyaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

// assertYAML compares a built node with a YAML literal after normalizing both
// through the encoder, so styles never influence the comparison.
func assertYAML(t *testing.T, got *goyaml.Node, want string) {
	t.Helper()
	var doc goyaml.Node
	if err := goyaml.Unmarshal([]byte(want), &doc); err != nil {
		t.Fatal(err)
	}
	resetStyle(got)
	resetStyle(&doc)
	gotBytes, err := goyaml.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := goyaml.Marshal(doc.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(wantBytes), string(gotBytes)); diff != "" {
		t.Errorf("yaml mismatch (-want +got):\n%s", diff)
	}
}

// resetStyle clears the encoder style hints so both sides of assertYAML are
// written with the encoder's own choices.
func resetStyle(node *goyaml.Node) {
	if node == nil {
		return
	}
	node.Style = 0
	for _, child := range node.Content {
		resetStyle(child)
	}
}

func triggerPipeline(triggers ...ir.Trigger) *ir.Pipeline {
	pipeline := newPipeline("ci").withJob("build", "s1").build()
	pipeline.Triggers = triggers
	return pipeline
}

func runTriggers(t *testing.T, pipeline *ir.Pipeline) Output {
	t.Helper()
	out, err := Run(pipeline, triggerMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func TestUnsupportedTriggerKinds(t *testing.T) {
	cases := []struct {
		kind     ir.TriggerKind
		code     string
		fidelity diag.Fidelity
	}{
		{ir.TriggerSchedule, "TRL-CONV-0003", diag.FidelityNeedsManual},
		{ir.TriggerManual, "TRL-CONV-0003", diag.FidelityNeedsManual},
		{ir.TriggerUpstream, "TRL-CONV-0005", diag.FidelityUnsupported},
		{ir.TriggerWebhook, "TRL-CONV-0005", diag.FidelityUnsupported},
		{ir.TriggerOther, "TRL-CONV-0005", diag.FidelityUnsupported},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.kind), func(t *testing.T) {
			out := runTriggers(t, triggerPipeline(ir.Trigger{ID: "t1", Kind: testCase.kind}))
			if len(out.Outcomes) != 1 {
				t.Fatalf("outcomes = %d, want 1", len(out.Outcomes))
			}
			disposition := out.Outcomes[0].Disposition
			if disposition.Kind != "dropped" || disposition.Fidelity != testCase.fidelity {
				t.Errorf("disposition = %+v, want dropped/%s", disposition, testCase.fidelity)
			}
			if len(out.Outcomes[0].Diagnostics) != 1 || out.Outcomes[0].Diagnostics[0].Code != testCase.code {
				t.Errorf("diagnostics = %v, want one %s", out.Outcomes[0].Diagnostics, testCase.code)
			}
			for _, fragment := range out.Fragments {
				if len(fragment.Workflow) != 0 || len(fragment.Jobs) != 0 || len(fragment.Steps) != 0 {
					t.Errorf("dropped trigger contributed a fragment: %v", fragment)
				}
			}
		})
	}
}

func TestSyntheticTriggerCarriesReaderDefault(t *testing.T) {
	trigger := ir.Trigger{
		Metadata: ir.Metadata{Fidelity: diag.FidelityExact},
		ID:       "pr",
		Kind:     ir.TriggerPR,
		Binding:  ir.TriggerBinding{Synthetic: true},
	}
	t.Run("reader finding carried", func(t *testing.T) {
		pipeline := triggerPipeline(trigger)
		pipeline.Diagnostics = []diag.Diagnostic{diag.MustNew("TRL-CONV-0004",
			diag.WithParams(map[string]any{"construct": "trigger", "property": "pr"}))}
		out := runTriggers(t, pipeline)
		if len(out.Outcomes[0].Diagnostics) != 0 {
			t.Errorf("duplicated the reader finding: %v", out.Outcomes[0].Diagnostics)
		}
		if out.Outcomes[0].Disposition.Fidelity != diag.FidelityExact {
			t.Errorf("fidelity = %s, want exact", out.Outcomes[0].Disposition.Fidelity)
		}
		assertYAML(t, onMapping(out), "pull_request:\n")
	})
	t.Run("reader finding missing", func(t *testing.T) {
		out := runTriggers(t, triggerPipeline(trigger))
		findings := out.Outcomes[0].Diagnostics
		if len(findings) != 1 || findings[0].Code != "TRL-CONV-0004" {
			t.Fatalf("findings = %v, want one TRL-CONV-0004", findings)
		}
	})
}

func TestExternalTriggerMaterialized(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{ID: "t1", Kind: ir.TriggerPush}))
	disposition := out.Outcomes[0].Disposition
	if disposition.Kind != "emitted" || disposition.Fidelity != diag.FidelityApproximated {
		t.Errorf("disposition = %+v, want emitted/approximated", disposition)
	}
	findings := out.Outcomes[0].Diagnostics
	if len(findings) != 1 || findings[0].Code != "TRL-EMIT-0001" {
		t.Fatalf("findings = %v, want one TRL-EMIT-0001", findings)
	}
	assertYAML(t, onMapping(out), "push:\n  branches:\n    - '**'\n")
}

func TestPushBranchGuardWithTagsOnly(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{
		Metadata: ir.Metadata{Fidelity: diag.FidelityExact},
		ID:       "t1",
		Kind:     ir.TriggerPush,
		Binding:  ir.TriggerBinding{InYAML: true},
		Filters:  ir.TriggerFilters{Tags: []ir.Glob{{Pattern: "v**"}, {Pattern: "!v**-rc"}}},
	}))
	if len(out.Outcomes[0].Diagnostics) != 0 {
		t.Errorf("diagnostics = %v, want none", out.Outcomes[0].Diagnostics)
	}
	assertYAML(t, onMapping(out), "push:\n  branches:\n    - '**'\n  tags:\n    - 'v**'\n    - '!v**-rc'\n")
}

func TestPullRequestDropsTagFilters(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{
		Metadata: ir.Metadata{Fidelity: diag.FidelityExact},
		ID:       "t1",
		Kind:     ir.TriggerPR,
		Binding:  ir.TriggerBinding{InYAML: true},
		Filters: ir.TriggerFilters{
			Branches: []ir.Glob{{Pattern: "main"}, {Pattern: "refs/tags/v1"}},
			Tags:     []ir.Glob{{Pattern: "v**"}},
		},
	}))
	findings := out.Outcomes[0].Diagnostics
	if len(findings) != 1 || findings[0].Code != "TRL-CONV-0005" {
		t.Fatalf("findings = %v, want one TRL-CONV-0005", findings)
	}
	if out.Outcomes[0].Disposition.Fidelity != diag.FidelityApproximated {
		t.Errorf("fidelity = %s, want approximated", out.Outcomes[0].Disposition.Fidelity)
	}
	assertYAML(t, onMapping(out), "pull_request:\n  branches:\n    - main\n")
}

func TestEmptyPatternsDropWithoutWidening(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{
		ID:      "t1",
		Kind:    ir.TriggerPush,
		Binding: ir.TriggerBinding{InYAML: true},
		Filters: ir.TriggerFilters{Branches: []ir.Glob{{Pattern: ""}, {Pattern: "!"}}},
	}))
	if len(out.Outcomes[0].Diagnostics) != 0 {
		t.Errorf("diagnostics = %v, want none", out.Outcomes[0].Diagnostics)
	}
	assertYAML(t, onMapping(out), "push:\n  branches:\n    - '**'\n")
}

func TestReaderFidelityCarried(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{
		Metadata: ir.Metadata{Fidelity: diag.FidelityApproximated},
		ID:       "t1",
		Kind:     ir.TriggerPush,
		Binding:  ir.TriggerBinding{InYAML: true},
		Filters:  ir.TriggerFilters{Branches: []ir.Glob{{Pattern: "releases/**"}}},
	}))
	if out.Outcomes[0].Disposition.Fidelity != diag.FidelityApproximated {
		t.Errorf("fidelity = %s, want the reader's approximated", out.Outcomes[0].Disposition.Fidelity)
	}
	if len(out.Outcomes[0].Diagnostics) != 0 {
		t.Errorf("diagnostics = %v, want none", out.Outcomes[0].Diagnostics)
	}
}

func TestTriggerMapperSkipsClaimedIDs(t *testing.T) {
	pipeline := triggerPipeline(ir.Trigger{ID: "t1", Kind: ir.TriggerPush})
	id := QualifyID("trigger", "t1")
	out, err := Run(pipeline, fakeMapper{concern: Triggers, ids: []string{id}}, triggerMapper{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Outcomes) != 1 || out.Outcomes[0].Disposition.Kind != "dropped" {
		t.Errorf("outcomes = %v, want the earlier claim only", out.Outcomes)
	}
}

func TestTranslatePattern(t *testing.T) {
	cases := []struct {
		pattern     string
		translated  string
		expressible bool
	}{
		{"main", "main", true},
		{"releases/**", "releases/**", true},
		{"docs/**.md", "docs/**.md", true},
		{"release/v?", "release/v*", false},
		{"a?b[!x]c", "a*b*c", false},
		{"[!abc]", "*", false},
		{"[a-z]", "[a-z]", true},
		{"[a-zA-Z0-9]", "[a-zA-Z0-9]", true},
		{"[a.b]", "*", false},
		{"[]", "*", false},
		{"c++", `c\+\+`, true},
		{`a\?b`, `a\?b`, true},
		{`a\[b`, `a\[b`, true},
		{"a[bc", "a[bc", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern, func(t *testing.T) {
			translated, expressible := translatePattern(testCase.pattern)
			if translated != testCase.translated || expressible != testCase.expressible {
				t.Errorf("translatePattern(%q) = %q, %v; want %q, %v",
					testCase.pattern, translated, expressible, testCase.translated, testCase.expressible)
			}
		})
	}
}

func TestPatternWideningDiagnostics(t *testing.T) {
	out := runTriggers(t, triggerPipeline(ir.Trigger{
		ID:      "t1",
		Kind:    ir.TriggerPush,
		Binding: ir.TriggerBinding{InYAML: true},
		Filters: ir.TriggerFilters{Branches: []ir.Glob{
			{Pattern: "release/v?"},
			{Pattern: "!feature/?"},
		}},
	}))
	findings := out.Outcomes[0].Diagnostics
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want two TRL-EMIT-0007", findings)
	}
	wantParams := []map[string]any{
		{"pattern": "release/v?", "kind": "branch", "action": "widened"},
		{"pattern": "!feature/?", "kind": "branch", "action": "dropped"},
	}
	for i, finding := range findings {
		if finding.Code != "TRL-EMIT-0007" {
			t.Errorf("finding %d code = %s, want TRL-EMIT-0007", i, finding.Code)
		}
		if diff := cmp.Diff(wantParams[i], finding.Params); diff != "" {
			t.Errorf("finding %d params mismatch (-want +got):\n%s", i, diff)
		}
	}
	assertYAML(t, onMapping(out), "push:\n  branches:\n    - 'release/v*'\n")
}
