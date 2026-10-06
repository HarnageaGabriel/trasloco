package diag

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestDiagnosticVectors(t *testing.T) {
	t.Run("DIAG-001", func(t *testing.T) {
		if !ValidCode("TRL-NAME-0002") {
			t.Fatal("valid code rejected")
		}
	})
	t.Run("DIAG-002", func(t *testing.T) {
		if ValidCode("TRL-RLY-0001") {
			t.Fatal("invalid prefix accepted")
		}
	})
	t.Run("DIAG-003", func(t *testing.T) {
		if ValidCode("TRL-NAME-2") {
			t.Fatal("invalid digits accepted")
		}
	})
	t.Run("DIAG-004", func(t *testing.T) {
		if ValidCode("TRL-XXX-0001") {
			t.Fatal("unknown area accepted")
		}
	})
	t.Run("DIAG-005", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "diag", "retired-missing-replaced.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRegistry(data); err == nil {
			t.Fatal("missing replacedBy accepted")
		}
	})
	t.Run("DIAG-006", func(t *testing.T) {
		if _, err := New("TRL-PLAN-0001", WithSeverity(SeverityInfo)); err == nil {
			t.Fatal("forbidden severity override accepted")
		}
	})
	t.Run("DIAG-007", func(t *testing.T) {
		collector := NewCollector()
		d10, _ := New("TRL-INT-0001", WithSource(Source{File: "a.yaml", Line: 10}))
		d3, _ := New("TRL-INT-0001", WithSource(Source{File: "a.yaml", Line: 3}))
		collector.Add(d10)
		collector.Add(d3)
		if got := collector.List(); got[0].Source.Line != 3 || got[1].Source.Line != 10 {
			t.Fatalf("wrong order: %#v", got)
		}
	})
	t.Run("DIAG-008", func(t *testing.T) {
		collector := NewCollector()
		without, _ := New("TRL-INT-0001")
		with, _ := New("TRL-INT-0001", WithSource(Source{File: "z.yaml"}))
		collector.Add(without)
		collector.Add(with)
		if collector.List()[0].Source == nil {
			t.Fatal("diagnostic without source did not sort last")
		}
	})
	t.Run("DIAG-009", func(t *testing.T) {
		d, err := New("TRL-VAR-0003")
		if err != nil {
			t.Fatal(err)
		}
		want := "https://github.com/HarnageaGabriel/trasloco/blob/main/docs/diagnostics/TRL-VAR-0003.md"
		if d.DocsURL != want {
			t.Fatalf("DocsURL = %q, want %q", d.DocsURL, want)
		}
	})
	t.Run("DIAG-010", func(t *testing.T) {
		r := redact.New()
		r.Add("canary-secret")
		d, err := New("TRL-INT-0001", WithMessage("failed: canary-secret"), WithRedactor(r))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(d.Message, "canary-secret") {
			t.Fatalf("secret leaked: %q", d.Message)
		}
	})
}

func TestAllSpecCodesExist(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "spec", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`TRL-[A-Z]+-[0-9]{4}`)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range pattern.FindAllString(string(data), -1) {
			if !ValidCode(code) {
				continue
			}
			if _, ok := Lookup(code); !ok {
				t.Errorf("%s references missing registry code %s", filepath.Base(file), code)
			}
		}
	}
}

func TestCollectorDedupAndCap(t *testing.T) {
	collector := NewCollector(1)
	first, _ := New("TRL-INT-0001", WithMessage("first"))
	second, _ := New("TRL-INT-0001", WithMessage("second"))
	if !collector.Add(first) || collector.Add(first) || collector.Add(second) {
		t.Fatal("deduplication/cap contract violated")
	}
	if collector.Suppressed()["TRL-INT-0001"] != 1 {
		t.Fatalf("Suppressed() = %#v", collector.Suppressed())
	}
}
