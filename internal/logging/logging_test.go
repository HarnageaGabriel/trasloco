package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		output string
		env    string
		want   Format
	}{
		{"text", "", FormatText},
		{"json", "", FormatJSON},
		{"jsonl", "", FormatJSON},
		{"text", "JSON", FormatJSON},
	}
	for _, tt := range tests {
		if got := ResolveFormat(tt.output, tt.env); got != tt.want {
			t.Errorf("ResolveFormat(%q, %q) = %q, want %q", tt.output, tt.env, got, tt.want)
		}
	}
}

func TestNewJSON(t *testing.T) {
	var dst bytes.Buffer
	New(&dst, slog.LevelInfo, FormatJSON).Info("hello", "key", "value")
	got := dst.String()
	if !strings.Contains(got, `"msg":"hello"`) || !strings.Contains(got, `"key":"value"`) {
		t.Fatalf("JSON log = %q", got)
	}
}
