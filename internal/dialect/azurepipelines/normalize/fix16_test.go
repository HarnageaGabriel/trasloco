package normalize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// B1: a declared decimal secret must not reappear as the canonical number text
// the expression lexer builds (leading zeros, trailing zeros, dot forms).
func TestDeclaredDecimalCanonicalFormsDoNotReachNumberLiterals(t *testing.T) {
	shapes := []struct{ declared, canonical string }{
		{"007421", "7421"},
		{"0042", "42"},
		{"00123", "123"},
		{"0123456789", "123456789"},
		{"1.50", "1.5"},
		{"3.14000", "3.14"},
		{"1.0", "1"},
		{".5", "0.5"},
		{"5.", "5"},
		{"-0.0", "0"},
	}
	for _, shape := range shapes {
		t.Run(shape.declared, func(t *testing.T) {
			got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \""+shape.declared+"\"\n"+
				"steps:\n- script: echo hi\n  condition: eq(variables.a, ${{ parameters.dbPassword }})\n")
			out := requireAbsent(t, got, collector, `"number":"`+shape.canonical+`"`)
			if findings := ir.Validate(got); len(findings) != 0 {
				t.Fatalf("IR validation: %v\n%s", findings, out)
			}
		})
	}
}

func TestCanonicalDecimal(t *testing.T) {
	for in, want := range map[string]string{ //trasloco:unordered-ok independent assertions
		"007421": "7421", "1.50": "1.5", ".5": "0.5", "5.": "5", "-0.0": "0", "-00.50": "-0.5", "0": "0", "000": "0",
	} {
		if got, ok := canonicalDecimal(in); !ok || got != want {
			t.Errorf("canonicalDecimal(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", ".", "-", "1e3", "abc", "1.2.3", "+"} {
		if got, ok := canonicalDecimal(in); ok {
			t.Errorf("canonicalDecimal(%q) = %q, want not decimal", in, got)
		}
	}
}

// B2: composites built by the normalizer from a user value are made of
// scrubbed components.
func TestDeclaredSecretComponentsScrubbedBeforeComposition(t *testing.T) {
	const secret = "123456"
	cases := map[string]string{
		"exclude glob": "trigger:\n  branches:\n    exclude:\n    - ${{ parameters.dbPassword }}\nsteps:\n- script: echo hi\n",
		"stage and job": "stages:\n- stage: ${{ parameters.dbPassword }}\n  jobs:\n  - job: ${{ parameters.dbPassword }}\n    steps:\n    - script: echo hi\n" +
			"  - job: other\n    dependsOn: ${{ parameters.dbPassword }}\n    steps:\n    - script: echo hi\n" +
			"- stage: two\n  dependsOn: ${{ parameters.dbPassword }}\n  jobs:\n  - job: j\n    steps:\n    - script: echo hi\n",
		"unknown key": "jobs:\n- job: a\n  ${{ parameters.dbPassword }}: x\n  steps:\n  - script: echo hi\n",
		"step ids":    "steps:\n- script: echo a\n  name: ${{ parameters.dbPassword }}\n- script: echo b\n  name: ${{ parameters.dbPassword }}\n",
		"resource triggers": "resources:\n  webhooks:\n  - webhook: ${{ parameters.dbPassword }}\n    connection: c\n" +
			"  pipelines:\n  - pipeline: ${{ parameters.dbPassword }}\n    source: up\n    trigger: true\n" +
			"steps:\n- script: echo hi\n",
	}
	for name, body := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \""+secret+"\"\n"+body)
			requireAbsent(t, got, collector, secret)
		})
	}
}

// F1: numeric secrets of six or more characters are also replaced inside
// longer text; shorter ones stay whole-leaf only (documented limit).
func TestLongNumericSecretReplacedInsideText(t *testing.T) {
	body := "steps:\n- script: echo 123456\n- task: Foo@1\n  inputs:\n    arguments: --label 123456\n" +
		"- script: echo 12345\n"
	got, collector := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \"123456\"\n"+body)
	out := requireAbsent(t, got, collector, "123456")
	if !strings.Contains(out, "echo 12345") {
		t.Fatalf("short numeric text should be untouched:\n%s", out)
	}
	short, _ := resolvedPipeline(t, "parameters:\n- name: dbPassword\n  type: string\n  default: \"12345\"\nsteps:\n- script: echo 12345\n")
	if !strings.Contains(canonical(t, short), "echo 12345") {
		t.Fatal("5-digit numeric secret must remain whole-leaf only")
	}
}

// F3: IDs collapsed by the built-in credential scrubber alone are
// deduplicated.
func TestCredentialCollapsedIDsAreDeduplicated(t *testing.T) {
	a := "ghp_" + strings.Repeat("a", 36)
	b := "ghp_" + strings.Repeat("b", 36)
	cases := map[string]string{
		"jobs":   "jobs:\n- job: " + a + "\n  steps:\n  - script: echo hi\n- job: " + b + "\n  steps:\n  - script: echo hi\n",
		"steps":  "steps:\n- script: echo hi\n  name: " + a + "\n- script: echo hi\n  name: " + b + "\n",
		"stages": "stages:\n- stage: " + a + "\n  jobs:\n  - job: j\n    steps:\n    - script: echo hi\n- stage: " + b + "\n  jobs:\n  - job: j\n    steps:\n    - script: echo hi\n",
		"params": "parameters:\n- name: " + a + "\n  type: string\n- name: " + b + "\n  type: string\nsteps:\n- script: echo hi\n",
		"matrix": "jobs:\n- job: m\n  strategy:\n    matrix:\n      " + a + ":\n        x: '1'\n      " + b + ":\n        x: '2'\n  steps:\n  - script: echo hi\n",
	}
	for name, text := range cases { //trasloco:unordered-ok independent subtests
		t.Run(name, func(t *testing.T) {
			got, collector := resolvedPipeline(t, text)
			requireAbsent(t, got, collector, a, b)
			if findings := ir.Validate(got); len(findings) != 0 {
				t.Fatalf("IR validation: %v", findings)
			}
		})
	}
}

// useSiteSecretForms lists the spellings that must not survive for secret.
func useSiteSecretForms(secret string) (forms []string) {
	for _, value := range []string{secret, strings.TrimSpace(secret)} {
		forms = append(forms, value, strings.ToLower(value), strings.ToUpper(value),
			jsonEscaped(value, true), jsonEscaped(value, false))
	}
	if canon, ok := canonicalDecimal(strings.TrimSpace(secret)); ok {
		forms = append(forms, `"number":"`+canon+`"`)
	}
	return forms
}

// strongSecret reports the values replaced inside longer text: non-numeric
// values of four or more characters and decimals of six or more.
func strongSecret(secret string) bool {
	trimmed := strings.TrimSpace(secret)
	if numericText(trimmed) {
		return len(trimmed) >= minNumericSubstring
	}
	return len(secret) >= minTaintBytes
}

// F4: the injected parameter is referenced at many use sites so the shape
// runs prove the declared value never reaches the IR or the diagnostics.
func TestDeclaredSecretShapesAtUseSites(t *testing.T) {
	const ref = "${{ parameters.__corpusPassword }}"
	whole := "trigger:\n  branches:\n    exclude:\n    - " + ref + "\n" +
		"stages:\n- stage: " + ref + "\n  displayName: " + ref + "\n  jobs:\n  - job: " + ref + "\n    displayName: " + ref + "\n" +
		"    " + ref + ": unknownValue\n" +
		"    container:\n      image: ubuntu\n      options: " + ref + "\n" +
		"    steps:\n    - script: " + ref + "\n      name: " + ref + "\n      condition: eq(variables.a, @COND@)\n" +
		"      env:\n        TOKEN: " + ref + "\n" +
		"  - job: other\n    dependsOn: " + ref + "\n    steps:\n    - script: echo hi\n"
	embedded := "    - script: echo " + ref + " done\n" +
		"    - task: Foo@1\n      inputs:\n        arguments: --label " + ref + "\n"
	shapes := []string{
		"-12.5", "1e3", "Qz9", " 42.5 ", "007421", "3.14000", "0042", "1.50", "0.50", "00123", "1234567",
		"hunter2pass", "0123456789",
	}
	for _, secret := range append(shapes, strings.Repeat("S", maxTaintSourceBytes+1)) {
		encoded, _ := json.Marshal(secret)
		text := "parameters:\n- name: __corpusPassword\n  type: string\n  default: " + string(encoded) + "\n" + whole
		// A bare decimal becomes a number literal in the condition; any other
		// value must be quoted to be a valid expression.
		operand := "'" + ref + "'"
		if m := plainDecimal.FindStringSubmatch(secret); m != nil && m[1] == "" && (m[2] != "" || m[3] != "") {
			operand = ref
		}
		if len(secret) > maxTaintSourceBytes {
			// The overflow value exceeds the expression size bound.
			text = strings.Replace(text, "      condition: eq(variables.a, @COND@)\n", "", 1)
		}
		text = strings.ReplaceAll(text, "@COND@", operand)
		if strongSecret(secret) {
			text = strings.Replace(text, "  - job: other\n", "  - job: embedded\n    steps:\n"+embedded+"  - job: other\n", 1)
		}
		pre := diag.NewCollector()
		if parse.Source("main.yml", []byte(text), pre); len(pre.List()) != 0 {
			t.Fatalf("secret %q: parse findings %v\n%s", shortSecret(secret), pre.List(), text)
		}
		got, collector := resolvedPipeline(t, text)
		irBytes, err := ir.Canonical(got)
		if err != nil {
			t.Fatal(err)
		}
		diagnostics := diagnosticsJSON(t, got, collector)
		for _, form := range useSiteSecretForms(secret) {
			if form == "" || strings.TrimSpace(form) == "" {
				continue
			}
			if strings.Contains(string(irBytes), form) || strings.Contains(diagnostics, form) {
				t.Fatalf("secret %q: form %q leaked", shortSecret(secret), shortSecret(form))
			}
		}
		if !strings.Contains(string(irBytes), "[REDACTED]") {
			t.Fatalf("secret %q: no placeholder, the use sites were not exercised", shortSecret(secret))
		}
		if findings := ir.Validate(got); len(findings) != 0 {
			t.Fatalf("secret %q: IR validation: %v", shortSecret(secret), findings)
		}
	}
}
