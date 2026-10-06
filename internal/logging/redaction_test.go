package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestRunRedactor(t *testing.T) {
	for _, format := range []Format{FormatText, FormatJSON} {
		var dst bytes.Buffer
		r := redact.New()
		r.Add("logging-secret")
		New(&dst, slog.LevelInfo, format, r).Info("logging-secret", "value", "logging-secret")
		if strings.Contains(dst.String(), "logging-secret") {
			t.Fatal("secret reached logging sink")
		}
	}
	for _, level := range []string{"debug", "info", "", "warn", "warning", "error"} {
		if _, err := ParseLevel(level); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ParseLevel("invalid"); err == nil {
		t.Fatal("invalid level accepted")
	}
}
