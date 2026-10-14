// Package ast defines the native, position-carrying syntax tree of an Azure
// Pipelines YAML definition before any template is expanded.
//
// The tree is produced by the parse package from a yamlx document and consumed
// by the template resolver. Nothing here evaluates an expression: insertion
// directives (`${{ if }}`, `${{ each }}`, `${{ expr }}`) are kept as explicit
// Directive nodes, expression-valued fields keep their raw text next to the
// parsed expression. The normative description is docs/spec/ado-yaml-structure.md.
package ast

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// Node is implemented by every syntax node.
type Node interface {
	// Position returns where the node was written (ADR 0003).
	Position() diag.Source
}

// Pos carries the source position of a node and is embedded in every node.
type Pos struct {
	Source diag.Source
	// Chain is the template-instantiation call-site chain, outermost first.
	// It is empty for nodes written in the root file and mirrors ir.Origin.Chain.
	Chain []diag.Source
}

// Position implements Node.
func (p Pos) Position() diag.Source { return p.Source }

// DirectiveKind identifies a template directive or expression wrapper.
type DirectiveKind uint8

const (
	// DirectiveIf is a `${{ if cond }}` key.
	DirectiveIf DirectiveKind = iota + 1
	// DirectiveElseIf is a `${{ elseif cond }}` key.
	DirectiveElseIf
	// DirectiveElse is a `${{ else }}` key.
	DirectiveElse
	// DirectiveEach is a `${{ each name in expr }}` key.
	DirectiveEach
	// DirectiveInsert is a `${{ expr }}` occurrence inside a scalar or key.
	DirectiveInsert
	// DirectiveRuntime is a `$[ expr ]` runtime expression that fills a scalar.
	DirectiveRuntime
)

// String returns the lower-case directive name.
func (k DirectiveKind) String() string {
	switch k {
	case DirectiveIf:
		return "if"
	case DirectiveElseIf:
		return "elseif"
	case DirectiveElse:
		return "else"
	case DirectiveEach:
		return "each"
	case DirectiveInsert:
		return "insert"
	case DirectiveRuntime:
		return "runtime"
	default:
		return "unknown"
	}
}

// IsKey reports whether the kind can only appear as a mapping key.
func (k DirectiveKind) IsKey() bool {
	return k == DirectiveIf || k == DirectiveElseIf || k == DirectiveElse || k == DirectiveEach
}

// ExprSource maps one node of a parsed expression back to the file. Span is the
// node's own Range() as reported by package expr: relative to the parsed text
// (the Value of an Expression, the Raw of an inserted or runtime Directive, the
// expression part of an if, elseif or each key directive).
type ExprSource struct {
	Span   expr.Span
	Source diag.Source
}

// Directive is a template directive or expression wrapper, never evaluated by
// the parser.
type Directive struct {
	Pos
	Kind DirectiveKind
	// Raw is the directive text as written, including the `${{ }}` or `$[ ]` wrapper.
	Raw string
	// Start and End are the byte range of Raw inside the Value of the scalar that
	// contains it. Key directives span the whole key.
	Start, End int
	// Variable is the loop variable of an each directive.
	Variable string
	// Expr is the parsed condition, iterable or inserted expression. It is nil for
	// else and when the expression has a syntax error (already diagnosed).
	Expr expr.Expr
	// ExprSources maps the nodes of Expr back to file positions.
	ExprSources []ExprSource
}

// SourceOf returns the file position of an expression node span.
func (d *Directive) SourceOf(span expr.Span) (diag.Source, bool) {
	return sourceOf(d.ExprSources, span)
}

func sourceOf(sources []ExprSource, span expr.Span) (diag.Source, bool) {
	for _, entry := range sources {
		if entry.Span == span {
			return entry.Source, true
		}
	}
	return diag.Source{}, false
}

// Scalar is a YAML scalar read through yamlx Scalar() (ADR 0009): its Value is
// the decoded text whatever the YAML tag was.
type Scalar struct {
	Pos
	Value string
	// Null is true when the scalar was written as null, `~` or left empty.
	Null bool
	// Quoted is true when the scalar was written in single or double quotes. The
	// value alone cannot tell `60` from `'60'`; consumers that infer a type from a
	// free-form value use this flag (ADR 0009).
	Quoted bool
	// Directives are the `${{ }}` occurrences (and a whole-value `$[ ]`) found in
	// Value, in order.
	Directives []*Directive
}

// Whole returns the directive that covers the whole (trimmed) value, or nil.
func (s *Scalar) Whole() *Directive {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(s.Value)
	for _, directive := range s.Directives {
		if directive.Raw == trimmed {
			return directive
		}
	}
	return nil
}

// Bool is a scalar that the schema declares boolean.
type Bool struct {
	Scalar
	// Val is the parsed value; it is only meaningful when Known is true.
	Val bool
	// Known is false when the value is a template expression or not a boolean.
	Known bool
}

// Enum is a scalar restricted to a closed, case-insensitive set of values.
type Enum struct {
	Scalar
	// Canon is the canonical spelling of the value, or empty when the value is a
	// template expression or not in the set.
	Canon string
}

// Expression is a scalar holding an Azure Pipelines expression, such as a
// condition. The raw text stays in Scalar.Value.
type Expression struct {
	Scalar
	// Expr is the parsed expression; nil when it contains template expressions
	// (deferred to the resolver) or has a syntax error (already diagnosed).
	Expr expr.Expr
	// ExprSources maps the nodes of Expr to file positions; spans are relative to Value.
	ExprSources []ExprSource
}

// SourceOf returns the file position of an expression node span.
func (e *Expression) SourceOf(span expr.Span) (diag.Source, bool) {
	return sourceOf(e.ExprSources, span)
}

// Insertable is embedded in compound nodes that may be written as one
// `${{ expr }}` value instead of their structure.
type Insertable struct {
	// Insertion is non-nil when the whole value was a template expression; the
	// structural fields of the node are then empty.
	Insertion *Directive
}

// Branch is the body of a key directive inside a typed mapping: a partial node
// of the same type holding the fields the directive inserts.
type Branch[B any] struct {
	Directive *Directive
	Body      B
}

// Tail is embedded in typed mapping nodes. It holds what the typed fields do not.
type Tail[T any] struct {
	// Inserts are the key directives of the mapping, in source order.
	Inserts []*Branch[*T]
	// Extra holds unknown and dynamic keys, in source order (never dropped).
	Extra []*Extra
}

// Extra is a mapping entry the schema does not define, or whose key is dynamic.
type Extra struct {
	Key   *Scalar
	Value *Any
}

// AnyKind identifies the shape of a free-form value.
type AnyKind uint8

const (
	// AnyScalar is a scalar value.
	AnyScalar AnyKind = iota + 1
	// AnySeq is a sequence.
	AnySeq
	// AnyMap is a mapping.
	AnyMap
)

// Any is a free-form YAML value: template parameter defaults and arguments,
// templateContext, and unknown keys. Key directives are explicit entries.
type Any struct {
	Pos
	Kind    AnyKind
	Scalar  *Scalar
	Items   []*Any
	Entries []*AnyEntry
}

// AnyEntry is one mapping entry of an Any. Directive is set when the key is an
// if, elseif, else or each directive; Value is then the directive body.
type AnyEntry struct {
	Pos
	Key       *Scalar
	Directive *Directive
	Value     *Any
}

// List is a YAML sequence of T whose items may be conditional insertions.
type List[T any] struct {
	Pos
	Insertable
	Items []ListItem[T]
}

// ListItem is a value, a conditional/each insertion (Cond), or one whole-value
// `${{ expr }}` item (Insert) that expands to any number of items.
type ListItem[T any] struct {
	Value  T
	Cond   *ListCond[T]
	Insert *Directive
}

// ListCond is a `- ${{ if ... }}:` style list item.
type ListCond[T any] struct {
	Directive *Directive
	// Items is the body when it is a sequence.
	Items *List[T]
	// Other is the body when it is not a sequence (a mapping or a scalar).
	Other *Any
}

// Map is an ordered YAML mapping whose values are V.
type Map[V any] struct {
	Pos
	Insertable
	Entries []MapEntry[V]
}

// MapEntry is either a key/value pair (Cond == nil) or a key directive.
type MapEntry[V any] struct {
	Key   *Scalar
	Value V
	Cond  *MapCond[V]
}

// MapCond is a key directive inside a Map; Body holds the inserted entries.
type MapCond[V any] struct {
	Directive *Directive
	Body      *Map[V]
}

// SetPos replaces the node position; it lets generic code position any node.
func (p *Pos) SetPos(v Pos) { *p = v }

// SetInsertion marks the node as one whole-value template expression.
func (i *Insertable) SetInsertion(d *Directive) { i.Insertion = d }
