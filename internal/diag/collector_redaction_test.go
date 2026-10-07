package diag_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestCollectorRedactsEveryInput(t *testing.T) {
	r := redact.New()
	c := diag.NewCollector(diag.WithRedactor(r), diag.WithPerCodeCap(1))
	r.Add("run-secret") // Registration after collector construction is supported.
	d := diag.Diagnostic{
		Code: "TRL-PARSE-0001", Message: "run-secret", Remediation: "run-secret",
		Source:   &diag.Source{File: "run-secret", Platform: "run-secret", Line: 3, Col: 7},
		Resource: &diag.Resource{Kind: "run-secret", Key: "run-secret", Name: "run-secret"},
		Params:   map[string]any{"run-secret": []any{map[string]any{"value": "run-secret", "url": "https://example.test/?a=keep&sig=hidden-signature&sp=rw"}}},
	}
	if !c.Add(d) || c.Add(d) {
		t.Fatal("redacted dedup failed")
	}
	encoded, err := json.Marshal(c.List())
	if err != nil || strings.Contains(string(encoded), "run-secret") || strings.Contains(string(encoded), "hidden-signature") || strings.Contains(string(encoded), "sp=rw") {
		t.Fatalf("collector leaked: %s, %v", encoded, err)
	}
	if d.Source.File != "run-secret" || d.Resource.Key != "run-secret" {
		t.Fatal("Add mutated caller")
	}
	d.Message = "different"
	if c.Add(d) || c.Suppressed()[d.Code] != 1 {
		t.Fatal("cap failed")
	}
	if c.List()[0].Source.Col != 7 {
		t.Fatal("position changed")
	}
}

func TestCollectorCoversParserLayers(t *testing.T) {
	for _, input := range []string{
		"steps:\n- script: echo hi\n  enabled: run-secret\n",
		"run-secret: one\nrun-secret: two\n",
		"steps:\n- script: echo hi\n  condition: eq('run-secret',)\n",
	} {
		r := redact.New()
		r.Add("run-secret")
		c := diag.NewCollector(diag.WithRedactor(r))
		parse.Source("run-secret.yml", []byte(input), c)
		findings := c.List()
		if len(findings) == 0 {
			t.Fatalf("no regression exercised: %q", input)
		}
		encoded, err := json.Marshal(findings)
		if err != nil || strings.Contains(string(encoded), "run-secret") {
			t.Fatalf("leaked: %s, %v", encoded, err)
		}
	}
}
