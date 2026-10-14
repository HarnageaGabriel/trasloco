package normalize

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func leaks(t *testing.T, yaml string, canaries ...string) *ir.Pipeline {
	t.Helper()
	got := normalizeText(t, yaml)
	diagnostics, err := json.Marshal(got.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	out := canonical(t, got) + string(diagnostics)
	for _, canary := range canaries {
		if strings.Contains(out, canary) {
			t.Errorf("canary %q leaked into the IR", canary)
		}
	}
	return got
}

func TestMatrixLegSecretValuesRedacted(t *testing.T) {
	got := leaks(t, "jobs:\n- job: j\n  strategy:\n    matrix:\n      a:\n        password: matrix-pw-canary\n        apiToken: matrix-token-canary\n        keep: plain\n  steps:\n  - script: ok\n",
		"matrix-pw-canary", "matrix-token-canary")
	job := jobByID(t, got, "j")
	if job.Fidelity != diag.FidelityNeedsManual {
		t.Fatalf("job fidelity = %v, want needs-manual (losses %v)", job.Fidelity, lossProperties(t, job.Metadata))
	}
	if job.Matrix == nil || len(job.Matrix.Legs) != 1 {
		t.Fatalf("matrix legs missing: %+v", job.Matrix)
	}
	for _, key := range []string{"password", "apiToken"} {
		if v, _ := job.Matrix.Legs[0].Values.Get(key); v.String != redact.Replacement() {
			t.Errorf("matrix %s = %q, want redaction placeholder", key, v.String)
		}
	}
	if v, _ := job.Matrix.Legs[0].Values.Get("keep"); v.String != "plain" {
		t.Fatalf("non-secret leg value changed: %+v", v)
	}
	losses := strings.Join(lossProperties(t, job.Metadata), " ")
	for _, property := range []string{"matrix.password", "matrix.apiToken"} {
		if !strings.Contains(losses, property) {
			t.Errorf("losses %q do not contain %q", losses, property)
		}
	}
}

func TestPairScrubberBlockers(t *testing.T) {
	options := func(value string) string {
		return "jobs:\n- job: j\n  container:\n    image: x\n    options: '" + value + "'\n  steps:\n  - script: ok\n"
	}
	resource := func(value string) string {
		return "resources:\n  containers:\n  - container: c\n    image: x\n    options: '" + value + "'\nsteps:\n- script: ok\n"
	}
	tests := []struct {
		name, value string
		canaries    []string
	}{
		{"equals env", "--env=DB_PASSWORD=eq-canary", []string{"eq-canary"}},
		{"comma tail", "DB_PASSWORD=comma-canary,tail-canary", []string{"comma-canary", "tail-canary"}},
		{"bracket tail", "DB_PASSWORD=br-canary]tail-canary", []string{"br-canary", "tail-canary"}},
		{"brace tail", "DB_PASSWORD=brace-canary}tail-canary", []string{"brace-canary", "tail-canary"}},
		{"semicolon tail", "DB_PASSWORD=semi-canary;tail-canary", []string{"semi-canary", "tail-canary"}},
		{"quoted pair", `"DB_PASSWORD=qp-canary tail-canary"`, []string{"qp-canary", "tail-canary"}},
		{"quoted value", `DB_PASSWORD="qv-canary tail-canary"`, []string{"qv-canary", "tail-canary"}},
		{"ampersand tail", "DB_PASSWORD=amp-canary&tail-canary", []string{"amp-canary", "tail-canary"}},
		{"angle tail", "DB_PASSWORD=lt-canary<tail-canary", []string{"lt-canary", "tail-canary"}},
	}
	for _, test := range tests {
		t.Run(test.name+"/job", func(t *testing.T) {
			got := leaks(t, options(test.value), test.canaries...)
			if jobByID(t, got, "j").Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("job fidelity = %v", jobByID(t, got, "j").Fidelity)
			}
		})
		t.Run(test.name+"/resource", func(t *testing.T) {
			leaks(t, resource(test.value), test.canaries...)
		})
	}
}

func TestScriptLikeLeavesOutsideStepsReplaced(t *testing.T) {
	cmd := "echo ##vso[task.setsecret]%s-canary"
	tests := []struct{ name, yaml, canary string }{
		{"parameter default", "parameters:\n- name: p\n  type: string\n  default: '" + strings.Replace(cmd, "%s", "pd", 1) + "'\nsteps:\n- script: ok\n", "pd-canary"},
		{"stepList default", "parameters:\n- name: s\n  type: stepList\n  default:\n  - script: '" + strings.Replace(cmd, "%s", "sl", 1) + "'\nsteps:\n- script: ok\n", "sl-canary"},
		{"template context", "jobs:\n- job: j\n  templateContext:\n    note: '" + strings.Replace(cmd, "%s", "tc", 1) + "'\n  steps:\n  - script: ok\n", "tc-canary"},
		{"unknown key", "unknownKey: '" + strings.Replace(cmd, "%s", "uk", 1) + "'\nsteps:\n- script: ok\n", "uk-canary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			leaks(t, test.yaml, test.canary)
			if got := (&backstop{hitSet: map[*ir.Metadata]bool{}, raise: map[*ir.Metadata]bool{}}).text(strings.Replace(cmd, "%s", test.name, 1), false); got != redact.Replacement() {
				t.Errorf("logging-command leaf = %q, want redaction placeholder", got)
			}
		})
	}
}

func TestLineContinuationJoinsCommandName(t *testing.T) {
	tests := []struct{ name, script, canary, step, pool string }{
		{"bash backslash", "echo ##vso[task.setse\\\ncret]bash-cont-canary", "bash-cont-canary", "script", ""},
		{"cmd caret", "echo ##vso[task.setse^\ncret]cmd-cont-canary", "cmd-cont-canary", "script", "windows-latest"},
		{"pwsh backtick", "Write-Host ##vso[task.setse`\ncret]pwsh-cont-canary", "pwsh-cont-canary", "pwsh", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !secretLoggingLiteral(test.script) {
				t.Fatalf("continuation not detected in %q", test.script)
			}
			yaml := ""
			if test.pool != "" {
				yaml += "pool:\n  vmImage: " + test.pool + "\n"
			}
			yaml += "steps:\n- " + test.step + ": |\n    " + strings.ReplaceAll(test.script, "\n", "\n    ") + "\n"
			leaks(t, yaml, test.canary)
		})
	}
}

func TestTaskArgumentsPairScrubbed(t *testing.T) {
	leaks(t, "steps:\n- task: Docker@2\n  name: s\n  inputs:\n    arguments: --build-arg NPM_TOKEN=docker-arg-canary\n", "docker-arg-canary")
}

func TestRawJSONScrubKeepsPriorValuesAndLosses(t *testing.T) {
	b := &backstop{hitSet: map[*ir.Metadata]bool{}, raise: map[*ir.Metadata]bool{}}
	m := ir.DefaultMetadata()
	m.Extensions = ir.Extensions{ExtensionKey: json.RawMessage(`{"values":{"keep":"v"},"losses":[{"construct":"c","property":"p"}],"a":"postgres://u:fallback-canary@h/db"}`)}
	b.stack = append(b.stack, &m)
	b.metadata(&m)
	b.stack = b.stack[:0]
	c := &conv{own: diag.NewCollector()}
	p := &ir.Pipeline{Metadata: ir.DefaultMetadata()}
	p.Origin = &ir.Origin{Source: diag.Source{File: "pipeline.yml"}}
	for _, hit := range b.hit {
		c.recordBackstop(hit, p)
	}
	out := m.Extensions[ExtensionKey]
	if strings.Contains(string(out), "fallback-canary") || !json.Valid(out) || !strings.Contains(string(out), `"keep":"v"`) ||
		!strings.Contains(string(out), `"construct":"c"`) || !strings.Contains(string(out), `"property":"secretMaterial"`) {
		t.Fatalf("extension after raw JSON scrub = %s", out)
	}
	// Input that is not JSON is kept as a scrubbed string, never wiped.
	out = b.rawJSON([]byte(`prior text postgres://u:fallback-two-canary@h/db`))
	if strings.Contains(string(out), "fallback-two-canary") || !json.Valid(out) || !strings.Contains(string(out), "prior text") {
		t.Fatalf("rawJSON fallback = %s", out)
	}
}

func TestBackstopPassesThroughBenignText(t *testing.T) {
	for _, text := range []string{
		"https://example.com/a/b?x=1&y=2", "git@github.com:org/repo.git", "ssh://git@host/org/repo.git",
		"@scope/pkg", "LOG_LEVEL=debug", "TOKEN_ENDPOINT=https://login.example.com/token", "KEY=$(x)",
		"--env LOG_LEVEL=debug --env TOKEN_ENDPOINT=https://a/b",
	} {
		if got := scrubPairs(safeText(text)); got != text {
			t.Errorf("scrub(%q) = %q", text, got)
		}
	}
}

type synthetic struct {
	ir.Metadata
	Text   string
	Map    map[string]string
	Iface  any
	Arr    [1]string
	Raw    ir.Raw
	Opts   string
	Nested []string
}

func TestBackstopWalkBranches(t *testing.T) {
	node := &synthetic{
		Text:  "postgres://u:walk-text-canary@h/db",
		Map:   map[string]string{"options": "DB_PASSWORD=walk-map-canary", "other": "plain"},
		Iface: "postgres://u:walk-iface-canary@h/db",
		Arr:   [1]string{"postgres://u:walk-arr-canary@h/db"},
		Raw: ir.Raw{
			Provider: "azure-pipelines",
			Kind:     "synthetic",
			Format:   "json",
			JSON:     json.RawMessage(`{"k":"DB_PASSWORD=walk-raw-canary\u0026tail-canary\u003cmore-canary"}`),
		},
		Opts:   "plain",
		Nested: []string{"postgres://u:walk-slice-canary@h/db"},
	}
	b := &backstop{hitSet: map[*ir.Metadata]bool{}, raise: map[*ir.Metadata]bool{}}
	b.walk(reflect.ValueOf(node).Elem(), false)
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"walk-text", "walk-map", "walk-iface", "walk-arr", "walk-raw", "tail-canary", "more-canary", "walk-slice"} {
		if strings.Contains(string(encoded), canary) {
			t.Errorf("%s leaked: %s", canary, encoded)
		}
	}
	if node.Map["other"] != "plain" || node.Opts != "plain" {
		t.Fatalf("benign values changed: %+v", node)
	}
	if !b.hitSet[&node.Metadata] || !b.raise[&node.Metadata] {
		t.Fatal("hit not attributed to the nearest metadata")
	}
}
