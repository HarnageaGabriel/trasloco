package diag

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestRegistryReferences(t *testing.T) {
	refs := make(map[string]bool)
	pattern := regexp.MustCompile(`TRL-[A-Z]+-[0-9]{4}`)
	for _, root := range []string{"internal", "cmd", "docs/spec"} {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || (!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md")) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, code := range pattern.FindAllString(string(data), -1) {
				// The spec and negative tests deliberately demonstrate unknown areas.
				if !ValidCode(code) {
					continue
				}
				if _, ok := Lookup(code); !ok {
					t.Errorf("%s: unregistered diagnostic %s", path, code)
				}
				refs[code] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range List() {
		if !refs[entry.Code] {
			t.Errorf("unreferenced registry entry %s", entry.Code)
		}
	}
}

func TestTypedConstruction(t *testing.T) {
	r := redact.New()
	r.Add("run-secret")
	d := MustNew("TRL-INT-0001", WithRedactor(r), WithMessage("failure run-secret"),
		WithSource(Source{File: "run-secret"}), WithResource(Resource{Key: "run-secret"}),
		WithRemediation("hide run-secret"), WithFidelity(FidelityNeedsManual),
		WithProvenance(ProvenanceLLM), WithArgs(map[string]any{"value": "run-secret"}))
	if strings.Contains(fmt.Sprint(d), "run-secret") {
		t.Fatal("run secret leaked")
	}
	var finding *Error
	if !errors.As(fmt.Errorf("wrapped: %w", &Error{Finding: d}), &finding) || finding.Finding.Code != d.Code {
		t.Fatal("errors.As lost finding")
	}
	if !strings.Contains(finding.Error(), d.Code) {
		t.Fatal("error lost code")
	}
	for _, opts := range [][]Option{{nil}, {WithFidelity("invalid")}, {WithProvenance("invalid")}, {WithSeverity(SeverityInfo)}, {WithParams(map[string]any{"bad": make(chan int)})}} {
		if _, err := New("TRL-INT-0001", opts...); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := New("unregistered"); err == nil {
		t.Fatal("unknown code accepted")
	}
	if _, err := New("TRL-INT-0001", WithSeverity(SeverityError), WithRedactor(nil)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew did not panic")
		}
	}()
	MustNew("unregistered")
}

func TestCollectorStableTiesAndSnapshots(t *testing.T) {
	var c Collector
	a := MustNew("TRL-INT-0001", WithMessage("a"))
	b := MustNew("TRL-INT-0001", WithMessage("b"))
	c.Add(b)
	c.Add(a)
	got := c.List()
	if got[0].Message != "a" {
		t.Fatal("unstable tie order")
	}
	got[0].Message = "mutated"
	if c.List()[0].Message != "a" {
		t.Fatal("snapshot aliased")
	}
	if c.Add(Diagnostic{Params: map[string]any{"bad": make(chan int)}}) {
		t.Fatal("unserializable diagnostic accepted")
	}
}
