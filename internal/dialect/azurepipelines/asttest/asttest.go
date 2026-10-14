// Package asttest holds helpers shared by the tests of the Azure Pipelines
// structure packages: a structural comparison that ignores positions and a
// deterministic line-per-leaf dump of a syntax tree.
package asttest

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// Options returns the go-cmp options that compare syntax trees structurally:
// source positions are ignored and parsed expressions compare by their
// canonical printed form.
func Options() []cmp.Option {
	return []cmp.Option{
		cmp.Transformer("position", func(ast.Pos) struct{} { return struct{}{} }),
		cmp.Transformer("source", func(diag.Source) struct{} { return struct{}{} }),
		cmp.Transformer("expr", func(e expr.Expr) string {
			if e == nil {
				return ""
			}
			return expr.Print(e)
		}),
		// Quoting is presentation: the printer does not have to re-emit the
		// original quote style, so round-trip comparisons ignore it.
		cmp.Transformer("quoted", func(s ast.Scalar) ast.Scalar {
			s.Quoted = false
			return s
		}),
	}
}

// Diff returns a human-readable difference between two trees, or the empty
// string when they are structurally equal.
func Diff(want, got *ast.Pipeline) string { return cmp.Diff(want, got, Options()...) }

// Dump renders the tree as sorted-by-structure `path = value` lines. Paths use
// the lower-camel names of the Go fields, `[i]` for list items and the key text
// for mapping entries. With positions, every node also yields a `path @ line:col`
// line.
func Dump(p *ast.Pipeline, positions bool) []string {
	d := &dumper{positions: positions}
	d.walk("", reflect.ValueOf(p))
	return d.lines
}

type dumper struct {
	positions bool
	lines     []string
}

func (d *dumper) line(path, value string) {
	if path == "" {
		path = "$"
	}
	d.lines = append(d.lines, path+" = "+value)
}

func (d *dumper) position(path string, source diag.Source) {
	if !d.positions {
		return
	}
	if path == "" {
		path = "$"
	}
	d.lines = append(d.lines, fmt.Sprintf("%s @ %d:%d", path, source.Line, source.Col))
}

func escape(value string) string {
	return strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(value)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func lowerFirst(name string) string {
	runes := []rune(name)
	end := 0
	for end < len(runes) && unicode.IsUpper(runes[end]) {
		end++
	}
	if end > 1 && end < len(runes) {
		end-- // keep the capital that starts the next word: VMImage -> vmImage
	}
	if end == 0 {
		return name
	}
	return strings.ToLower(string(runes[:end])) + string(runes[end:])
}

var (
	scalarType    = reflect.TypeOf(ast.Scalar{})
	directiveType = reflect.TypeOf(ast.Directive{})
	posType       = reflect.TypeOf(ast.Pos{})
	sourceType    = reflect.TypeOf(diag.Source{})
	exprType      = reflect.TypeOf((*expr.Expr)(nil)).Elem()
)

func (d *dumper) walk(path string, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			d.walk(path, v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		if v.Type() == exprType {
			d.line(path, expr.Print(v.Interface().(expr.Expr)))
			return
		}
		d.line(join(path, "type"), strings.TrimPrefix(v.Elem().Type().String(), "*ast."))
		d.walk(path, v.Elem())
	case reflect.Slice:
		for i := range v.Len() {
			d.walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
		}
	case reflect.String:
		if v.String() != "" {
			d.line(path, escape(v.String()))
		}
	case reflect.Bool:
		if v.Bool() {
			d.line(path, "true")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		d.line(path, fmt.Sprint(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if stringer, ok := v.Interface().(fmt.Stringer); ok {
			d.line(path, stringer.String())
		} else if v.Uint() != 0 {
			d.line(path, fmt.Sprint(v.Uint()))
		}
	case reflect.Struct:
		d.structure(path, v)
	default:
	}
}

func (d *dumper) structure(path string, v reflect.Value) {
	t := v.Type()
	switch {
	case t == posType, t == sourceType:
		return
	case t == directiveType:
		d.directive(path, v.Interface().(ast.Directive))
	case t == scalarType:
		s := v.Interface().(ast.Scalar)
		value := escape(s.Value)
		switch {
		case s.Null:
			value = "<null>" + value
		case value == "":
			value = "<empty>"
		}
		d.line(path, value)
		d.position(path, s.Source)
		for i, directive := range s.Directives {
			d.directive(fmt.Sprintf("%s.directives[%d]", path, i), *directive)
		}
	case strings.HasPrefix(t.Name(), "List["):
		d.list(path, v)
	case strings.HasPrefix(t.Name(), "Map["):
		d.mapping(path, v)
	case t.Name() == "Any":
		d.free(path, v.Interface().(ast.Any))
	default:
		d.fields(path, v)
	}
}

func (d *dumper) fields(path string, v reflect.Value) {
	t := v.Type()
	if f, ok := t.FieldByName("Pos"); ok && len(f.Index) == 1 {
		pos := v.FieldByName("Pos")
		d.position(path, pos.Interface().(ast.Pos).Source)
	}
	for i := range t.NumField() {
		field := t.Field(i)
		if field.Type == posType || field.Type == sourceType {
			continue
		}
		name := path
		if !field.Anonymous {
			name = join(path, lowerFirst(field.Name))
		}
		if field.Name == "ExprSources" {
			continue
		}
		if field.Type == reflect.TypeOf(expr.Expr(nil)) {
			if expression := v.Field(i); !expression.IsNil() {
				d.line(join(path, "expr"), expr.Print(expression.Interface().(expr.Expr)))
			}
			continue
		}
		d.walk(name, v.Field(i))
	}
}

func (d *dumper) directive(path string, directive ast.Directive) {
	value := directive.Kind.String() + ": " + escape(directive.Raw)
	d.line(path, value)
	d.position(path, directive.Source)
	if directive.Variable != "" {
		d.line(join(path, "variable"), directive.Variable)
	}
	if directive.Expr != nil {
		d.line(join(path, "expr"), expr.Print(directive.Expr))
	}
}

func (d *dumper) list(path string, v reflect.Value) {
	if pos := v.FieldByName("Pos"); pos.IsValid() {
		d.position(path, pos.Interface().(ast.Pos).Source)
	}
	if insertion := v.FieldByName("Insertion"); insertion.IsValid() && !insertion.IsNil() {
		d.directive(join(path, "insertion"), *insertion.Interface().(*ast.Directive))
	}
	items := v.FieldByName("Items")
	for i := range items.Len() {
		item := items.Index(i)
		at := fmt.Sprintf("%s[%d]", path, i)
		switch {
		case !item.FieldByName("Insert").IsNil():
			d.directive(at, *item.FieldByName("Insert").Interface().(*ast.Directive))
		case !item.FieldByName("Cond").IsNil():
			cond := item.FieldByName("Cond").Elem()
			d.directive(at, *cond.FieldByName("Directive").Interface().(*ast.Directive))
			d.walk(join(at, "body"), cond.FieldByName("Items"))
			if other := cond.FieldByName("Other"); !other.IsNil() {
				d.walk(join(at, "body"), other)
			}
		default:
			d.walk(at, item.FieldByName("Value"))
		}
	}
}

func (d *dumper) mapping(path string, v reflect.Value) {
	if pos := v.FieldByName("Pos"); pos.IsValid() {
		d.position(path, pos.Interface().(ast.Pos).Source)
	}
	if insertion := v.FieldByName("Insertion"); insertion.IsValid() && !insertion.IsNil() {
		d.directive(join(path, "insertion"), *insertion.Interface().(*ast.Directive))
	}
	entries := v.FieldByName("Entries")
	for i := range entries.Len() {
		entry := entries.Index(i)
		if cond := entry.FieldByName("Cond"); !cond.IsNil() {
			at := fmt.Sprintf("%s.cond[%d]", path, i)
			d.directive(at, *cond.Elem().FieldByName("Directive").Interface().(*ast.Directive))
			d.walk(join(at, "body"), cond.Elem().FieldByName("Body"))
			continue
		}
		key := entry.FieldByName("Key").Interface().(*ast.Scalar)
		d.walk(join(path, key.Value), entry.FieldByName("Value"))
		d.position(join(path, key.Value)+".key", key.Source)
	}
}

func (d *dumper) free(path string, a ast.Any) {
	d.position(path, a.Source)
	switch a.Kind {
	case ast.AnySeq:
		for i, item := range a.Items {
			d.free(fmt.Sprintf("%s[%d]", path, i), *item)
		}
	case ast.AnyMap:
		for i, entry := range a.Entries {
			if entry.Directive != nil {
				at := fmt.Sprintf("%s.cond[%d]", path, i)
				d.directive(at, *entry.Directive)
				if entry.Value != nil {
					d.free(join(at, "body"), *entry.Value)
				}
				continue
			}
			if entry.Value != nil {
				d.free(join(path, entry.Key.Value), *entry.Value)
			}
		}
	default:
		if a.Scalar != nil {
			d.walk(path, reflect.ValueOf(*a.Scalar))
		}
	}
}
