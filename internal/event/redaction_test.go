package event

import (
	"errors"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestRedactionFindingIsFresh(t *testing.T) {
	first := ErrRedaction()
	first.Finding.Code = "modified"
	second := ErrRedaction()
	var finding *diag.Error
	if first == second || !errors.As(second, &finding) || finding.Finding.Code != "TRL-SECRET-0004" {
		t.Fatal("finding shared mutable state")
	}
}

func TestSignedURLsInEventMessage(t *testing.T) {
	event := baseEvent(0, ProgressTick, map[string]any{"completed": 1, "unit": "items", "extensions": map[string]any{
		"error":   `Get "https://example.test/?se=canary-expiry&sig=canary-signature&sp=canary-permission": failed`,
		"details": []any{"https://example.test/?a=keep&X-Amz-Credential=canary-credential&X-Goog-Signature=canary-signature", "glpat-abcdefghijklmnopqrst"},
	}})
	clean, err := sanitizeEvent(event, redact.New())
	if err != nil || strings.Contains(string(clean.Payload), "canary-") || strings.Contains(string(clean.Payload), "glpat-") {
		t.Fatalf("event = %s, %v", clean.Payload, err)
	}
}
