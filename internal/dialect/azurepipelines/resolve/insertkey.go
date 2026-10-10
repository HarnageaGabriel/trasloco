package resolve

import (
	"context"
	"reflect"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// frame describes how the struct being walked was reached, which is what a
// `${{ insert }}` key needs to re-parse an inserted mapping in context: the
// inserted keys are typed fields of this struct, and only the parser knows how
// to build them.
type frame struct {
	name  string
	value reflect.Value
	// owner and field are set when the struct is the typed field `field` of
	// the struct described by owner.
	owner *frame
	field string
}

// takeFrame returns the frame prepared for the struct about to be walked.
func (r *resolver) takeFrame(value reflect.Value) *frame {
	f := r.next
	r.next = nil
	if f == nil {
		f = &frame{name: value.Type().Name()}
	}
	if !f.value.IsValid() {
		f.value = value
	}
	return f
}

// frameFor prepares the frame for a field of the struct described by self: an
// embedded struct shares it, a typed mapping gets a child frame, anything else
// has none.
func (r *resolver) frameFor(self *frame, sf reflect.StructField, field reflect.Value) *frame {
	switch {
	case field.Kind() == reflect.Struct && sf.Anonymous:
		return self
	case field.Kind() == reflect.Pointer && !field.IsNil() && field.Elem().Kind() == reflect.Struct:
		if _, typed := field.Elem().Type().FieldByName("Inserts"); typed {
			return &frame{name: field.Elem().Type().Name(), owner: self, field: sf.Name}
		}
	}
	return nil
}

// insertDoc is a generated document that re-parses an inserted mapping.
type insertDoc struct {
	document  string
	errorLine int
	// pick returns the struct the inserted keys were parsed into.
	pick func(*ast.Pipeline) reflect.Value
	// clear lists placeholder fields that must not overwrite the real ones.
	clear []string
}

// structInsertKeys expands the `${{ insert }}: ${{ mapping }}` keys of a typed
// mapping: the mapping's entries become fields of the struct, parsed in the
// context of the struct's type, later keys winning over earlier ones.
func (r *resolver) structInsertKeys(ctx context.Context, value reflect.Value, env *expr.Object, self *frame) error {
	tail := value.FieldByName("Tail")
	if !tail.IsValid() {
		return nil
	}
	extraField := tail.FieldByName("Extra")
	if !extraField.IsValid() || extraField.Kind() != reflect.Slice || extraField.Len() == 0 {
		return nil
	}
	extras, ok := extraField.Interface().([]*ast.Extra)
	if !ok {
		return nil
	}
	var inserts, kept []*ast.Extra
	for _, extra := range extras {
		if extra != nil && isInsertKey(extra.Key) {
			inserts = append(inserts, extra)
		} else {
			kept = append(kept, extra)
		}
	}
	if len(inserts) == 0 {
		return nil
	}
	if len(kept) == 0 {
		extraField.Set(reflect.Zero(extraField.Type()))
	} else {
		extraField.Set(reflect.ValueOf(kept))
	}
	for _, extra := range inserts {
		inserted, err := r.resolveAny(ctx, extra.Value, env)
		if err != nil {
			return err
		}
		object, ok := inserted.(*expr.Object)
		if !ok {
			r.addAt("TRL-EXPR-0005", extra.Key.Pos, "insert requires a mapping", nil)
			continue
		}
		if err := r.mergeInserted(ctx, value, self, object, extra.Key.Pos); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) mergeInserted(ctx context.Context, value reflect.Value, self *frame, object *expr.Object, at ast.Pos) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	doc, ok := r.insertDocument(self, object, at)
	if !ok {
		return nil
	}
	if err := r.charge(len(object.Properties)+1, len(doc.document)); err != nil {
		return err
	}
	pipeline, ok := r.parseDocument(doc.document, doc.errorLine, at)
	if !ok {
		return nil
	}
	produced := doc.pick(pipeline)
	if !produced.IsValid() {
		return nil
	}
	for _, name := range doc.clear {
		if field := produced.FieldByName(name); field.IsValid() && field.CanSet() {
			field.Set(reflect.Zero(field.Type()))
		}
	}
	setPos(produced, r.originOf(object, at))
	restoreScalars(produced)
	mergeStruct(value, produced)
	return nil
}

func hasProperty(object *expr.Object, name string) bool {
	for _, property := range object.Properties {
		if property.Name == name {
			return true
		}
	}
	return false
}

// jsonKey is the YAML key of a struct field.
func jsonKey(field string) string {
	if field == "PR" {
		return "pr"
	}
	return strings.ToLower(field[:1]) + field[1:]
}

// insertDocument builds the document that parses props as the keys of the
// struct described by f. Structs that cannot stand alone are wrapped in the
// struct that holds them; a struct with no such holder is not supported.
func (r *resolver) insertDocument(f *frame, props *expr.Object, at ast.Pos) (insertDoc, bool) {
	listed := func(listKey, key, placeholder string, pick func(*ast.Pipeline) reflect.Value, clearFields ...string) (insertDoc, bool) {
		object := &expr.Object{}
		placeholderUsed := !hasProperty(props, key)
		if placeholderUsed {
			object.Properties = append(object.Properties, expr.Property{Name: key, Value: expr.String(placeholder)})
		}
		object.Properties = append(object.Properties, props.Properties...)
		encoded, err := r.encode(object)
		if err != nil {
			r.addAt("TRL-EXPR-0005", at, err.Error(), nil)
			return insertDoc{}, false
		}
		doc := insertDoc{document: listKey + ":\n- " + encoded + "\n", errorLine: 2, pick: pick}
		if placeholderUsed {
			doc.clear = clearFields
		}
		return doc, true
	}
	switch f.name {
	case "Pipeline":
		encoded, err := r.encode(props)
		if err != nil {
			r.addAt("TRL-EXPR-0005", at, err.Error(), nil)
			return insertDoc{}, false
		}
		return insertDoc{
			document: encoded + "\n", errorLine: 1, clear: []string{"Shape"},
			pick: func(p *ast.Pipeline) reflect.Value { return reflect.ValueOf(p).Elem() },
		}, true
	case "StageDef":
		return listed("stages", "stage", "x", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Stages) }, "Stage")
	case "JobDef":
		return listed("jobs", "job", "x", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Jobs) }, "Job")
	case "Deployment":
		return listed("jobs", "deployment", "x", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Jobs) }, "Deployment")
	case "ScriptStep":
		keyword := f.value.FieldByName("Kind").Interface().(ast.ScriptKind).Keyword()
		return listed("steps", keyword, "x", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Steps) }, "Script")
	case "TaskStep":
		return listed("steps", "task", "x@1", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Steps) }, "Task", "Ref")
	case "CheckoutStep":
		return listed("steps", "checkout", "self", func(p *ast.Pipeline) reflect.Value { return firstItem(p.Steps) }, "Checkout")
	}
	if f.owner == nil {
		r.addAt("TRL-EXPR-0005", at, "${{ insert }} is not supported in a "+f.name+" here", nil)
		return insertDoc{}, false
	}
	wrapped := &expr.Object{Properties: []expr.Property{{Name: jsonKey(f.field), Value: props}}}
	doc, ok := r.insertDocument(f.owner, wrapped, at)
	if !ok {
		return insertDoc{}, false
	}
	inner, field := doc.pick, f.field
	doc.clear = nil
	doc.pick = func(p *ast.Pipeline) reflect.Value {
		holder := inner(p)
		if !holder.IsValid() {
			return holder
		}
		child := holder.FieldByName(field)
		if child.Kind() == reflect.Pointer {
			if child.IsNil() {
				return reflect.Value{}
			}
			child = child.Elem()
		}
		return child
	}
	return doc, true
}
