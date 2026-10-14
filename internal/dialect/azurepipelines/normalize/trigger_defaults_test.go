package normalize

import (
	"context"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// normalizeWithOptions is normalizeText with an explicit Options value.
func normalizeWithOptions(t testing.TB, text string, opts Options) *ir.Pipeline {
	t.Helper()
	data := []byte(text)
	parsed := parse.Source("pipeline.yml", data, diag.NewCollector())
	if opts.SourceText == nil {
		opts.SourceText = func(string) ([]byte, bool) { return data, true }
	}
	got, err := Pipeline(context.Background(), parsed, opts)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func triggerByID(t *testing.T, p *ir.Pipeline, id string) *ir.Trigger {
	t.Helper()
	for i := range p.Triggers {
		if p.Triggers[i].ID == id {
			return &p.Triggers[i]
		}
	}
	t.Fatalf("trigger %q not found in %v", id, p.Triggers)
	return nil
}

func hasCode(p *ir.Pipeline, code string) bool {
	for _, finding := range p.Diagnostics {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func hasGlobSynthesis(p *ir.Pipeline) bool {
	for _, finding := range p.Diagnostics {
		if finding.Code == codeSynth && finding.Params["construct"] == "glob" {
			return true
		}
	}
	return false
}

const (
	prOmittedYAML  = "steps:\n- script: echo build\n"
	prDeclaredYAML = "pr:\n  branches:\n    include:\n    - main\nsteps:\n- script: echo build\n"
	prNoneYAML     = "pr: none\nsteps:\n- script: echo build\n"
)

// TestDefaultPRTriggerIsRepositoryDependent covers the ir.md section 4 rule: an
// omitted `pr` synthesizes an unfiltered PR trigger on GitHub/Bitbucket Cloud,
// none on Azure Repos Git (branch policies own validation), and an approximated
// one for other/unknown repository types.
func TestDefaultPRTriggerIsRepositoryDependent(t *testing.T) {
	tests := []struct {
		name         string
		repo         RepositoryType
		yaml         string
		wantPR       bool
		wantFidelity diag.Fidelity
		wantInYAML   bool
	}{
		{name: "github omitted", repo: RepositoryGitHub, yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityExact},
		{name: "bitbucket omitted", repo: RepositoryBitbucket, yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityExact},
		{name: "azure repos git omitted", repo: RepositoryAzureReposGit, yaml: prOmittedYAML, wantPR: false},
		{name: "other omitted", repo: RepositoryOther, yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityApproximated},
		{name: "unknown explicit omitted", repo: RepositoryUnknown, yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityApproximated},
		{name: "unknown zero value omitted", repo: "", yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityApproximated},
		{name: "unrecognized type omitted", repo: RepositoryType("gitlab"), yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityApproximated},
		{name: "case variant omitted", repo: RepositoryType("GitHub"), yaml: prOmittedYAML, wantPR: true, wantFidelity: diag.FidelityApproximated},
		{name: "github declared", repo: RepositoryGitHub, yaml: prDeclaredYAML, wantPR: true, wantFidelity: diag.FidelityExact, wantInYAML: true},
		{name: "github none", repo: RepositoryGitHub, yaml: prNoneYAML, wantPR: false},
		{name: "bitbucket none", repo: RepositoryBitbucket, yaml: prNoneYAML, wantPR: false},
		{name: "azure repos git none", repo: RepositoryAzureReposGit, yaml: prNoneYAML, wantPR: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeWithOptions(t, test.yaml, Options{RepositoryType: test.repo})
			var pr *ir.Trigger
			for i := range got.Triggers {
				if got.Triggers[i].Kind == ir.TriggerPR {
					pr = &got.Triggers[i]
				}
			}
			if !test.wantPR {
				if pr != nil {
					t.Fatalf("unexpected pr trigger: %+v", *pr)
				}
				return
			}
			if pr == nil {
				t.Fatalf("no pr trigger in %v", got.Triggers)
			}
			if pr.Fidelity != test.wantFidelity {
				t.Fatalf("pr fidelity = %s, want %s", pr.Fidelity, test.wantFidelity)
			}
			if pr.Binding.InYAML != test.wantInYAML {
				t.Fatalf("pr inYaml = %t, want %t", pr.Binding.InYAML, test.wantInYAML)
			}
			if pr.Binding.Synthetic == test.wantInYAML {
				t.Fatalf("pr synthetic = %t, want %t", pr.Binding.Synthetic, !test.wantInYAML)
			}
			if !hasCode(got, codeSynth) {
				t.Fatalf("no TRL-CONV-0004 diagnostic: %v", got.Diagnostics)
			}
			if test.wantInYAML {
				if len(pr.Filters.Branches) != 1 || pr.Filters.Branches[0].Pattern != "main" {
					t.Fatalf("declared pr filters = %+v", pr.Filters)
				}
				return
			}
			if len(pr.Filters.Branches) != 0 || len(pr.Filters.Tags) != 0 || len(pr.Filters.Paths) != 0 {
				t.Fatalf("synthesized pr filters = %+v, want none", pr.Filters)
			}
		})
	}

	// Azure Repos Git records why it does not synthesize even though no trigger appears.
	got := normalizeWithOptions(t, prOmittedYAML, Options{RepositoryType: RepositoryAzureReposGit})
	if !hasCode(got, codeSynth) {
		t.Fatalf("azureReposGit must explain the omitted pr trigger: %v", got.Diagnostics)
	}
}

// TestDefaultCITriggerSynthesis covers the default CI trigger: synthesized with a
// synthetic binding, absent for `trigger: none`, and in YAML when declared.
func TestDefaultCITriggerSynthesis(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantCI        bool
		wantInYAML    bool
		wantSynthetic bool
	}{
		{name: "omitted", yaml: "steps:\n- script: echo hi\n", wantCI: true, wantSynthetic: true},
		{name: "none", yaml: "trigger: none\nsteps:\n- script: echo hi\n", wantCI: false},
		{name: "declared", yaml: "trigger:\n  branches:\n    include:\n    - main\nsteps:\n- script: echo hi\n", wantCI: true, wantInYAML: true},
		{name: "empty list", yaml: "trigger: []\nsteps:\n- script: echo hi\n", wantCI: true, wantInYAML: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeWithOptions(t, test.yaml, Options{RepositoryType: RepositoryGitHub})
			var ci *ir.Trigger
			for i := range got.Triggers {
				if got.Triggers[i].ID == "ci" {
					ci = &got.Triggers[i]
				}
			}
			if !test.wantCI {
				if ci != nil {
					t.Fatalf("unexpected ci trigger: %+v", *ci)
				}
				return
			}
			if ci == nil {
				t.Fatalf("no ci trigger in %v", got.Triggers)
			}
			if ci.Binding.InYAML != test.wantInYAML || ci.Binding.Synthetic != test.wantSynthetic {
				t.Fatalf("ci binding = %+v, want inYaml=%t synthetic=%t", ci.Binding, test.wantInYAML, test.wantSynthetic)
			}
			if ci.Fidelity != diag.FidelityExact {
				t.Fatalf("ci fidelity = %s, want exact", ci.Fidelity)
			}
			if test.wantSynthetic && !hasCode(got, codeSynth) {
				t.Fatalf("no TRL-CONV-0004 for the synthesized ci trigger: %v", got.Diagnostics)
			}
		})
	}
}

// TestGlobNormalization covers the IR glob rules: Azure `*` (crosses `/`) maps to
// `**`, `?`/classes/escapes are preserved, includes precede excludes, and an
// exclude-only list materializes an implicit `**` include.
func TestGlobNormalization(t *testing.T) {
	type glob struct{ pattern, original string }
	tests := []struct {
		name       string
		filter     string
		key        string
		want       []glob
		wantApprox bool
		wantSynth  bool
	}{
		{
			name: "include star widens", filter: "  branches:\n    include:\n    - 'release/*'\n", key: "branches",
			want: []glob{{"release/**", "release/*"}}, wantApprox: true,
		},
		{
			name: "bare star widens", filter: "  branches:\n    include:\n    - '*'\n", key: "branches",
			want: []glob{{"**", "*"}}, wantApprox: true,
		},
		{
			name: "double star idempotent", filter: "  branches:\n    include:\n    - 'services/api/**'\n", key: "branches",
			want: []glob{{"services/api/**", "services/api/**"}},
		},
		{
			name: "nested path star widens", filter: "  paths:\n    include:\n    - 'src/*/test'\n", key: "paths",
			want: []glob{{"src/**/test", "src/*/test"}}, wantApprox: true,
		},
		{
			name: "question mark preserved", filter: "  branches:\n    include:\n    - 'v?'\n", key: "branches",
			want: []glob{{"v?", "v?"}},
		},
		{
			name: "class preserved", filter: "  branches:\n    include:\n    - '[ab]x*'\n", key: "branches",
			want: []glob{{"[ab]x**", "[ab]x*"}}, wantApprox: true,
		},
		{
			name: "escaped star preserved", filter: "  branches:\n    include:\n    - '\\*'\n", key: "branches",
			want: []glob{{"\\*", "\\*"}},
		},
		{
			name: "escape then star widens", filter: "  branches:\n    include:\n    - 'a\\*b*'\n", key: "branches",
			want: []glob{{"a\\*b**", "a\\*b*"}}, wantApprox: true,
		},
		{
			name: "include precedes exclude", filter: "  branches:\n    include:\n    - main\n    exclude:\n    - 'legacy/*'\n", key: "branches",
			want: []glob{{"main", "main"}, {"!legacy/**", "legacy/*"}}, wantApprox: true,
		},
		{
			name: "exclude only materializes include", filter: "  paths:\n    exclude:\n    - 'docs/*'\n", key: "paths",
			want: []glob{{"**", "**"}, {"!docs/**", "docs/*"}}, wantApprox: true, wantSynth: true,
		},
		{
			name: "exclude only without star", filter: "  branches:\n    exclude:\n    - legacy\n", key: "branches",
			want: []glob{{"**", "**"}, {"!legacy", "legacy"}}, wantSynth: true,
		},
		{
			name: "tags widen", filter: "  tags:\n    include:\n    - 'v*'\n    exclude:\n    - 'v*-beta'\n", key: "tags",
			want: []glob{{"v**", "v*"}, {"!v**-beta", "v*-beta"}}, wantApprox: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := "trigger:\n" + test.filter + "steps:\n- script: echo hi\n"
			got := normalizeWithOptions(t, yaml, Options{RepositoryType: RepositoryGitHub})
			ci := triggerByID(t, got, "ci")
			var globs []ir.Glob
			switch test.key {
			case "branches":
				globs = ci.Filters.Branches
			case "paths":
				globs = ci.Filters.Paths
			case "tags":
				globs = ci.Filters.Tags
			}
			if len(globs) != len(test.want) {
				t.Fatalf("%s globs = %+v, want %+v", test.key, globs, test.want)
			}
			for i := range test.want {
				if globs[i].Pattern != test.want[i].pattern || globs[i].Original != test.want[i].original {
					t.Fatalf("glob %d = %+v, want %+v", i, globs[i], test.want[i])
				}
			}
			approx := ci.Fidelity == diag.FidelityApproximated
			if approx != test.wantApprox || hasCode(got, codeApprox) != test.wantApprox {
				t.Fatalf("approximation = %t (diag %t), want %t", approx, hasCode(got, codeApprox), test.wantApprox)
			}
			if hasGlobSynthesis(got) != test.wantSynth {
				t.Fatalf("glob synthesis = %t, want %t (%v)", hasGlobSynthesis(got), test.wantSynth, got.Diagnostics)
			}
		})
	}
}

// TestTranslateGlobIsIdempotent pins the pure translation function.
func TestTranslateGlobIsIdempotent(t *testing.T) {
	tests := []struct {
		in, want string
		changed  bool
	}{
		{"main", "main", false},
		{"release/*", "release/**", true},
		{"*", "**", true},
		{"**", "**", false},
		{"***", "**", true},
		{"a**b", "a**b", false},
		{"src/*/test", "src/**/test", true},
		{"v?", "v?", false},
		{"[ab]*", "[ab]**", true},
		{"[a*b]", "[a*b]", false},
		{"[a*b]*", "[a*b]**", true},
		{"\\*", "\\*", false},
		{"a\\*b*", "a\\*b**", true},
		{"", "", false},
	}
	for _, test := range tests {
		got, changed := translateGlob(test.in)
		if got != test.want || changed != test.changed {
			t.Errorf("translateGlob(%q) = %q, %t; want %q, %t", test.in, got, changed, test.want, test.changed)
		}
		if again, _ := translateGlob(got); again != got {
			t.Errorf("translateGlob(%q) is not idempotent: %q", test.in, again)
		}
	}
}
