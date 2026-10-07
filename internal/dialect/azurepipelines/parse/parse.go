// Package parse turns a yamlx document into the Azure Pipelines syntax tree of
// package ast. Templates are not expanded: insertion directives are preserved
// as explicit Directive nodes and expression-valued fields keep their raw text
// next to the parsed expression.
//
// Every finding is a registered diagnostic added to a *diag.Collector
// (ADR 0002); the tree is built on a best-effort basis so that one problem does
// not hide the rest. The normative rules are in docs/spec/ado-yaml-structure.md.
package parse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// Source parses Azure Pipelines YAML text. Anchors, aliases and merge keys are
// reported as errors (ADR 0009). Diagnostics from the YAML layer and from the
// structure parser are added to c.
func Source(name string, data []byte, c *diag.Collector) *ast.Pipeline {
	document, diagnostics := yamlx.ParseWithOptions(name, data, yamlx.WithAnchorPolicy(yamlx.AnchorError))
	for _, diagnostic := range diagnostics {
		c.Add(diagnostic)
	}
	if document == nil {
		return &ast.Pipeline{Pos: ast.Pos{Source: diag.Source{File: name, Line: 1, Col: 1}}}
	}
	return Document(document, c)
}

// Document parses the first document of a YAML stream. Findings of the YAML
// layer itself (syntax, duplicate keys, anchors) must be reported by whoever
// called yamlx; aliases reached through the tree are additionally reported
// here. More than one document in the stream is an error.
func Document(document *yamlx.Document, c *diag.Collector) *ast.Pipeline {
	p := &parser{c: c, limit: yamlx.DefaultDepthLimit}
	root := document.Root()
	if root == nil {
		return &ast.Pipeline{Pos: ast.Pos{Source: diag.Source{File: document.Name, Line: 1, Col: 1}}}
	}
	for _, extra := range document.Documents[1:] {
		p.add("TRL-PARSE-0006", extra.Position(), "only one YAML document is allowed in a pipeline file",
			map[string]any{"actual": "multiple documents", "expected": "single document"})
	}
	return p.pipeline(root)
}

type parser struct {
	c     *diag.Collector
	depth int
	limit int
}

func (p *parser) add(code string, source diag.Source, message string, params map[string]any) {
	options := []diag.Option{diag.WithSource(source)}
	if message != "" {
		options = append(options, diag.WithMessage(message))
	}
	if params != nil {
		options = append(options, diag.WithParams(params))
	}
	p.c.Add(diag.MustNew(code, options...))
}

func (p *parser) addAll(diagnostics []diag.Diagnostic) {
	for _, diagnostic := range diagnostics {
		p.c.Add(diagnostic)
	}
}

func pos(n *yamlx.Node) ast.Pos { return ast.Pos{Source: n.Position()} }

func kindName(n *yamlx.Node) string { return n.Kind().String() }

func (p *parser) typeError(n *yamlx.Node, expected, ctx string) {
	actual := kindName(n)
	p.add("TRL-PARSE-0006", n.Position(), fmt.Sprintf("%s: expected %s, found %s", ctx, expected, actual),
		map[string]any{"actual": actual, "expected": expected})
}

// visit applies the checks every parsed node goes through: aliases are not
// allowed (ADR 0009) and nesting is bounded. It returns false when the node
// must not be descended into; callers that return true must call leave.
func (p *parser) visit(n *yamlx.Node) bool {
	if n == nil {
		return false
	}
	if p.depth >= p.limit {
		p.add("TRL-PARSE-0010", n.Position(), "", map[string]any{"limit": p.limit})
		return false
	}
	if n.Position() != n.Provenance() {
		p.add("TRL-PARSE-0008", n.Position(), "", nil)
	}
	p.depth++
	return true
}

func (p *parser) leave() { p.depth-- }

// entry is one key/value pair of a mapping under inspection.
type entry struct {
	key   *yamlx.Node
	name  string
	value *yamlx.Node
	used  bool
	// dir is the key directive kind, or zero for an ordinary key.
	dir ast.DirectiveKind
}

func (e *entry) dynamic() bool { return e.dir == 0 && strings.Contains(e.name, "${{") }

// mapping is a YAML mapping being consumed field by field. Keys are matched
// case-insensitively; whatever is not consumed is preserved as Extra.
type mapping struct {
	p       *parser
	node    *yamlx.Node
	ctx     string
	entries []*entry
}

// open prepares a mapping for field extraction. It returns nil, after a
// diagnostic, when n is not a mapping. A non-nil result must be closed.
func (p *parser) open(n *yamlx.Node, ctx string) *mapping {
	if n == nil || !p.visit(n) {
		return nil
	}
	if n.Kind() != yamlx.MappingKind {
		p.typeError(n, "mapping", ctx)
		p.leave()
		return nil
	}
	pairs, diagnostics := n.Map()
	if len(diagnostics) > 0 {
		p.addAll(diagnostics)
		p.leave()
		return nil
	}
	m := &mapping{p: p, node: n, ctx: ctx}
	seen := make(map[string]*entry, len(pairs))
	for _, pair := range pairs {
		name, problems := pair.Key.Scalar()
		if len(problems) > 0 {
			p.typeError(pair.Key, "scalar key", ctx)
			continue
		}
		e := &entry{key: pair.Key, name: name, value: pair.Value}
		if kind, ok := classifyKey(name); ok {
			e.dir = kind
		} else if !e.dynamic() {
			lower := strings.ToLower(name)
			if first, duplicate := seen[lower]; duplicate {
				// Exact duplicates were reported by the YAML layer; keys that only
				// differ in case are duplicates for this dialect.
				e.used = true
				if first.name != name {
					p.add("TRL-PARSE-0003", pair.Key.Position(),
						fmt.Sprintf("duplicate mapping key %q (keys are case-insensitive); first defined at %d:%d as %q",
							name, first.key.Line(), first.key.Column(), first.name),
						map[string]any{"key": name, "firstLine": first.key.Line(), "firstCol": first.key.Column()})
				}
				continue
			}
			seen[lower] = e
		}
		m.entries = append(m.entries, e)
	}
	return m
}

func (m *mapping) close() { m.p.leave() }

// take returns the first unconsumed entry for name (case-insensitive) and marks it consumed.
func (m *mapping) take(name string) *entry {
	for _, e := range m.entries {
		if !e.used && e.dir == 0 && strings.EqualFold(e.name, name) {
			e.used = true
			return e
		}
	}
	return nil
}

func (m *mapping) str(name string) *ast.Scalar {
	if e := m.take(name); e != nil {
		return m.p.scalar(e.value, e.name)
	}
	return nil
}

func (m *mapping) boolean(name string) *ast.Bool {
	if e := m.take(name); e != nil {
		return m.p.boolean(e.value, e.name)
	}
	return nil
}

func (m *mapping) enum(name string, allowed ...string) *ast.Enum {
	if e := m.take(name); e != nil {
		return m.p.enum(e.value, e.name, allowed)
	}
	return nil
}

func (m *mapping) expression(name string) *ast.Expression {
	if e := m.take(name); e != nil {
		return m.p.expression(e.value, e.name)
	}
	return nil
}

func (m *mapping) free(name string) *ast.Any {
	if e := m.take(name); e != nil {
		return m.p.free(e.value)
	}
	return nil
}

func (m *mapping) strList(name string) *ast.List[*ast.Scalar] {
	if e := m.take(name); e != nil {
		return m.p.scalarList(e.value, e.name)
	}
	return nil
}

func (m *mapping) strMap(name string) *ast.Map[*ast.Scalar] {
	if e := m.take(name); e != nil {
		return m.p.stringMap(e.value, e.name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scalars, booleans, enums and expressions.

// classifyKey recognizes `${{ if }}`, `${{ elseif }}`, `${{ else }}` and
// `${{ each }}` mapping keys. Other `${{ }}` keys are dynamic keys.
func classifyKey(name string) (ast.DirectiveKind, bool) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") || len(trimmed) < 5 {
		return 0, false
	}
	fields := strings.Fields(trimmed[3 : len(trimmed)-2])
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "if":
		return ast.DirectiveIf, true
	case "elseif":
		return ast.DirectiveElseIf, true
	case "else":
		return ast.DirectiveElse, true
	case "each":
		return ast.DirectiveEach, true
	}
	return 0, false
}

func (p *parser) scalarValue(n *yamlx.Node, ctx string) (string, bool) {
	if n == nil || !p.visit(n) {
		return "", false
	}
	defer p.leave()
	if n.Kind() != yamlx.ScalarKind {
		p.typeError(n, "scalar", ctx)
		return "", false
	}
	value, problems := n.Scalar()
	if len(problems) > 0 {
		p.addAll(problems)
		return "", false
	}
	return value, true
}

// scalar reads a scalar through yamlx Scalar() (ADR 0009) and records the
// template expressions it contains.
func (p *parser) scalar(n *yamlx.Node, ctx string) *ast.Scalar {
	value, ok := p.scalarValue(n, ctx)
	if !ok {
		return nil
	}
	s := &ast.Scalar{Pos: pos(n), Value: value, Null: n.Tag() == "!!null"}
	if strings.Contains(value, "${{") || strings.HasPrefix(strings.TrimSpace(value), "$[") {
		s.Directives = p.scanDirectives(n, value)
	}
	return s
}

// keyScalar reads a mapping key.
func (p *parser) keyScalar(n *yamlx.Node) *ast.Scalar {
	s := p.scalar(n, "key")
	if s != nil && s.Value == "" {
		// An empty key has no plain spelling; it is the empty string, not null.
		s.Null = false
	}
	return s
}

func (p *parser) boolean(n *yamlx.Node, ctx string) *ast.Bool {
	s := p.scalar(n, ctx)
	if s == nil {
		return nil
	}
	b := &ast.Bool{Scalar: *s}
	switch strings.ToLower(strings.TrimSpace(s.Value)) {
	case "true":
		b.Val, b.Known = true, true
	case "false":
		b.Known = true
	default:
		if !s.Null && !deferred(s) {
			p.add("TRL-PARSE-0012", s.Source, fmt.Sprintf("%s: %q is not a boolean", ctx, s.Value),
				map[string]any{"key": ctx, "value": s.Value, "allowed": "true, false"})
		}
	}
	return b
}

// deferred reports whether a scalar's value is only known after template
// expansion or at run time.
func deferred(s *ast.Scalar) bool {
	return len(s.Directives) > 0 || strings.Contains(s.Value, "$(")
}

func (p *parser) enum(n *yamlx.Node, ctx string, allowed []string) *ast.Enum {
	s := p.scalar(n, ctx)
	if s == nil {
		return nil
	}
	e := &ast.Enum{Scalar: *s}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(s.Value), candidate) {
			e.Canon = candidate
			return e
		}
	}
	if !s.Null && !deferred(s) {
		p.add("TRL-PARSE-0012", s.Source, fmt.Sprintf("%s: %q is not one of %s", ctx, s.Value, strings.Join(allowed, ", ")),
			map[string]any{"key": ctx, "value": s.Value, "allowed": strings.Join(allowed, ", ")})
	}
	return e
}

// expression reads an expression-valued field such as a condition. The raw
// text stays in Value; the parsed expression is kept next to it unless the text
// contains template expressions, which only the resolver can evaluate.
func (p *parser) expression(n *yamlx.Node, ctx string) *ast.Expression {
	s := p.scalar(n, ctx)
	if s == nil {
		return nil
	}
	out := &ast.Expression{Scalar: *s}
	if strings.TrimSpace(s.Value) == "" || strings.Contains(s.Value, "${{") {
		return out
	}
	mapper := p.mapper(n)
	node, err := expr.Parse(s.Value)
	if err != nil {
		p.exprError(n, mapper, err, 0)
		return out
	}
	out.Expr = node
	out.ExprSources = exprSources(mapper, node, 0, s.Source)
	return out
}

func (p *parser) mapper(n *yamlx.Node) *yamlx.ScalarMapper {
	mapper, err := yamlx.NewScalarMapper(n)
	if err != nil {
		return nil
	}
	return mapper
}

func mapRange(mapper *yamlx.ScalarMapper, start, end int, fallback diag.Source) diag.Source {
	if mapper == nil {
		return fallback
	}
	source, err := mapper.Map(start)
	if err != nil {
		return fallback
	}
	source.EndLine, source.EndCol, source.EndOffset = source.Line, source.Col, source.Offset
	if end > start {
		// The end is one past the last character, not the end of the scalar range,
		// so that it stays on the line of that character in folded and block scalars.
		last, err := mapper.Map(end - 1)
		if err != nil {
			return fallback
		}
		source.EndLine, source.EndCol, source.EndOffset = last.Line, last.Col+1, last.Offset+1
	}
	return source
}

func exprSources(mapper *yamlx.ScalarMapper, node expr.Expr, base int, fallback diag.Source) []ast.ExprSource {
	var sources []ast.ExprSource
	expr.Walk(expr.VisitorFunc(func(e expr.Expr) {
		span := e.Range()
		sources = append(sources, ast.ExprSource{
			Span: span, Source: mapRange(mapper, base+span.Start, base+span.End, fallback),
		})
	}), node)
	return sources
}

// exprError reports an expression syntax error at the exact file position. base
// is the offset of the parsed text inside the scalar value.
func (p *parser) exprError(n *yamlx.Node, mapper *yamlx.ScalarMapper, err error, base int) {
	offset := 0
	message := err.Error()
	var expressionErr *diag.Error
	if errors.As(err, &expressionErr) {
		message = expressionErr.Finding.Message
		if expressionErr.Finding.Source != nil {
			offset = expressionErr.Finding.Source.Offset
		}
	}
	where := n.Position()
	if mapper != nil {
		if source, mapErr := mapper.Map(base + offset); mapErr == nil {
			where = source
		}
	}
	p.add("TRL-EXPR-0001", where, message, map[string]any{"offset": base + offset})
}

// scanDirectives finds the `${{ }}` occurrences of a scalar value, and a
// whole-value `$[ ]` runtime expression.
func (p *parser) scanDirectives(n *yamlx.Node, value string) []*ast.Directive {
	var found []*ast.Directive
	mapper := p.mapper(n)
	fallback := n.Position()
	for from := 0; from < len(value); {
		index := strings.Index(value[from:], "${{")
		if index < 0 {
			break
		}
		start := from + index
		end := closeTemplate(value, start+3)
		if end < 0 {
			p.add("TRL-EXPR-0001", mapRange(mapper, start, min(start+3, len(value)), fallback),
				"unterminated template expression", map[string]any{"offset": start})
			break
		}
		found = append(found, p.newDirective(n, mapper, ast.DirectiveInsert, value, start, end, ""))
		from = end
	}
	if trimmed := strings.TrimSpace(value); len(found) == 0 && strings.HasPrefix(trimmed, "$[") && strings.HasSuffix(trimmed, "]") {
		start := strings.Index(value, trimmed)
		found = append(found, p.newDirective(n, mapper, ast.DirectiveRuntime, value, start, start+len(trimmed), ""))
	}
	return found
}

// closeTemplate returns the index just after the `}}` that closes a template
// expression whose body starts at from, skipping single-quoted strings.
func closeTemplate(value string, from int) int {
	inString := false
	for i := from; i < len(value); i++ {
		switch {
		case value[i] == '\'':
			inString = !inString
		case !inString && value[i] == '}' && i+1 < len(value) && value[i+1] == '}':
			return i + 2
		}
	}
	return -1
}

// newDirective builds an insertion or runtime directive for value[start:end].
func (p *parser) newDirective(n *yamlx.Node, mapper *yamlx.ScalarMapper, kind ast.DirectiveKind, value string, start, end int, variable string) *ast.Directive {
	raw := value[start:end]
	fallback := n.Position()
	d := &ast.Directive{
		Pos:  ast.Pos{Source: mapRange(mapper, start, end, fallback)},
		Kind: kind, Raw: raw, Start: start, End: end, Variable: variable,
	}
	node, err := expr.Parse(raw)
	if err != nil {
		p.exprError(n, mapper, err, start)
		return d
	}
	d.Expr = node
	d.ExprSources = exprSources(mapper, node, start, fallback)
	return d
}

// keyDirective parses an if, elseif, else or each mapping key. The directive is
// always returned so that the body is kept; a malformed head is diagnosed.
func (p *parser) keyDirective(key *yamlx.Node, kind ast.DirectiveKind) *ast.Directive {
	text, ok := p.scalarValue(key, "key")
	if !ok {
		return &ast.Directive{Pos: pos(key), Kind: kind}
	}
	trimmed := strings.TrimSpace(text)
	start := strings.Index(text, trimmed)
	mapper := p.mapper(key)
	fallback := key.Position()
	d := &ast.Directive{
		Pos:  ast.Pos{Source: mapRange(mapper, start, start+len(trimmed), fallback)},
		Kind: kind, Raw: trimmed, Start: start, End: start + len(trimmed),
	}
	parsed, err := expr.ParseDirective(trimmed)
	if err != nil {
		p.exprError(key, mapper, err, start)
		return d
	}
	d.Variable = parsed.Variable
	d.Expr = parsed.Expr
	if parsed.Expr != nil {
		// expr reports the spans of a directive expression relative to the
		// expression text, not to the key.
		d.ExprSources = exprSources(mapper, parsed.Expr, start+directiveBodyOffset(trimmed, kind), fallback)
	}
	return d
}

// ---------------------------------------------------------------------------
// Free-form values.

// free converts any YAML value to an Any tree, preserving key directives.
func (p *parser) free(n *yamlx.Node) *ast.Any {
	if n == nil || !p.visit(n) {
		return nil
	}
	defer p.leave()
	switch n.Kind() {
	case yamlx.ScalarKind:
		s := p.scalar(n, "value")
		if s == nil {
			return nil
		}
		return &ast.Any{Pos: pos(n), Kind: ast.AnyScalar, Scalar: s}
	case yamlx.SequenceKind:
		items, problems := n.Seq()
		if len(problems) > 0 {
			p.addAll(problems)
			return nil
		}
		out := &ast.Any{Pos: pos(n), Kind: ast.AnySeq}
		for _, item := range items {
			if value := p.free(item); value != nil {
				out.Items = append(out.Items, value)
			}
		}
		return out
	case yamlx.MappingKind:
		pairs, problems := n.Map()
		if len(problems) > 0 {
			p.addAll(problems)
			return nil
		}
		out := &ast.Any{Pos: pos(n), Kind: ast.AnyMap}
		for _, pair := range pairs {
			entry := p.freeEntry(pair)
			if entry != nil {
				out.Entries = append(out.Entries, entry)
			}
		}
		return out
	default:
		p.typeError(n, "scalar, sequence or mapping", "value")
		return nil
	}
}

func (p *parser) freeEntry(pair yamlx.Pair) *ast.AnyEntry {
	name, problems := pair.Key.Scalar()
	if len(problems) > 0 {
		p.typeError(pair.Key, "scalar key", "mapping")
		return nil
	}
	entry := &ast.AnyEntry{Pos: pos(pair.Key)}
	if kind, ok := classifyKey(name); ok {
		entry.Directive = p.keyDirective(pair.Key, kind)
	} else {
		entry.Key = p.keyScalar(pair.Key)
	}
	entry.Value = p.free(pair.Value)
	return entry
}

// directiveBodyOffset returns the offset, inside the key text of an if, elseif
// or each directive, where its expression starts.
func directiveBodyOffset(key string, kind ast.DirectiveKind) int {
	inner := key[3 : len(key)-2]
	offset := 3 + (len(inner) - len(strings.TrimLeft(inner, " \t")))
	rest := key[offset:]
	switch kind {
	case ast.DirectiveIf:
		rest = strings.TrimPrefix(rest, "if")
		offset += 2
	case ast.DirectiveElseIf:
		rest = strings.TrimPrefix(rest, "elseif")
		offset += 6
	case ast.DirectiveEach:
		if index := strings.Index(rest, " in "); index >= 0 {
			rest = rest[index+4:]
			offset += index + 4
		}
	default:
		return offset
	}
	return offset + (len(rest) - len(strings.TrimLeft(rest, " \t")))
}
