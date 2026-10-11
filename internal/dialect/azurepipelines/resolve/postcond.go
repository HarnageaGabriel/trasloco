package resolve

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

// postcondition walks the expanded AST and reports an internal error for any
// compile-time construct that survived resolution: a Cond, Insert, Insertion,
// Directive, key directive, template reference or ${{ }} occurrence.
func (r *resolver) postcondition(p *ast.Pipeline) {
	leftovers := findLeftovers(p)
	for _, leftover := range leftovers {
		r.addAt("TRL-INT-0002", leftover.Pos, "unresolved template construct remains after resolution: "+leftover.What, map[string]any{"construct": leftover.What})
		if len(leftovers) > 20 {
			break
		}
	}
}

// leftover describes one compile-time construct found in a resolved AST.
type leftover struct {
	What string
	Pos  ast.Pos
}

// findLeftovers returns every compile-time construct still present in p. A
// resolved AST has none; it is the oracle for tests and fuzzing.
func findLeftovers(p *ast.Pipeline) []leftover {
	var found []leftover
	var pos ast.Pos
	var visit func(value reflect.Value, depth int)
	report := func(what string) { found = append(found, leftover{What: what, Pos: pos}) }
	visit = func(value reflect.Value, depth int) {
		if !value.IsValid() {
			return
		}
		if depth > maxWalkDepth {
			report("nesting too deep to verify")
			return
		}
		switch value.Kind() {
		case reflect.Interface:
			if !value.IsNil() {
				visit(value.Elem(), depth+1)
			}
		case reflect.Pointer:
			if value.IsNil() || !value.CanInterface() {
				return
			}
			switch node := value.Interface().(type) {
			case *ast.StepTemplate, *ast.JobTemplate, *ast.StageTemplate, *ast.VariableTemplate, *ast.Extends:
				report(fmt.Sprintf("%T", node))
				return
			case *ast.Directive:
				// Runtime $[ ] expressions are evaluated by Azure at run time and
				// stay in the resolved AST; everything else is compile-time.
				if node.Kind != ast.DirectiveRuntime {
					pos = node.Pos
					report("directive " + node.Raw)
				}
				return
			case *ast.Scalar:
				for _, directive := range node.Directives {
					if directive.Kind != ast.DirectiveRuntime {
						pos = node.Pos
						report("directive " + directive.Raw)
					}
				}
				return
			}
			visit(value.Elem(), depth+1)
		case reflect.Struct:
			if value.CanAddr() {
				if p, ok := value.Addr().Interface().(*ast.Pos); ok {
					pos = *p
					return
				}
			}
			t := value.Type()
			name := t.Name()
			switch {
			case strings.HasPrefix(name, "ListItem["):
				if !value.FieldByName("Cond").IsNil() {
					report("list condition")
				}
				if !value.FieldByName("Insert").IsNil() {
					report("list insert")
				}
			case strings.HasPrefix(name, "MapEntry["):
				if !value.FieldByName("Cond").IsNil() {
					report("map condition")
				}
			case name == "Insertable":
				if insertion, _ := value.FieldByName("Insertion").Interface().(*ast.Directive); compileTime(insertion) {
					report("whole-value insertion")
				}
			case name == "AnyEntry":
				if field := value.FieldByName("Directive"); !field.IsNil() {
					directive, _ := field.Interface().(*ast.Directive)
					if directive != nil && directive.Kind != ast.DirectiveRuntime {
						report("free-form directive")
					}
				}
			}
			for i := 0; i < value.NumField(); i++ {
				if !value.Field(i).CanInterface() {
					continue
				}
				if t.Field(i).Name == "Inserts" && value.Field(i).Len() > 0 {
					report("mapping branch")
					continue
				}
				visit(value.Field(i), depth+1)
			}
		case reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				visit(value.Index(i), depth+1)
			}
		}
	}
	visit(reflect.ValueOf(p), 0)
	return found
}
