package ir

import (
	"encoding/json"
	"fmt"
)

// DebugString renders stable indented JSON. The IR contains secret references,
// never secret values, so the result is safe when the input obeys the contract.
func DebugString(p *Pipeline) (string, error) {
	encoded, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render IR: %w", err)
	}
	return string(encoded), nil
}
