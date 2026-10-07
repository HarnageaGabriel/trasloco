package expr

import (
	"errors"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

func expressionError(code string, span Span, message string) *diag.Error {
	return &diag.Error{Finding: diag.MustNew(code,
		diag.WithMessage(message),
		diag.WithSource(diag.Source{Offset: span.Start, EndOffset: span.End}),
		diag.WithParams(map[string]any{"offset": span.Start, "endOffset": span.End}),
	)}
}

func expressionErrorSpan(err error) (Span, bool) {
	var expressionErr *diag.Error
	if !errors.As(err, &expressionErr) || expressionErr.Finding.Source == nil {
		return Span{}, false
	}
	source := expressionErr.Finding.Source
	return Span{Start: source.Offset, End: source.EndOffset}, true
}

func expressionErrorMessage(err error) string {
	var expressionErr *diag.Error
	if errors.As(err, &expressionErr) {
		return expressionErr.Finding.Message
	}
	return err.Error()
}

func syntaxError(offset int, message string) error {
	return expressionError("TRL-EXPR-0001", Span{Start: offset, End: offset}, message)
}
