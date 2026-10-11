package normalize

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/resolve"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
)

var (
	secretKeyLine = regexp.MustCompile(`(?im)^\s*(?:-\s*)?([\w.-]*(?:password|secret|token|apikey|api_key|passwd|pwd)[\w.-]*)\s*:\s*(\S.*?)\s*$`)
	// secretNamePair matches `- name: <secret-classified>` followed (possibly
	// after other properties) by its `default:` or `value:` line.
	secretNamePair = regexp.MustCompile(`(?im)^[ \t]*-[ \t]*name:[ \t]*([\w.-]*(?:password|secret|token|apikey|api_key|passwd|pwd)[\w.-]*)[ \t]*\r?\n(?:[ \t]+[\w-]+:[^\n]*\n)*?[ \t]+(?:default|value):[ \t]*(\S[^\r\n]*?)[ \t]*\r?$`)
	secretLogging  = regexp.MustCompile(`(?i)##vso\[task\.setsecret\]([^"'\s]+)|issecret=true\]([^"'\s]+)`)
)

// TestNoSecretLiteralInAnyGolden collects every literal secret-looking value
// written anywhere in the corpus (including template repositories) and
// requires that none appears in any canonical IR golden.
func TestNoSecretLiteralInAnyGolden(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	literals := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yml") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		text := string(data)
		for _, m := range secretKeyLine.FindAllStringSubmatch(text, -1) {
			key := strings.ToLower(m[1])
			value := strings.Trim(strings.TrimSpace(m[2]), `"'`)
			// Name/filter/file keys list secret names, not secret values.
			if strings.Contains(key, "filter") || strings.Contains(key, "name") || strings.Contains(key, "file") {
				continue
			}
			if candidateSecret(value) {
				literals[value] = p
			}
		}
		for _, m := range secretNamePair.FindAllStringSubmatch(text, -1) {
			key := strings.ToLower(m[1])
			value := strings.Trim(strings.TrimSpace(m[2]), `"'`)
			if strings.Contains(key, "filter") || strings.Contains(key, "name") || strings.Contains(key, "file") {
				continue
			}
			if candidateSecret(value) {
				literals[value] = p
			}
		}
		for _, m := range secretLogging.FindAllStringSubmatch(text, -1) {
			value := m[1] + m[2]
			if candidateSecret(value) {
				literals[value] = p
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(literals) == 0 {
		t.Fatal("no secret-looking literals found in the corpus; the guard is vacuous")
	}
	goldens := 0
	err = filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ir.json") {
			return err
		}
		goldens++
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, value := range slices.Sorted(maps.Keys(literals)) {
			origin := literals[value]
			if containsToken(string(data), value) {
				t.Errorf("golden %s contains secret-looking literal %q written in %s", filepath.ToSlash(p), value, filepath.ToSlash(origin))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if goldens != normalizedCount {
		t.Fatalf("scanned %d goldens, want %d", goldens, normalizedCount)
	}
}

// containsToken reports whether value occurs in text delimited by non-word
// characters, so a literal "example" does not match "contoso.example".
func containsToken(text, value string) bool {
	word := func(b byte) bool {
		return b == '_' || b == '.' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	for from := 0; ; {
		i := strings.Index(text[from:], value)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(value)
		if (start == 0 || !word(text[start-1])) && (end == len(text) || !word(text[end])) {
			return true
		}
		from = start + 1
	}
}

func candidateSecret(value string) bool {
	switch {
	case len(value) < 5:
		return false
	case strings.ContainsAny(value[:1], "$[{|>&*!#"):
		return false
	case strings.Contains(value, "$(") || strings.Contains(value, "${{") || strings.Contains(value, "$["):
		return false
	}
	switch strings.ToLower(value) {
	// "example" is an English word that also appears in diagnostic prose.
	case "true", "false", "string", "number", "boolean", "object", "parameters", "example":
		return false
	}
	return !strings.ContainsAny(value, " \t")
}

// TestResolvedProvenanceChain checks that the resolver call-site chain of a
// template-produced node reaches the IR origin (ADR 0003), while nodes written
// in the root file carry none.
func TestResolvedProvenanceChain(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("main.yml", "steps:\n- script: echo root\n- template: inner.yml\n")
	write("inner.yml", "steps:\n- template: leaf.yml\n")
	write("leaf.yml", "steps:\n- script: echo leaf\n  condition: eq(variables.a, 'b')\n")
	source, err := localfs.New(map[string]string{"": dir})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	collector := diag.NewCollector()
	parsed := parse.Source("main.yml", data, collector)
	resolved, err := resolve.Resolve(context.Background(), parsed, source, resolve.Options{Collector: collector})
	if err != nil || resolved == nil {
		t.Fatalf("resolve: %v %v", resolved, err)
	}
	got, err := Pipeline(context.Background(), resolved, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if findings := ir.Validate(got); len(findings) != 0 {
		t.Fatalf("IR validation: %v", findings)
	}
	var root, leaf *ir.Step
	for i := range got.Jobs[0].Steps {
		step := &got.Jobs[0].Steps[i]
		if step.Origin == nil {
			continue
		}
		switch step.Origin.Source.File {
		case "main.yml":
			if step.Origin.Source.Line == 2 {
				root = step
			}
		case "leaf.yml":
			leaf = step
		}
	}
	if root == nil || leaf == nil {
		t.Fatalf("steps not found: root=%v leaf=%v", root, leaf)
	}
	if len(root.Origin.Chain) != 0 {
		t.Fatalf("root step chain = %v", root.Origin.Chain)
	}
	chain := leaf.Origin.Chain
	if len(chain) != 2 || chain[0].File != "main.yml" || chain[0].Line != 3 || chain[1].File != "inner.yml" || chain[1].Line != 2 {
		t.Fatalf("leaf chain = %+v", chain)
	}
	for _, link := range chain {
		if link.Platform != Platform {
			t.Fatalf("chain link platform = %q", link.Platform)
		}
	}
}

// TestUnresolvedGuardStillRejects keeps the ErrNeedsResolver guard honest for
// every template construct: template references and extends.
func TestUnresolvedGuardStillRejects(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"template", "steps:\n- template: x.yml\n"},
		{"extends", "extends:\n  template: x.yml\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parse.Source("p.yml", []byte(tc.text), diag.NewCollector())
			if _, err := Pipeline(context.Background(), parsed, Options{}); err == nil {
				t.Fatal("unresolved pipeline normalized")
			}
		})
	}
}
