package main

import (
	"os"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func TestGenerateAndCheck(t *testing.T) {
	dir := t.TempDir()
	if err := run(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, true); err != nil {
		t.Fatalf("fresh output drifted: %v", err)
	}
	entries := diag.List()
	if _, err := os.Stat(dir + "/" + entries[0].Code + ".md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+entries[0].Code+".md", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, true); err == nil {
		t.Fatal("expected drift error")
	}
}
