package redact

import (
	"context"
	"fmt"
	"log/slog"
)

type slogHandler struct {
	next     slog.Handler
	redactor *Redactor
}

// NewSlogHandler wraps a handler and redacts record messages and attributes.
func NewSlogHandler(next slog.Handler, redactor *Redactor) slog.Handler {
	if redactor == nil {
		redactor = New()
	}
	return &slogHandler{next: next, redactor: redactor}
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.redactor.Redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		clean[i] = h.redactAttr(attr)
	}
	return &slogHandler{next: h.next.WithAttrs(clean), redactor: h.redactor}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	return &slogHandler{next: h.next.WithGroup(name), redactor: h.redactor}
}

func (h *slogHandler) redactAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(h.redactor.Redact(value.String()))
	case slog.KindGroup:
		group := value.Group()
		for i := range group {
			group[i] = h.redactAttr(group[i])
		}
		attr.Value = slog.GroupValue(group...)
	case slog.KindAny:
		attr.Value = slog.StringValue(h.redactor.Redact(fmt.Sprint(value.Any())))
	}
	return attr
}
