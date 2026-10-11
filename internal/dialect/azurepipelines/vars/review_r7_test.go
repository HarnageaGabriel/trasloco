package vars

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

const markerM = "##vso[task.setvariable variable=m]"

// emissionRun evaluates one script step of the given kind on the given agent OS
// and returns the effective candidate for "m" plus the findings.
func emissionRun(t *testing.T, kind ast.ScriptKind, agentOS, script string) (Candidate, bool, []diag.Diagnostic) {
	t.Helper()
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: kind, Script: scalarNode(script)},
		&ast.ScriptStep{Kind: kind, Script: scalarNode("echo consume")},
	)}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: agentOS}, collector)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	candidate, ok := graph.Resolve(stepScope(1), "m", PhaseMacro)
	return candidate, ok, collector.List()
}

func TestReviewR7UnprovableEmissionsAreUnknown(t *testing.T) {
	cases := []struct {
		name   string
		kind   ast.ScriptKind
		os     string
		script string
	}{
		{"empty cmd echo argument", ast.ScriptCmd, AgentOSWindows, "echo \t\necho " + markerM + "v"},
		{"cmd echo trailing space", ast.ScriptCmd, AgentOSWindows, "echo \n"},
		{"bash lone quote", ast.ScriptBash, AgentOSLinux, "echo \"\necho '" + markerM + "v'"},
		{"pwsh lone quote", ast.ScriptPwsh, AgentOSLinux, "Write-Host '\nWrite-Host '" + markerM + "v'"},
		{"pwsh print", ast.ScriptPwsh, AgentOSLinux, "print '" + markerM + "v'"},
		{"pwsh echo", ast.ScriptPwsh, AgentOSLinux, "echo '" + markerM + "v'"},
		{"bash uppercase echo", ast.ScriptBash, AgentOSLinux, "ECHO '" + markerM + "v'"},
		{"bash printf", ast.ScriptBash, AgentOSLinux, "printf '%s\\n' '" + markerM + "v'"},
		{"cmd caret comment", ast.ScriptCmd, AgentOSWindows, ":: note ^\necho " + markerM + "v"},
		{"cmd rem caret comment", ast.ScriptCmd, AgentOSWindows, "REM note ^\necho " + markerM + "v"},
		{"cmd percent", ast.ScriptCmd, AgentOSWindows, "echo " + markerM + "100%"},
		{"cmd double percent", ast.ScriptCmd, AgentOSWindows, "echo " + markerM + "50%% done"},
		{"cmd percent dotted", ast.ScriptCmd, AgentOSWindows, "echo " + markerM + "%a.b%"},
		{"cmd percent digit", ast.ScriptCmd, AgentOSWindows, "echo " + markerM + "%1"},
		{"macro on earlier bash line", ast.ScriptBash, AgentOSLinux, "echo \"$(Build.SourceVersionMessage)\"\necho '" + markerM + "v'"},
		{"macro on earlier cmd line", ast.ScriptCmd, AgentOSWindows, "echo $(Build.SourceVersionMessage)\necho " + markerM + "v"},
		{"macro in marker value", ast.ScriptBash, AgentOSLinux, "echo '" + markerM + "$(Build.BuildId)'"},
		{"pwsh smart quote", ast.ScriptPwsh, AgentOSLinux, "Write-Host ‘" + markerM + "v’"},
		{"pwsh non-ascii", ast.ScriptPwsh, AgentOSLinux, "Write-Host '" + markerM + "vé'"},
		{"bash set -x", ast.ScriptBash, AgentOSLinux, "set -x\necho '" + markerM + "v'"},
		{"bash set +v", ast.ScriptBash, AgentOSLinux, "set +v\necho '" + markerM + "v'"},
		{"pwsh Set-PSDebug", ast.ScriptPwsh, AgentOSLinux, "Set-PSDebug -Trace 1\nWrite-Host '" + markerM + "v'"},
		{"pwsh requires", ast.ScriptPwsh, AgentOSLinux, "#requires -Version 99\nWrite-Host '" + markerM + "v'"},
		{"bash comment continuation", ast.ScriptBash, AgentOSLinux, "# note \\\necho '" + markerM + "v'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate, ok, findings := emissionRun(t, tc.kind, tc.os, tc.script)
			if strings.Contains(tc.name, "trailing space") {
				return // no marker at all: only the absence of a panic matters
			}
			if !ok || !candidate.Unknown || candidate.Value != "" {
				t.Fatalf("candidate = %#v, %t; want Unknown", candidate, ok)
			}
			if !hasCode(findings, "TRL-VAR-0016") {
				t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(findings))
			}
		})
	}
}

func TestReviewR7MinimalGrammarStaysProven(t *testing.T) {
	cases := []struct {
		name   string
		kind   ast.ScriptKind
		os     string
		script string
	}{
		{"bash double", ast.ScriptBash, AgentOSLinux, "# c\n\necho \"" + markerM + "v\""},
		{"bash single", ast.ScriptBash, AgentOSLinux, "echo 'plain'\necho '" + markerM + "v'"},
		{"pwsh write-host", ast.ScriptPwsh, AgentOSLinux, "# c\nwrite-host '" + markerM + "v'"},
		{"pwsh write-output", ast.ScriptPwsh, AgentOSLinux, "Write-Output \"" + markerM + "v\""},
		{"cmd", ast.ScriptCmd, AgentOSWindows, "@echo off\nREM c\necho " + markerM + "v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate, ok, findings := emissionRun(t, tc.kind, tc.os, tc.script)
			if !ok || candidate.Unknown || candidate.Value != "v" {
				t.Fatalf("candidate = %#v, %t; want v", candidate, ok)
			}
			if hasCode(findings, "TRL-VAR-0016") {
				t.Fatalf("unexpected TRL-VAR-0016")
			}
		})
	}
}

func TestReviewR7InvalidLoggedNameDoesNotAbort(t *testing.T) {
	_, ok, findings := emissionRun(t, ast.ScriptBash, AgentOSLinux, "echo 'x'\necho '"+strings.Replace(markerM, "variable=m", "variable=a b", 1)+"v'")
	if ok {
		t.Fatal("unexpected m")
	}
	if !hasCode(findings, "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(findings))
	}
}

func TestReviewR7HeaderMacroIsConservative(t *testing.T) {
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo '##vso[task.setvariable variable=m;isOutput=$(o)]v'"), StepProps: ast.StepProps{Name: scalarNode("emit")}},
	)}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, collector)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(collector.List(), "TRL-VAR-0016") {
		t.Fatalf("diagnostics = %v, want TRL-VAR-0016", codes(collector.List()))
	}
	if len(graph.Outputs) != 1 || !graph.Outputs[0].Candidate.Unknown || graph.Outputs[0].Candidate.Value != "" {
		t.Fatalf("outputs = %#v, want one Unknown output", graph.Outputs)
	}
}

func FuzzEmission(f *testing.F) {
	for _, seed := range []string{
		"echo \"" + markerM + "v\"", "echo \"", "echo \t", "Write-Host '", "echo ##vso[task.setvariable variable=m]100%",
		":: a ^\necho " + markerM + "v", "set -x\necho '" + markerM + "$(a)'", "REM x\n@echo off\necho " + markerM + "v",
		"echo '##vso[task.setvariable variable=a b]v'", "#requires\nWrite-Host ‘x’", "echo '" + markerM + "$(nested)'", "echo '" + markerM + "$(fuzzSecret)'",
	} {
		for shell := byte(0); shell < 4; shell++ {
			f.Add(seed, shell, shell&1 == 0, "value")
		}
	}
	f.Fuzz(func(t *testing.T, script string, shell byte, windows bool, variableValue string) {
		const declaredSecret = "S3CR3T-9f3a7c"
		if strings.Contains(script+variableValue, declaredSecret) {
			t.Skip("the input itself holds the secret")
		}
		kinds := []ast.ScriptKind{ast.ScriptCmd, ast.ScriptBash, ast.ScriptPwsh, ast.ScriptPowerShell}
		agentOS := AgentOSLinux
		if windows {
			agentOS = AgentOSWindows
		}
		run := func() ([]Candidate, []string) {
			pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
				&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo '##vso[task.setvariable variable=fuzzSecret;issecret=true]" + declaredSecret + "'")},
				&ast.ScriptStep{Kind: kinds[int(shell)%len(kinds)], Script: scalarNode(script)},
				&ast.ScriptStep{Script: scalarNode("echo consume")},
			)}
			pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{
				{Key: scalarNode("fuzz"), Value: scalarNode(variableValue)},
				{Key: scalarNode("nested"), Value: scalarNode("$(fuzz)")},
			}}}
			collector := diag.NewCollector()
			graph, err := Evaluate(pipeline, Context{AgentOS: agentOS}, collector)
			if err != nil {
				return nil, codes(collector.List())
			}
			var found []Candidate
			for _, binding := range graph.Tables[len(graph.Tables)-1].Bindings {
				found = append(found, binding.Candidates...)
			}
			for _, finding := range collector.List() {
				if strings.Contains(fmt.Sprintf("%+v", finding), declaredSecret) {
					t.Fatalf("the declared secret leaked into a diagnostic: %+v", finding)
				}
			}
			for _, candidate := range found {
				if strings.Contains(candidate.Value, declaredSecret) {
					t.Fatalf("the declared secret leaked into candidate %q", candidate.Name)
				}
				if candidate.Unknown && candidate.Source == SourceLogging && !hasCode(collector.List(), "TRL-VAR-0016") && !hasCode(collector.List(), "TRL-VAR-0017") {
					t.Fatalf("Unknown logging candidate %q without diagnostic", candidate.Name)
				}
			}
			return found, codes(collector.List())
		}
		first, firstCodes := run()
		second, secondCodes := run()
		if len(first) != len(second) || strings.Join(firstCodes, ",") != strings.Join(secondCodes, ",") {
			t.Fatalf("non-deterministic result")
		}
		for i := range first {
			if first[i].Name != second[i].Name || first[i].Value != second[i].Value || first[i].Unknown != second[i].Unknown {
				t.Fatalf("non-deterministic candidate %d", i)
			}
		}
	})
}

// knownMacroRun evaluates a bash step after a variable block and returns the
// effective "m" at the consumer step.
func knownMacroRun(t *testing.T, name, value, script string) (Candidate, bool, []diag.Diagnostic) {
	t.Helper()
	pipeline := &ast.Pipeline{Shape: ast.ShapeSteps, Steps: list[ast.Step](
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode(script)},
		&ast.ScriptStep{Kind: ast.ScriptBash, Script: scalarNode("echo consume")},
	)}
	pipeline.Variables = &ast.Variables{Form: ast.VariablesMap, Map: &ast.Map[*ast.Scalar]{Entries: []ast.MapEntry[*ast.Scalar]{{Key: scalarNode(name), Value: scalarNode(value)}}}}
	collector := diag.NewCollector()
	graph, err := Evaluate(pipeline, Context{AgentOS: AgentOSLinux}, collector)
	if err != nil {
		t.Fatal(err)
	}
	candidate, ok := graph.Resolve(stepScope(1), "m", PhaseMacro)
	return candidate, ok, collector.List()
}

func TestReviewR7KnownMacrosAreNeverExpandedByEmissionAnalysis(t *testing.T) {
	const emit = "echo '" + markerM
	for _, tc := range []struct{ name, script string }{
		{"bare word on a prior line", "echo $(k)\n" + emit + "v'"},
		{"known macro in quoted prior line", "echo \"$(k)\"\n" + emit + "v'"},
		{"known macro in comment", "# $(k)\n" + emit + "v'"},
		{"known macro in the value", emit + "got $(k)'"},
		{"known macro in the header", "echo '##vso[task.setvariable variable=m;issecret=$(k)]v'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, ok, findings := knownMacroRun(t, "k", "10", tc.script)
			if !ok || !candidate.Unknown || candidate.Value != "" || !hasCode(findings, "TRL-VAR-0016") {
				t.Fatalf("m = %#v, %t, %v; want Unknown with TRL-VAR-0016", candidate, ok, codes(findings))
			}
		})
	}
}

func TestReviewR7KnownMacroWithMetacharactersIsNotTrusted(t *testing.T) {
	for _, value := range []string{"a; exit 1", "Fix (parser) bug", "it's", "a\nexit 1"} {
		scripts := []string{"echo $(k)\necho '" + markerM + "v'"}
		if value == "it's" || strings.Contains(value, "\n") {
			// Inside quotes only a quote character or a line break is hostile.
			scripts = append(scripts, "echo \"$(k)\"\necho '"+markerM+"v'", "echo '"+markerM+"$(k)'")
		}
		if strings.Contains(value, "\n") {
			scripts = append(scripts, "# $(k)\necho '"+markerM+"v'")
		}
		for _, script := range scripts {
			candidate, ok, findings := knownMacroRun(t, "k", value, script)
			if !ok || !candidate.Unknown || candidate.Value != "" || !hasCode(findings, "TRL-VAR-0016") {
				t.Fatalf("value %q script %q: m = %#v, %t, %v; want Unknown", value, script, candidate, ok, codes(findings))
			}
		}
	}
}

func TestReviewR7BashBareWord(t *testing.T) {
	for _, tc := range []struct {
		word   string
		proven bool
	}{{"a/b.c:d=e-f_1", true}, {"a;b", false}, {"$HOME", false}, {"a b", false}, {"~x", false}} {
		candidate, ok, _ := emissionRun(t, ast.ScriptBash, AgentOSLinux, "echo "+tc.word+"\necho '"+markerM+"v'")
		if !ok || candidate.Unknown == tc.proven {
			t.Fatalf("word %q: m = %#v, %t; want proven=%t", tc.word, candidate, ok, tc.proven)
		}
	}
}
