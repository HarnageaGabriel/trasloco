// Package logging configures the process-wide structured logger.
//
// Logs are always written to the supplied writer (stderr in production) and
// never to stdout, which is reserved for command results.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// Format selects the log encoding.
type Format string

// Supported log formats.
const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseLevel converts a level name (debug, info, warn, error) to a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", s)
	}
}

// New builds a logger writing to w using the given level and format.
func New(w io.Writer, level slog.Level, format Format, redactors ...*redact.Redactor) *slog.Logger {
	var r *redact.Redactor
	if len(redactors) > 0 {
		r = redactors[0]
	}
	opts := &slog.HandlerOptions{Level: level}
	if format == FormatJSON {
		return slog.New(redact.NewSlogHandler(slog.NewJSONHandler(w, opts), r))
	}
	return slog.New(redact.NewSlogHandler(slog.NewTextHandler(w, opts), r))
}

// ResolveFormat picks the log format: JSON when the output mode is json or
// jsonl, or when envFormat (TRASLOCO_LOG_FORMAT) is "json"; text otherwise.
func ResolveFormat(outputMode, envFormat string) Format {
	switch outputMode {
	case "json", "jsonl":
		return FormatJSON
	}
	if strings.EqualFold(strings.TrimSpace(envFormat), "json") {
		return FormatJSON
	}
	return FormatText
}
