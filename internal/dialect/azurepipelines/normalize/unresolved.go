package normalize

import (
	"reflect"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

var directiveType = reflect.TypeFor[ast.Directive]()

func (c *conv) findUnresolved(root *ast.Pipeline) int {
	count := 0
	var walk func(reflect.Value)
	walk = func(value reflect.Value) {
		if !value.IsValid() {
			return
		}
		if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			if value.Kind() == reflect.Pointer && value.Elem().Type() == directiveType {
				directive := value.Interface().(*ast.Directive)
				if directive.Kind != ast.DirectiveRuntime {
					count++
				}
				return
			}
			name := value.Elem().Type().Name()
			if name == "Extends" || name == "StageTemplate" || name == "JobTemplate" || name == "StepTemplate" || name == "VariableTemplate" {
				count++
				return
			}
			walk(value.Elem())
			return
		}
		switch value.Kind() {
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				walk(value.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				walk(value.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(root))
	return count
}
