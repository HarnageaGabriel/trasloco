package vars

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// r8Run evaluates two script steps after a variables block and returns the
// effective "m" at the consumer step.
func r8Run(t testing.TB, kind ast.ScriptKind, agentOS string, vars [][2]string, script string) (*Graph, Candidate, bool, []diag.Diagnostic) {
	t.Helper()
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: kind, Script: scalarNode(script), StepProps: ast.StepProps{Name: scalarNode("emit")}},
		&ast.ScriptStep{Kind: kind, Script: scalarNode("echo consume")},
	)}
	if len(vars) > 0 {
		entries := make([]ast.MapEntry[*ast.Scalar], 0, len(vars))
		for _, v := range vars {
			entries = append(entries, ast.MapEntry[*ast.Scalar]{Key: scalarNode(v[0]), Value: scalarNode(v[1])})
		}
		pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: entries}}
	}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: agentOS}, collector)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	candidate, ok := graph.Resolve(stepScope(1), "m", PhaseMacro)
	return graph, candidate, ok, collector.List()
}

func wantUnknownM(t *testing.T, label string, candidate Candidate, ok bool, findings []diag.Diagnostic) {
	t.Helper()
	if !ok || !candidate.Unknown || candidate.Value != "" {
		t.Fatalf("%s: m = %#v, %t; want Unknown", label, candidate, ok)
	}
	if !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("%s: diagnostics = %v, want TRL-VAR-0016", label, codes(findings))
	}
}

// growthVariables are v0..v5, each ten times the previous one (v5 is 1 MB).
func growthVariables() [][2]string {
	vars := [][2]string{{"v0", "aaaaaaaaaa"}}
	for i := 1; i <= 5; i++ {
		vars = append(vars, [2]string{"v" + string(rune('0'+i)), strings.Repeat("$(v"+string(rune('0'+i-1))+")", 10)})
	}
	return vars
}

func TestReviewR8MacrosInScriptsAreNeverExpandedPerLine(t *testing.T) {
	graph, _, _, _ := r8Run(t, ast.ScriptBash, AgentOSLinux, growthVariables(), "echo x")
	table, ok := graph.Table(stepScope(0))
	if !ok {
		t.Fatal("no step table")
	}
	expander := newMacroExpander(table)
	step := &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(strings.Repeat("echo $(v2)\n", 8))}
	if events := mutationsWithExpander(step, expander, "", "", "", 0, AgentOSLinux); len(events) != 0 {
		t.Fatalf("events = %#v; known benign macros warn about nothing", events)
	}
	work := expander.totalWork
	for i := 0; i < 3; i++ {
		expander.useTable(table)
		_ = mutationsWithExpander(step, expander, "", "", "", i, AgentOSLinux)
	}
	if expander.totalWork != work {
		t.Fatalf("unchanged steps rebuilt memoized values: work grew from %d to %d", work, expander.totalWork)
	}
}

func BenchmarkReviewR8MacrosInScriptsUseMemo(b *testing.B) {
	graph, _, _, _ := r8Run(b, ast.ScriptBash, AgentOSLinux, growthVariables(), "echo x")
	table, ok := graph.Table(stepScope(0))
	if !ok {
		b.Fatal("no step table")
	}
	expander := newMacroExpander(table)
	step := &ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(strings.Repeat("echo $(v5)\n", 5) + strings.Repeat("echo $(v2)\n", 5000))}
	_ = mutationsWithExpander(step, expander, "", "", "", 0, AgentOSLinux)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		expander.useTable(table)
		_ = mutationsWithExpander(step, expander, "", "", "", i, AgentOSLinux)
	}
}

func TestReviewR8BigMacroValueEndsProvability(t *testing.T) {
	_, candidate, ok, findings := r8Run(t, ast.ScriptBash, AgentOSLinux, growthVariables(), "echo $(v5)\necho '"+markerM+"v'")
	wantUnknownM(t, "big value", candidate, ok, findings)
	_, candidate, ok, findings = r8Run(t, ast.ScriptBash, AgentOSLinux, growthVariables(), "echo "+strings.Repeat("$(v3) ", 8)+"\necho '"+markerM+"v'")
	wantUnknownM(t, "long line", candidate, ok, findings)
}

func TestReviewR8CmdRemMustBeInert(t *testing.T) {
	const emit = "\necho " + markerM + "v"
	for _, tc := range []struct{ name, line string }{
		{"help switch and exit", "REM /? & exit /b 1"},
		{"help switch", "REM /? x"},
		{"help redirect", "REM /?>"},
		{"percent tilde", "REM %~"},
		{"text percent tilde", "REM x%~"},
		{"redirect amp", "REM <&"},
		{"caret percent", "REM ^%"},
		{"substitute char", "REM a\x1aexit /b 1"},
		{"macro to percent", "REM $(p)~"},
	} {
		_, candidate, ok, findings := r8Run(t, ast.ScriptCmd, AgentOSWindows, [][2]string{{"p", "50%"}}, tc.line+emit)
		wantUnknownM(t, tc.name, candidate, ok, findings)
	}
	_, candidate, ok, _ := r8Run(t, ast.ScriptCmd, AgentOSWindows, nil, "REM plain text. ok, 1_2-3\nREM\n"+strings.TrimPrefix(emit, "\n"))
	if !ok || candidate.Unknown || candidate.Value != "v" {
		t.Fatalf("inert REM comments must stay proven: %#v, %t", candidate, ok)
	}
}

func TestReviewR8ControlCharactersEndProvability(t *testing.T) {
	const pwshEmit = "Write-Host '" + markerM + "v'"
	for _, tc := range []struct {
		name   string
		kind   ast.ScriptKind
		os     string
		script string
	}{
		{"pwsh comment lone CR", ast.ScriptPwsh, AgentOSLinux, "# c\rexit 1\n" + pwshEmit},
		{"pwsh comment SUB", ast.ScriptPwsh, AgentOSLinux, "# c\x1aexit 1\n" + pwshEmit},
		{"pwsh comment NUL", ast.ScriptPwsh, AgentOSLinux, "# c\x00\n" + pwshEmit},
		{"pwsh CR before CRLF", ast.ScriptPwsh, AgentOSLinux, "# c\r\r\n" + pwshEmit},
		{"pwsh trailing lone CR", ast.ScriptPwsh, AgentOSLinux, pwshEmit + "\r"},
		{"bash comment lone CR", ast.ScriptBash, AgentOSLinux, "# c\rexit 1\necho '" + markerM + "v'"},
		{"bash lone CR", ast.ScriptBash, AgentOSLinux, " \rx\necho '" + markerM + "v'"},
		{"bash comment DEL", ast.ScriptBash, AgentOSLinux, "# c\x7f\necho '" + markerM + "v'"},
		{"cmd echo SUB", ast.ScriptCmd, AgentOSWindows, "echo a\x1ab\necho " + markerM + "v"},
		{"bash NBSP line", ast.ScriptBash, AgentOSLinux, " \necho '" + markerM + "v'"},
		{"pwsh ideographic space line", ast.ScriptPwsh, AgentOSLinux, "　\n" + pwshEmit},
	} {
		_, candidate, ok, findings := r8Run(t, tc.kind, tc.os, nil, tc.script)
		wantUnknownM(t, tc.name, candidate, ok, findings)
	}
	// A CRLF script on pwsh and cmd stays provable.
	_, candidate, ok, _ := r8Run(t, ast.ScriptPwsh, AgentOSLinux, nil, "# c\r\n"+pwshEmit)
	if !ok || candidate.Unknown || candidate.Value != "v" {
		t.Fatalf("CRLF pwsh script must stay proven: %#v, %t", candidate, ok)
	}
	_, candidate, ok, _ = r8Run(t, ast.ScriptBash, AgentOSLinux, nil, "\r\necho '"+markerM+"v'")
	if !ok || candidate.Unknown || candidate.Value != "v" {
		t.Fatalf("CRLF bash script must stay proven: %#v, %t", candidate, ok)
	}
}

func TestReviewR8MacroValueWithMarkerPoisonsTheStep(t *testing.T) {
	vars := [][2]string{{"hostile", "##vso[task.setvariable variable=hit]x"}}
	// Names are not guessed: the step gets one warning and later emissions are unproven.
	_, candidate, ok, findings := r8Run(t, ast.ScriptBash, AgentOSLinux, vars, "echo \"$(hostile)\"\necho '"+markerM+"v'")
	wantUnknownM(t, "later emission", candidate, ok, findings)
	if n := countCode(findings, "TRL-VAR-0016"); n < 2 {
		t.Fatalf("diagnostics = %v, want the step warning and the unknown variable", codes(findings))
	}
	graph, _, _, findings := r8Run(t, ast.ScriptBash, AgentOSLinux, vars, "echo \"$(hostile)\"")
	if _, ok := graph.Resolve(stepScope(1), "hit", PhaseMacro); ok || countCode(findings, "TRL-VAR-0016") != 1 {
		t.Fatalf("hit resolved = %t, diagnostics = %v; want only the step warning", ok, codes(findings))
	}
}

func countCode(findings []diag.Diagnostic, code string) int {
	n := 0
	for _, f := range findings {
		if f.Code == code {
			n++
		}
	}
	return n
}

func TestReviewR8SecretInMarkerValueIsReported(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo '" + markerM + "$(s)'")},
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo consume")},
	)}
	pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{{Key: scalarNode("s"), Value: scalarNode("x")}}}}
	// A secret binding comes from the logging command of an earlier step.
	pipeline.Steps = list[ast.Step](
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo '##vso[task.setvariable variable=s;issecret=true]x'")},
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo '" + markerM + "pre-$(s)'")},
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo consume")},
	)
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, collector)
	if err != nil {
		t.Fatal(err)
	}
	candidate, ok := graph.Resolve(stepScope(2), "m", PhaseMacro)
	if !ok || !candidate.Unknown || candidate.Value != "" {
		t.Fatalf("m = %#v, %t; want Unknown without a value", candidate, ok)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
}

func TestReviewR8HeaderMacroIsUnknownAndSecret(t *testing.T) {
	graph, _, _, findings := r8Run(t, ast.ScriptBash, AgentOSLinux, nil, "echo '##vso[task.setvariable variable=m;issecret=$(f)]v'")
	candidate, ok := graph.Resolve(stepScope(1), "m", PhaseMacro)
	if !ok || !candidate.Unknown || !candidate.Secret || candidate.Value != "" || !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("m = %#v, %t, %v; want Unknown and Secret", candidate, ok, codes(findings))
	}
}

func TestReviewR8DuplicatePropertiesAreConservative(t *testing.T) {
	graph, _, _, findings := r8Run(t, ast.ScriptBash, AgentOSLinux, nil, "echo '##vso[task.setvariable variable=m;issecret=false;issecret=true]v'")
	candidate, ok := graph.Resolve(stepScope(1), "m", PhaseMacro)
	if !ok || !candidate.Secret || candidate.Value != "" || !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("m = %#v, %t, %v; want a secret and a diagnostic", candidate, ok, codes(findings))
	}
	graph, _, _, _ = r8Run(t, ast.ScriptBash, AgentOSLinux, nil, "echo '##vso[task.setvariable variable=m;isOutput=false;isOutput=true]v'")
	if len(graph.Outputs) != 1 {
		t.Fatalf("outputs = %#v, want one output", graph.Outputs)
	}
}

func TestReviewR8TrimIsASCIIOnly(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{{" \t\r\n x \t", "x"}, {" ", " "}, {"　a", "　a"}} {
		if got := trimASCII(tc.in); got != tc.want {
			t.Fatalf("trimASCII(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
