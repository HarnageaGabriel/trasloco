package parse

import (
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// checkChain validates directive adjacency: elseif and else must directly
// follow an if or elseif of the same sequence or mapping.
func (p *parser) checkChain(previous *ast.DirectiveKind, d *ast.Directive) {
	if (d.Kind == ast.DirectiveElseIf || d.Kind == ast.DirectiveElse) &&
		*previous != ast.DirectiveIf && *previous != ast.DirectiveElseIf {
		p.add("TRL-PARSE-0013", d.Source, fmt.Sprintf("%s directive has no preceding if", d.Kind),
			map[string]any{"directive": d.Kind.String()})
	}
	*previous = d.Kind
}

// nullBody reports a directive without a body.
func (p *parser) nullBody(body *yamlx.Node, d *ast.Directive) bool {
	if body == nil || (body.Kind() == yamlx.ScalarKind && body.Tag() == "!!null") {
		p.add("TRL-PARSE-0013", d.Source, fmt.Sprintf("%s directive has no body", d.Kind),
			map[string]any{"directive": d.Kind.String()})
		return true
	}
	return false
}

// extra preserves an entry the typed fields did not consume. Unknown keys are
// reported; dynamic keys (`${{ expr }}: value`) are expected and not.
func (m *mapping) extra(e *entry) *ast.Extra {
	e.used = true
	if !e.dynamic() {
		m.p.add("TRL-PARSE-0002", e.key.Position(),
			fmt.Sprintf("%s: unknown key %q is preserved as an extension", m.ctx, e.name),
			map[string]any{"key": e.name, "context": m.ctx})
	}
	key := m.p.keyScalar(e.key)
	if key == nil {
		return nil
	}
	return &ast.Extra{Key: key, Value: m.p.free(e.value)}
}

// finish collects what the typed fields of a mapping node did not consume:
// key directives, whose bodies are parsed with the same fill function, and
// unknown or dynamic keys. Both keep source order.
func finish[T any](m *mapping, fill func(*T, *mapping)) ([]*ast.Branch[*T], []*ast.Extra) {
	var inserts []*ast.Branch[*T]
	var extras []*ast.Extra
	previous := ast.DirectiveKind(0)
	for _, e := range m.entries {
		if e.dir != 0 {
			d := m.p.keyDirective(e.key, e.dir)
			m.p.checkChain(&previous, d)
			if m.p.nullBody(e.value, d) {
				continue
			}
			body := m.p.open(e.value, m.ctx)
			if body == nil {
				continue
			}
			branch := &ast.Branch[*T]{Directive: d, Body: new(T)}
			fill(branch.Body, body)
			body.close()
			inserts = append(inserts, branch)
			continue
		}
		previous = 0
		if e.used {
			continue
		}
		if extra := m.extra(e); extra != nil {
			extras = append(extras, extra)
		}
	}
	return inserts, extras
}

// build parses a mapping node into a typed node. required lists keys that must
// be present (directive bodies are partial and are not checked).
func build[T any](p *parser, n *yamlx.Node, ctx string, fill func(*T, *mapping), required ...string) *T {
	m := p.open(n, ctx)
	if m == nil {
		return nil
	}
	defer m.close()
	for _, key := range required {
		m.requireKey(key)
	}
	out := new(T)
	fill(out, m)
	return out
}

// item adapts build to a list item function.
func item[T any](p *parser, ctx string, fill func(*T, *mapping), required ...string) func(*yamlx.Node) (*T, bool) {
	return func(n *yamlx.Node) (*T, bool) {
		out := build(p, n, ctx, fill, required...)
		return out, out != nil
	}
}

// requireKey reports a missing required key of the mapping itself.
func (m *mapping) requireKey(key string, alternatives ...string) bool {
	for _, name := range append([]string{key}, alternatives...) {
		for _, e := range m.entries {
			if e.dir == 0 && strings.EqualFold(e.name, name) {
				return true
			}
		}
	}
	m.p.add("TRL-PARSE-0011", m.node.Position(), fmt.Sprintf("%s: required key %q is missing", m.ctx, key),
		map[string]any{"key": key, "context": m.ctx})
	return false
}

// selectKey picks the key that selects one alternative of a polymorphic item
// (stage or template, the step keyword, ...). The first one in source order
// wins; a missing selector or a second one is diagnosed.
func (m *mapping) selectKey(choices ...string) (string, bool) {
	var found []string
	chosen := ""
	for _, e := range m.entries {
		if e.dir != 0 || e.used {
			continue
		}
		for _, choice := range choices {
			if strings.EqualFold(e.name, choice) {
				found = append(found, e.name)
				if chosen == "" {
					chosen = choice
				}
			}
		}
	}
	switch {
	case len(found) == 0:
		m.p.add("TRL-PARSE-0011", m.node.Position(),
			fmt.Sprintf("%s: one of %s is required", m.ctx, strings.Join(choices, ", ")),
			map[string]any{"key": strings.Join(choices, "|"), "context": m.ctx})
		return "", false
	case len(found) > 1:
		m.p.add("TRL-PARSE-0014", m.node.Position(),
			fmt.Sprintf("%s: %s select different alternatives; only one is allowed", m.ctx, strings.Join(found, ", ")),
			map[string]any{"keys": strings.Join(found, ", ")})
	}
	return chosen, true
}

// directiveOnly reports whether every key of the mapping is a key directive.
func (p *parser) directiveOnly(n *yamlx.Node) ([]yamlx.Pair, bool) {
	if n.Kind() != yamlx.MappingKind {
		return nil, false
	}
	pairs, problems := n.Map()
	if len(problems) > 0 || len(pairs) == 0 {
		return nil, false
	}
	for _, pair := range pairs {
		name, bad := pair.Key.Scalar()
		if len(bad) > 0 {
			return nil, false
		}
		if _, ok := classifyKey(name); !ok {
			return nil, false
		}
	}
	return pairs, true
}

// parseList reads a sequence. A mapping item whose keys are all key directives
// becomes conditional list items; a whole-value `${{ }}` becomes an insertion.
func parseList[T any](p *parser, n *yamlx.Node, ctx string, parseItem func(*yamlx.Node) (T, bool)) *ast.List[T] {
	if n == nil || !p.visit(n) {
		return nil
	}
	defer p.leave()
	out := &ast.List[T]{Pos: pos(n)}
	switch n.Kind() {
	case yamlx.ScalarKind:
		s := p.scalar(n, ctx)
		switch {
		case s == nil:
			return nil
		case s.Whole() != nil:
			out.Insertion = s.Whole()
		case !s.Null:
			p.typeError(n, "sequence", ctx)
			return nil
		}
		return out
	case yamlx.SequenceKind:
	default:
		p.typeError(n, "sequence", ctx)
		return nil
	}
	nodes, problems := n.Seq()
	if len(problems) > 0 {
		p.addAll(problems)
		return nil
	}
	previous := ast.DirectiveKind(0)
	for _, node := range nodes {
		if pairs, ok := p.directiveOnly(node); ok && p.visit(node) {
			for _, pair := range pairs {
				name, _ := pair.Key.Scalar()
				kind, _ := classifyKey(name)
				d := p.keyDirective(pair.Key, kind)
				p.checkChain(&previous, d)
				if p.nullBody(pair.Value, d) {
					continue
				}
				cond := &ast.ListCond[T]{Directive: d}
				if pair.Value.Kind() == yamlx.SequenceKind {
					cond.Items = parseList(p, pair.Value, ctx, parseItem)
				} else {
					cond.Other = p.free(pair.Value)
				}
				out.Items = append(out.Items, ast.ListItem[T]{Cond: cond})
			}
			p.leave()
			continue
		}
		previous = 0
		if node != nil && node.Kind() == yamlx.ScalarKind {
			if s := p.scalar(node, ctx); s != nil && s.Whole() != nil {
				out.Items = append(out.Items, ast.ListItem[T]{Insert: s.Whole()})
				continue
			}
		}
		if value, ok := parseItem(node); ok {
			out.Items = append(out.Items, ast.ListItem[T]{Value: value})
		}
	}
	return out
}

// mapOf reads a mapping whose values are V, preserving key directives.
func mapOf[V any](p *parser, n *yamlx.Node, ctx string, value func(*yamlx.Node, string) (V, bool)) *ast.Map[V] {
	if n == nil || !p.visit(n) {
		return nil
	}
	defer p.leave()
	out := &ast.Map[V]{Pos: pos(n)}
	switch n.Kind() {
	case yamlx.ScalarKind:
		s := p.scalar(n, ctx)
		switch {
		case s == nil:
			return nil
		case s.Whole() != nil:
			out.Insertion = s.Whole()
		case !s.Null:
			p.typeError(n, "mapping", ctx)
			return nil
		}
		return out
	case yamlx.MappingKind:
	default:
		p.typeError(n, "mapping", ctx)
		return nil
	}
	pairs, problems := n.Map()
	if len(problems) > 0 {
		p.addAll(problems)
		return nil
	}
	previous := ast.DirectiveKind(0)
	for _, pair := range pairs {
		name, bad := pair.Key.Scalar()
		if len(bad) > 0 {
			p.typeError(pair.Key, "scalar key", ctx)
			continue
		}
		if kind, ok := classifyKey(name); ok {
			d := p.keyDirective(pair.Key, kind)
			p.checkChain(&previous, d)
			if p.nullBody(pair.Value, d) {
				continue
			}
			body := mapOf(p, pair.Value, ctx, value)
			if body != nil {
				out.Entries = append(out.Entries, ast.MapEntry[V]{Cond: &ast.MapCond[V]{Directive: d, Body: body}})
			}
			continue
		}
		previous = 0
		key := p.keyScalar(pair.Key)
		v, ok := value(pair.Value, name)
		if key != nil && ok {
			out.Entries = append(out.Entries, ast.MapEntry[V]{Key: key, Value: v})
		}
	}
	return out
}

func (p *parser) stringMap(n *yamlx.Node, ctx string) *ast.Map[*ast.Scalar] {
	return mapOf(p, n, ctx, func(v *yamlx.Node, key string) (*ast.Scalar, bool) {
		s := p.scalar(v, key)
		return s, s != nil
	})
}

func (p *parser) anyMap(n *yamlx.Node, ctx string) *ast.Map[*ast.Any] {
	return mapOf(p, n, ctx, func(v *yamlx.Node, _ string) (*ast.Any, bool) {
		a := p.free(v)
		return a, a != nil
	})
}

func (p *parser) scalarList(n *yamlx.Node, ctx string) *ast.List[*ast.Scalar] {
	return parseList(p, n, ctx, func(v *yamlx.Node) (*ast.Scalar, bool) {
		s := p.scalar(v, ctx)
		return s, s != nil
	})
}
