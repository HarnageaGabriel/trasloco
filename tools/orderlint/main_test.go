package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "example.com/project/internal/ir")
}

func TestTargetPackage(t *testing.T) {
	tests := map[string]bool{
		"example.com/project/internal/ir":         true,
		"example.com/project/internal/dialect/gh": true,
		"example.com/project/internal/event":      true,
		"example.com/project/internal/naming":     true,
		"example.com/project/report":              true,
		"example.com/project/planner":             false,
		"example.com/project/internal/cache":      false,
	}
	for path, want := range tests {
		if got := targetPackage(path); got != want {
			t.Errorf("targetPackage(%q) = %v, want %v", path, got, want)
		}
	}
}
