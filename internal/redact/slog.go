package redact

import (
	"context"
	"fmt"
	"log/slog"
)

type slogHandler struct {
	next     slog.Handler
	redactor *Redactor
	bindings []slogBinding
}

type slogBinding struct {
	group string
	attrs []slog.Attr
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
	next := h.next
	for _, binding := range h.bindings {
		if binding.attrs != nil {
			attrs := make([]slog.Attr, len(binding.attrs))
			for i, attr := range binding.attrs {
				attrs[i] = h.redactAttr(attr)
			}
			next = next.WithAttrs(attrs)
		} else {
			next = next.WithGroup(h.redactor.Redact(binding.group))
		}
	}
	return next.Handle(ctx, clean)
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bindings := append([]slogBinding(nil), h.bindings...)
	bindings = append(bindings, slogBinding{attrs: append([]slog.Attr{}, attrs...)})
	return &slogHandler{next: h.next, redactor: h.redactor, bindings: bindings}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	bindings := append([]slogBinding(nil), h.bindings...)
	bindings = append(bindings, slogBinding{group: name})
	return &slogHandler{next: h.next, redactor: h.redactor, bindings: bindings}
}

func (h *slogHandler) redactAttr(attr slog.Attr) slog.Attr {
	attr.Key = h.redactor.Redact(attr.Key)
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(h.redactor.Redact(value.String()))
	case slog.KindGroup:
		group := append([]slog.Attr(nil), value.Group()...)
		for i := range group {
			group[i] = h.redactAttr(group[i])
		}
		attr.Value = slog.GroupValue(group...)
	case slog.KindAny:
		attr.Value = slog.StringValue(h.redactor.Redact(fmt.Sprint(value.Any())))
	}
	return attr
}
