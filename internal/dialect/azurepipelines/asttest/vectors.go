package asttest

import (
	"os" //nolint:depguard // asttest is test support and loads specification fixtures.
	"path/filepath"
	"strings"
	"testing"
)

// Vector is one STRUCT-* row of docs/spec/ado-yaml-structure.md.
type Vector struct {
	ID    string
	Input string
	// Expected are dump lines that must appear, in this order, in Dump of the parsed tree.
	Expected []string
	// Diagnostics are the exact `CODE@line:col` findings in report order.
	Diagnostics []string
	Source      string
}

// SpecPath returns the path of the structure specification relative to a test
// running in one of the structure packages.
func SpecPath() string {
	return filepath.Join("..", "..", "..", "..", "docs", "spec", "ado-yaml-structure.md")
}

// LoadVectors reads the STRUCT-* table of the specification. It fails the test
// when a row is malformed or has no schema source.
func LoadVectors(t testing.TB) []Vector {
	t.Helper()
	data, err := os.ReadFile(SpecPath())
	if err != nil {
		t.Fatal(err)
	}
	var vectors []Vector
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| STRUCT-") {
			continue
		}
		cells := tableCells(line)
		if len(cells) != 7 {
			t.Fatalf("malformed vector row (%d cells): %.80s", len(cells), line)
		}
		v := Vector{
			ID:     cells[1],
			Input:  strings.ReplaceAll(cells[2], "<br>", "\n"),
			Source: cells[5],
		}
		if cells[3] != "-" {
			v.Expected = strings.Split(cells[3], "<br>")
		}
		if cells[4] != "-" {
			v.Diagnostics = strings.Fields(cells[4])
		}
		if !strings.HasPrefix(v.Source, "https://learn.microsoft.com/en-us/azure/devops/pipelines/") {
			t.Fatalf("vector %s has no schema page source: %q", v.ID, v.Source)
		}
		vectors = append(vectors, v)
	}
	return vectors
}

func tableCells(line string) []string {
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			b.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, cleanCell(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(line[i])
	}
	return append(cells, cleanCell(b.String()))
}

func cleanCell(cell string) string {
	cell = strings.TrimSpace(cell)
	if strings.HasPrefix(cell, "`") {
		if end := strings.LastIndex(cell, "`"); end > 0 {
			return cell[1:end]
		}
	}
	return cell
}
