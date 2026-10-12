package resolve

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

var (
	resolvedTaskGUIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	resolvedTaskVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
)

func clone[T any](input T) T { return cloneValue(reflect.ValueOf(input)).Interface().(T) }

func cloneValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.Type().PkgPath() == "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr" {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneValue(value.Elem()))
		return out
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := cloneValue(value.Elem())
		wrapped := reflect.New(value.Type()).Elem()
		wrapped.Set(out)
		return wrapped
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneValue(value.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneValue(value.Index(i)))
		}
		return out
	default:
		return value
	}
}

// walk expands every compile-time construct below value in place. It is the
// only traversal: lists, mappings, free-form values and scalars are handed to
// their shared expanders and everything else is visited structurally.
func (r *resolver) walk(ctx context.Context, value reflect.Value, env *expr.Object, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.exceeded {
		return errBudget
	}
	if r.errorCount >= maxErrorDiagnostics {
		return errTooMany
	}
	if !value.IsValid() {
		return nil
	}
	if depth > maxWalkDepth {
		r.add("TRL-EXPR-0005", r.pos, "pipeline nesting is too deep to expand", map[string]any{"limit": maxWalkDepth})
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return r.walk(ctx, value.Elem(), env, depth+1)
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if !value.CanInterface() {
			return nil
		}
		if handled, err := r.walkKnown(ctx, value.Interface(), env); handled {
			return err
		}
		return r.walk(ctx, value.Elem(), env, depth+1)
	case reflect.Struct:
		return r.walkStruct(ctx, value, env, depth)
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := r.walk(ctx, value.Index(i), env, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkKnown dispatches the node types that have a dedicated expander.
func (r *resolver) walkKnown(ctx context.Context, node any, env *expr.Object) (bool, error) {
	switch node := node.(type) {
	case *ast.Scalar:
		return true, r.expandScalar(ctx, node, env)
	case *ast.Bool:
		return true, r.expandBool(ctx, node, env)
	case *ast.Enum:
		return true, r.expandEnum(ctx, node, nil, "", env)
	case *ast.Expression:
		return true, r.expandExpression(ctx, node, env)
	case *ast.Any:
		return true, r.expandAny(ctx, node, env)
	case *ast.Extends, *ast.Pos, *ast.Directive:
		return true, nil
	case *ast.Variables:
		return true, r.expandVariables(ctx, node, env)
	case *ast.Map[*ast.Scalar]:
		return true, mapWalk(ctx, r, node, env, scalarMapOps(r), false)
	case *ast.Map[*ast.Any]:
		return true, mapWalk(ctx, r, node, env, anyMapOps(r), false)
	case *ast.Map[*ast.Map[*ast.Scalar]]:
		return true, mapWalk(ctx, r, node, env, matrixMapOps(r), false)
	case *ast.List[*ast.Scalar]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[ast.Step]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[ast.Job]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[ast.Stage]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[ast.Variable]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.Any]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.Parameter]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.Schedule]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.BuildResource]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.ContainerResource]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.PackageResource]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.PipelineResource]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.RepositoryResource]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.WebhookFilter]:
		return true, listWalk(ctx, r, node, env)
	case *ast.List[*ast.WebhookResource]:
		return true, listWalk(ctx, r, node, env)
	}
	return false, nil
}

func (r *resolver) walkStruct(ctx context.Context, value reflect.Value, env *expr.Object, depth int) error {
	var expandedTaskRef *ast.TaskStep
	if value.CanAddr() {
		if task, ok := value.Addr().Interface().(*ast.TaskStep); ok && hasExpandable(task.Task) {
			expandedTaskRef = task
		}
	}
	self := r.takeFrame(value)
	if err := r.structInsertKeys(ctx, value, env, self); err != nil {
		return err
	}
	if err := r.structInserts(ctx, value, env, self); err != nil {
		return err
	}
	kind := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if !field.CanInterface() {
			continue
		}
		name := kind.Field(i).Name
		if name == "Pos" || name == "Inserts" {
			continue
		}
		if field.Kind() == reflect.Pointer && !field.IsNil() {
			if d := nodeInsertion(field); d != nil && d.Kind == ast.DirectiveRuntime {
				if !runtimeInsertionAllowed(value, name) {
					r.rejectRuntime(d)
				}
				continue
			}
		}
		if field.Kind() == reflect.Pointer && !field.IsNil() && hasNodeInsertion(field) {
			if err := r.materializeField(ctx, value, i, env); err != nil {
				return err
			}
			if field.IsNil() {
				continue
			}
		}
		if enum, ok := field.Interface().(*ast.Enum); ok {
			if err := r.expandEnum(ctx, enum, enumSets[self.name+"."+name], name, env); err != nil {
				return err
			}
			continue
		}
		r.next = r.frameFor(self, kind.Field(i), field)
		var err error
		if matrix, ok := field.Interface().(*ast.Map[*ast.Map[*ast.Scalar]]); ok && matrix != nil {
			err = mapWalk(ctx, r, matrix, env, matrixMapOps(r), matrixRuntimeInsertionAllowed(value, name))
		} else {
			err = r.walk(ctx, field, env, depth+1)
		}
		r.next = nil
		if err != nil {
			return err
		}
	}
	if expandedTaskRef != nil {
		r.rederiveTaskRef(expandedTaskRef)
	}
	return nil
}

// rederiveTaskRef refreshes the parsed metadata after a compile-time
// directive supplied the task reference text. The parser intentionally defers
// validation of dynamic references until their text is known.
func (r *resolver) rederiveTaskRef(task *ast.TaskStep) {
	raw := strings.TrimSpace(task.Task.Value)
	if strings.Contains(raw, "$(") {
		task.Ref = ast.TaskRef{Name: raw, Major: -1, Dynamic: true, Valid: true}
		return
	}
	ref := ast.TaskRef{Major: -1}
	at := strings.LastIndex(raw, "@")
	if at > 0 && at < len(raw)-1 && !strings.ContainsAny(raw, " \t") {
		name, version := raw[:at], raw[at+1:]
		if resolvedTaskVersionPattern.MatchString(version) {
			major, _, _ := strings.Cut(version, ".")
			if number, err := strconv.Atoi(major); err == nil {
				task.Ref = ast.TaskRef{
					Name: name, IsGUID: resolvedTaskGUIDPattern.MatchString(name), Version: version,
					Major: number, Valid: true,
				}
				return
			}
		}
		ref.Name = name
	}
	task.Ref = ref
	r.addAt("TRL-PARSE-0015", task.Task.Pos, fmt.Sprintf("task reference %q is not Name@Version or GUID@Version", raw), map[string]any{"reference": raw})
}

// hasNodeInsertion reports a typed node (not a list, mapping or variables
// block, which expand their own insertion) written as one ${{ expr }}.
func hasNodeInsertion(field reflect.Value) bool {
	if _, ok := field.Interface().(*ast.Extends); ok {
		return false
	}
	return compileTime(nodeInsertion(field))
}

// nodeInsertion returns the whole-value insertion of a typed node field, or
// nil. Variables, lists and mappings expand their own insertion.
func nodeInsertion(field reflect.Value) *ast.Directive {
	switch field.Interface().(type) {
	case *ast.Variables, *ast.Any, *ast.Scalar:
		return nil
	}
	node := field.Elem()
	if node.Kind() != reflect.Struct {
		return nil
	}
	name := node.Type().Name()
	if strings.HasPrefix(name, "List[") || strings.HasPrefix(name, "Map[") {
		return nil
	}
	insertable := node.FieldByName("Insertable")
	if !insertable.IsValid() {
		return nil
	}
	insertion, _ := insertable.FieldByName("Insertion").Interface().(*ast.Directive)
	return insertion
}

// runtimeInsertionAllowed reports the only typed-node positions where Azure
// documents a whole-value runtime expression: a job or deployment container.
func runtimeInsertionAllowed(parent reflect.Value, field string) bool {
	if field != "Container" || !parent.CanAddr() {
		return false
	}
	switch parent.Addr().Interface().(type) {
	case *ast.JobDef, *ast.Deployment, *ast.Pipeline:
		// A pipeline with no jobs is one implicit job; its root container is
		// the job container (documented in container-phases).
		return true
	}
	return false
}

// matrixRuntimeInsertionAllowed identifies the Strategy.Matrix field itself.
// Maps of the same Go type in all other positions remain compile-time values.
func matrixRuntimeInsertionAllowed(parent reflect.Value, field string) bool {
	if field != "Matrix" || !parent.CanAddr() {
		return false
	}
	_, ok := parent.Addr().Interface().(*ast.Strategy)
	return ok
}

// rejectRuntime reports a whole-value runtime expression in a position that is
// resolved at compile time. It returns true when d was such an expression.
func (r *resolver) rejectRuntime(d *ast.Directive) bool {
	if d == nil || d.Kind != ast.DirectiveRuntime {
		return false
	}
	r.addAt("TRL-EXPR-0005", d.Pos, "runtime expression not allowed here; this value is resolved at compile time", nil)
	return true
}

// compileTime reports a whole-value insertion that the resolver must expand.
// Runtime $[ ] directives are left for Azure to evaluate.
func compileTime(d *ast.Directive) bool {
	return d != nil && d.Kind != ast.DirectiveRuntime
}

// ---------------------------------------------------------------------------
// Scalars.

func hasExpandable(s *ast.Scalar) bool {
	if s == nil {
		return false
	}
	for _, directive := range s.Directives {
		if directive.Kind != ast.DirectiveRuntime {
			return true
		}
	}
	return false
}

// interpolate renders the embedded ${{ }} occurrences of a scalar.
func (r *resolver) interpolate(ctx context.Context, s *ast.Scalar, env *expr.Object) (string, bool, error) {
	var out strings.Builder
	previous, changed, charged := 0, false, 0
	for _, directive := range s.Directives {
		if directive.Kind != ast.DirectiveInsert || directive.Start < previous || directive.End > len(s.Value) || directive.End < directive.Start {
			continue
		}
		changed = true
		out.WriteString(s.Value[previous:directive.Start])
		value, ok, err := r.evalOK(ctx, directive, env)
		if err != nil {
			return "", false, err
		}
		if ok {
			if text, scalar := scalarText(value); scalar {
				out.WriteString(text)
			} else {
				r.addAt("TRL-EXPR-0005", directive.Pos, "Array or Object is not convertible to String in an embedded expression", nil)
			}
		}
		previous = directive.End
		// Charge only what this directive added, not the running total.
		if err := r.charge(0, out.Len()-charged); err != nil {
			return "", false, err
		}
		charged = out.Len()
	}
	if !changed {
		return s.Value, false, nil
	}
	out.WriteString(s.Value[previous:])
	return out.String(), true, nil
}

// expandScalar evaluates the compile-time directives of one scalar in place.
func (r *resolver) expandScalar(ctx context.Context, s *ast.Scalar, env *expr.Object) error {
	if s == nil || len(s.Directives) == 0 {
		return nil
	}
	whole := s.Whole()
	if whole != nil && whole.Kind == ast.DirectiveRuntime {
		return nil
	}
	if whole != nil && whole.Kind == ast.DirectiveInsert {
		value, ok, err := r.evalOK(ctx, whole, env)
		if err != nil {
			return err
		}
		s.Directives = nil
		if !ok {
			return nil
		}
		text, scalar := scalarText(value)
		if !scalar {
			r.addAt("TRL-EXPR-0005", whole.Pos, "non-scalar expression cannot fill a scalar", nil)
			return nil
		}
		s.Value, s.Null = text, false
		return r.charge(0, len(text))
	}
	for _, directive := range s.Directives {
		if directive.Kind.IsKey() {
			r.addAt("TRL-EXPR-0001", directive.Pos, "directive not allowed inline", nil)
		}
	}
	text, _, err := r.interpolate(ctx, s, env)
	if err != nil {
		return err
	}
	s.Value, s.Null, s.Directives = text, false, nil
	return nil
}

func (r *resolver) expandBool(ctx context.Context, b *ast.Bool, env *expr.Object) error {
	had := hasExpandable(&b.Scalar)
	if err := r.expandScalar(ctx, &b.Scalar, env); err != nil || !had {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(b.Value)) {
	case "true":
		b.Value, b.Val, b.Known = "true", true, true
	case "false":
		b.Value, b.Val, b.Known = "false", false, true
	default:
		b.Val, b.Known = false, false
		if !b.Null && !strings.Contains(b.Value, "$(") && !strings.Contains(b.Value, "$[") {
			r.addAt("TRL-PARSE-0012", b.Pos, fmt.Sprintf("%q is not a boolean", b.Value), nil)
		}
	}
	return nil
}

// enumSets are the closed value sets of the Enum fields, per owning struct and
// field; they mirror the sets the parser validates literal values against.
var enumSets = map[string][]string{
	"Pipeline.LockBehavior":   {"sequential", "runLatest"},
	"StageDef.LockBehavior":   {"sequential", "runLatest"},
	"StageDef.Trigger":        {"manual", "automatic"},
	"Workspace.Clean":         {"outputs", "resources", "all"},
	"Target.Commands":         {"any", "restricted"},
	"RepositoryResource.Type": {"git", "github", "githubenterprise", "bitbucket"},
	"Parameter.Type": {
		"string", "number", "boolean", "object", "step", "stepList", "job", "jobList",
		"deployment", "deploymentList", "stage", "stageList", "stringList",
	},
}

// expandEnum expands an enum written with ${{ }} and validates the result
// against the field's own set (nil when the owner is unknown: nothing to check).
func (r *resolver) expandEnum(ctx context.Context, e *ast.Enum, allowed []string, field string, env *expr.Object) error {
	if e == nil {
		return nil
	}
	had := hasExpandable(&e.Scalar)
	if err := r.expandScalar(ctx, &e.Scalar, env); err != nil || !had {
		return err
	}
	e.Canon = ""
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(e.Value), candidate) {
			e.Canon = candidate
			return nil
		}
	}
	if allowed != nil && !e.Null && !strings.Contains(e.Value, "$(") && !strings.Contains(e.Value, "$[") {
		r.addAt("TRL-PARSE-0012", e.Pos, fmt.Sprintf("%s: %q is not one of %s", field, e.Value, strings.Join(allowed, ", ")),
			map[string]any{"key": field, "value": e.Value, "allowed": strings.Join(allowed, ", ")})
	}
	return nil
}

func (r *resolver) expandExpression(ctx context.Context, e *ast.Expression, env *expr.Object) error {
	had := hasExpandable(&e.Scalar)
	if err := r.expandScalar(ctx, &e.Scalar, env); err != nil || !had {
		return err
	}
	e.Expr, e.ExprSources = nil, nil
	if strings.TrimSpace(e.Value) == "" {
		return nil
	}
	parsed, err := expr.Parse(e.Value)
	if err != nil {
		r.addAt("TRL-EXPR-0001", e.Pos, "expanded expression is invalid: "+err.Error(), nil)
		return nil //nolint:nilerr // the diagnostic is the public failure channel
	}
	e.Expr = parsed
	// The expanded text no longer maps to file offsets; every node reports the
	// place the expression was written.
	e.ExprSources = e.ExprSources[:0]
	expr.Walk(expr.VisitorFunc(func(node expr.Expr) {
		e.ExprSources = append(e.ExprSources, ast.ExprSource{Span: node.Range(), Source: e.Position()})
	}), parsed)
	return nil
}

// ---------------------------------------------------------------------------
// Typed mapping bodies (Tail.Inserts).

func (r *resolver) structInserts(ctx context.Context, value reflect.Value, env *expr.Object, self *frame) error {
	inserts := value.FieldByName("Inserts")
	if !inserts.IsValid() || inserts.Kind() != reflect.Slice || inserts.Len() == 0 {
		return nil
	}
	branches := inserts.Interface()
	count := inserts.Len()
	directive := func(i int) *ast.Directive {
		branch := reflect.ValueOf(branches).Index(i)
		if branch.Kind() == reflect.Pointer {
			branch = branch.Elem()
		}
		if !branch.IsValid() {
			return nil
		}
		d, _ := branch.FieldByName("Directive").Interface().(*ast.Directive)
		return d
	}
	body := func(i int) reflect.Value {
		branch := reflect.ValueOf(branches).Index(i)
		if branch.Kind() == reflect.Pointer {
			branch = branch.Elem()
		}
		return branch.FieldByName("Body")
	}
	inserts.Set(reflect.Zero(inserts.Type()))
	return r.chain(ctx, env, chainOps{
		n:      count,
		noEach: true,
		at:     func(i int) (*ast.Directive, *ast.Directive) { return directive(i), nil },
		size:   func(int) int { return 0 },
		plain:  func(int) error { return nil },
		body: func(i int, bodyEnv *expr.Object, _ bool) error {
			b := body(i)
			if b.Kind() != reflect.Pointer || b.IsNil() {
				return nil
			}
			if err := r.structInsertKeys(ctx, b.Elem(), bodyEnv, self); err != nil {
				return err
			}
			if err := r.structInserts(ctx, b.Elem(), bodyEnv, self); err != nil {
				return err
			}
			mergeStruct(value, b.Elem())
			return nil
		},
	})
}

func hasPosField(t reflect.Type) bool {
	_, ok := t.FieldByName("Pos")
	return ok
}

func mergeStruct(destination, source reflect.Value) {
	for i := 0; i < source.NumField(); i++ {
		field := source.Field(i)
		target := destination.Field(i)
		name := source.Type().Field(i).Name
		if !target.CanSet() || name == "Pos" || name == "Inserts" {
			continue
		}
		switch field.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Slice:
			if !field.IsNil() {
				switch {
				case field.Kind() == reflect.Pointer && target.Kind() == reflect.Pointer && !target.IsNil() &&
					field.Elem().Kind() == reflect.Struct && !hasPosField(field.Elem().Type()):
					mergeStruct(target.Elem(), field.Elem())
				case field.Kind() == reflect.Slice && target.Kind() == reflect.Slice:
					target.Set(reflect.AppendSlice(target, field))
				default:
					target.Set(field)
				}
			}
		case reflect.Struct:
			mergeStruct(target, field)
		default:
			if !field.IsZero() {
				target.Set(field)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Shared list expansion.

type itemOps[T any] struct {
	expand    func(ctx context.Context, item T, env *expr.Object) ([]T, error)
	fromValue func(ctx context.Context, value expr.Value, pos ast.Pos) ([]T, error)
}

func listWalk[T any](ctx context.Context, r *resolver, list *ast.List[T], env *expr.Object) error {
	items, err := listItems(ctx, r, list, env, opsFor[T](r))
	if err != nil {
		return err
	}
	list.Items = valuesToItems(items)
	if compileTime(list.Insertion) {
		list.Insertion = nil
	}
	return nil
}

func listItems[T any](ctx context.Context, r *resolver, list *ast.List[T], env *expr.Object, ops itemOps[T]) ([]T, error) {
	var out []T
	if list == nil {
		return nil, nil
	}
	add := func(values []T) error {
		for _, value := range values {
			expanded, err := ops.expand(ctx, value, env)
			if err != nil {
				return err
			}
			out = append(out, expanded...)
		}
		return nil
	}
	// A rejected runtime insertion is reported and skipped; the remaining
	// siblings are still walked so their independent errors are reported too.
	// The rejection already makes resolution fail, so the partial result is
	// never used.
	r.rejectRuntime(list.Insertion)
	rejected := make([]bool, len(list.Items))
	for i, item := range list.Items {
		rejected[i] = r.rejectRuntime(item.Insert)
	}
	if compileTime(list.Insertion) {
		value, ok, err := r.evalOK(ctx, list.Insertion, env)
		if err != nil {
			return nil, err
		}
		if ok {
			values, err := ops.fromValue(ctx, value, list.Insertion.Pos)
			if err != nil {
				return nil, err
			}
			if err := add(values); err != nil {
				return nil, err
			}
		}
	}
	err := r.chain(ctx, env, chainOps{
		n: len(list.Items),
		at: func(i int) (*ast.Directive, *ast.Directive) {
			if cond := list.Items[i].Cond; cond != nil {
				return cond.Directive, nil
			}
			if rejected[i] {
				return nil, nil
			}
			return nil, list.Items[i].Insert
		},
		size: func(i int) int { return approximateSize(reflect.ValueOf(list.Items[i].Cond), 0) },
		plain: func(i int) error {
			item := list.Items[i].Value
			if reflect.ValueOf(&item).Elem().IsZero() {
				return nil
			}
			return add([]T{item})
		},
		insert: func(_ int, value expr.Value, d *ast.Directive) error {
			values, err := ops.fromValue(ctx, value, d.Pos)
			if err != nil {
				return err
			}
			return add(values)
		},
		body: func(i int, bodyEnv *expr.Object, repeated bool) error {
			cond := list.Items[i].Cond
			switch {
			case cond.Items != nil:
				src := cond.Items
				if repeated {
					src = clone(src)
				}
				nested, err := listItems(ctx, r, src, bodyEnv, ops)
				out = append(out, nested...)
				return err
			case cond.Other != nil:
				other := cond.Other
				if repeated {
					other = clone(other)
				}
				value, err := r.resolveAny(ctx, other, bodyEnv)
				if err != nil {
					return err
				}
				values, err := ops.fromValue(ctx, value, other.Pos)
				if err != nil {
					return err
				}
				return add(values)
			}
			return nil
		},
	})
	return out, err
}

func opsFor[T any](r *resolver) itemOps[T] {
	var generic any
	switch reflect.TypeFor[T]() {
	case reflect.TypeFor[ast.Step]():
		generic = itemOps[ast.Step]{expand: r.expandStep, fromValue: materializer(r, "steps", func(p *ast.Pipeline) *ast.List[ast.Step] { return p.Steps })}
	case reflect.TypeFor[ast.Job]():
		generic = itemOps[ast.Job]{expand: r.expandJob, fromValue: materializer(r, "jobs", func(p *ast.Pipeline) *ast.List[ast.Job] { return p.Jobs })}
	case reflect.TypeFor[ast.Stage]():
		generic = itemOps[ast.Stage]{expand: r.expandStage, fromValue: materializer(r, "stages", func(p *ast.Pipeline) *ast.List[ast.Stage] { return p.Stages })}
	case reflect.TypeFor[ast.Variable]():
		generic = itemOps[ast.Variable]{expand: r.expandVariable, fromValue: materializer(r, "variables", func(p *ast.Pipeline) *ast.List[ast.Variable] {
			if p.Variables == nil {
				return nil
			}
			return p.Variables.List
		})}
	case reflect.TypeFor[*ast.Scalar]():
		generic = itemOps[*ast.Scalar]{
			expand: func(ctx context.Context, item *ast.Scalar, env *expr.Object) ([]*ast.Scalar, error) {
				return []*ast.Scalar{item}, r.expandScalar(ctx, item, env)
			},
			fromValue: r.scalarItems,
		}
	case reflect.TypeFor[*ast.RepositoryResource]():
		generic = itemOps[*ast.RepositoryResource]{
			expand: func(ctx context.Context, item *ast.RepositoryResource, env *expr.Object) ([]*ast.RepositoryResource, error) {
				return []*ast.RepositoryResource{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
			},
			fromValue: materializer(r, "resources:\n  repositories", func(p *ast.Pipeline) *ast.List[*ast.RepositoryResource] {
				if p.Resources == nil {
					return nil
				}
				return p.Resources.Repositories
			}),
		}
	}
	if generic != nil {
		return generic.(itemOps[T])
	}
	return itemOps[T]{
		expand: func(ctx context.Context, item T, env *expr.Object) ([]T, error) {
			return []T{item}, r.walk(ctx, reflect.ValueOf(item), env, 0)
		},
		fromValue: func(_ context.Context, _ expr.Value, pos ast.Pos) ([]T, error) {
			r.addAt("TRL-EXPR-0005", pos, "expression cannot be inserted into this list", nil)
			return nil, nil
		},
	}
}

func (r *resolver) scalarItems(_ context.Context, value expr.Value, pos ast.Pos) ([]*ast.Scalar, error) {
	items := []expr.Value{value}
	origin := r.originOf(value, pos)
	if array, ok := value.(*expr.Array); ok {
		items = array.Items
	}
	out := make([]*ast.Scalar, 0, len(items))
	for _, item := range items {
		text, ok := scalarText(item)
		if !ok {
			r.addAt("TRL-EXPR-0005", pos, "only scalar values can be inserted into this list", nil)
			continue
		}
		out = append(out, &ast.Scalar{Pos: origin, Value: text, Null: item == expr.Value(expr.Null{})})
	}
	return out, r.charge(len(out), 0)
}

func valuesToItems[T any](values []T) []ast.ListItem[T] {
	out := make([]ast.ListItem[T], len(values))
	for i, value := range values {
		out[i].Value = value
	}
	return out
}

func listValues[T any](list *ast.List[T]) []T {
	if list == nil {
		return nil
	}
	out := make([]T, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Cond == nil && item.Insert == nil {
			out = append(out, item.Value)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Shared mapping expansion.

type mapOps[V any] struct {
	value     func(ctx context.Context, v V, env *expr.Object) (V, error)
	eval      func(ctx context.Context, v V, env *expr.Object) (expr.Value, bool, error)
	from      func(ctx context.Context, name string, value expr.Value, pos ast.Pos) (V, bool, error)
	insertion func(v V) *ast.Directive
}

func mapWalk[V any](ctx context.Context, r *resolver, m *ast.Map[V], env *expr.Object, ops mapOps[V], runtimeAllowed bool) error {
	entries, err := mapEntries(ctx, r, m, env, ops, runtimeAllowed)
	if err != nil {
		return err
	}
	m.Entries = entries
	if compileTime(m.Insertion) {
		m.Insertion = nil
	}
	return nil
}

func mapEntries[V any](ctx context.Context, r *resolver, m *ast.Map[V], env *expr.Object, ops mapOps[V], runtimeAllowed bool) ([]ast.MapEntry[V], error) {
	var out []ast.MapEntry[V]
	if m == nil {
		return nil, nil
	}
	fill := func(value expr.Value, pos ast.Pos) error {
		object, ok := value.(*expr.Object)
		if !ok {
			r.addAt("TRL-EXPR-0005", pos, "insert requires a mapping", nil)
			return nil
		}
		for _, property := range object.Properties {
			entry, ok, err := ops.from(ctx, property.Name, property.Value, r.originOf(property.Value, pos))
			if err != nil {
				return err
			}
			if ok {
				out = append(out, ast.MapEntry[V]{Key: &ast.Scalar{Pos: pos, Value: property.Name}, Value: entry})
			}
		}
		return r.charge(len(object.Properties), 0)
	}
	if !runtimeAllowed && r.rejectRuntime(m.Insertion) {
		return nil, nil
	}
	if compileTime(m.Insertion) {
		value, ok, err := r.evalOK(ctx, m.Insertion, env)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := fill(value, m.Insertion.Pos); err != nil {
				return nil, err
			}
		}
	}
	err := r.chain(ctx, env, chainOps{
		n: len(m.Entries),
		at: func(i int) (*ast.Directive, *ast.Directive) {
			if cond := m.Entries[i].Cond; cond != nil {
				return cond.Directive, nil
			}
			return nil, nil
		},
		size: func(i int) int { return approximateSize(reflect.ValueOf(m.Entries[i].Cond), 0) },
		plain: func(i int) error {
			entry := m.Entries[i]
			if entry.Key == nil {
				return nil
			}
			if isInsertKey(entry.Key) {
				if ops.insertion != nil && r.rejectRuntime(ops.insertion(entry.Value)) {
					return nil
				}
				value, ok, err := ops.eval(ctx, entry.Value, env)
				if err != nil {
					return err
				}
				if ok {
					return fill(value, entry.Key.Pos)
				}
				return nil
			}
			if err := r.expandScalar(ctx, entry.Key, env); err != nil {
				return err
			}
			resolved, err := ops.value(ctx, entry.Value, env)
			if err != nil {
				return err
			}
			entry.Value = resolved
			out = append(out, entry)
			return nil
		},
		body: func(i int, bodyEnv *expr.Object, repeated bool) error {
			body := m.Entries[i].Cond.Body
			if repeated {
				body = clone(body)
			}
			nested, err := mapEntries(ctx, r, body, bodyEnv, ops, false)
			out = append(out, nested...)
			return err
		},
	})
	if err != nil {
		return out, err
	}
	return dedupeEntries(r, out), nil
}

// dedupeEntries applies last-wins to keys that expansion produced twice: the
// first position keeps the last value, and a warning names the duplicate. Azure
// rejects duplicate keys in the source, but only expansion can create them here
// and an AST with a repeated key could not be printed. VERIFY against Azure.
func dedupeEntries[V any](r *resolver, entries []ast.MapEntry[V]) []ast.MapEntry[V] {
	index := make(map[string]int, len(entries))
	var out []ast.MapEntry[V]
	for _, entry := range entries {
		if entry.Key == nil || entry.Cond != nil {
			out = append(out, entry)
			continue
		}
		if i, seen := index[entry.Key.Value]; seen {
			r.addAt("TRL-TPL-0010", entry.Key.Pos, "duplicate key "+entry.Key.Value+" produced by expansion; the last value wins", map[string]any{"key": entry.Key.Value})
			out[i].Value = entry.Value
			continue
		}
		index[entry.Key.Value] = len(out)
		out = append(out, entry)
	}
	return out
}

func scalarMapOps(r *resolver) mapOps[*ast.Scalar] {
	return mapOps[*ast.Scalar]{
		value: func(ctx context.Context, v *ast.Scalar, env *expr.Object) (*ast.Scalar, error) {
			return v, r.expandScalar(ctx, v, env)
		},
		eval: func(ctx context.Context, v *ast.Scalar, env *expr.Object) (expr.Value, bool, error) {
			if whole := v.Whole(); whole != nil && whole.Kind == ast.DirectiveInsert {
				return r.evalOK(ctx, whole, env)
			}
			if v != nil {
				r.addAt("TRL-EXPR-0005", v.Pos, "insert requires an expression", nil)
			}
			return nil, false, nil
		},
		from: func(_ context.Context, _ string, value expr.Value, pos ast.Pos) (*ast.Scalar, bool, error) {
			text, ok := scalarText(value)
			if !ok {
				r.addAt("TRL-EXPR-0005", pos, "only scalar values can be inserted into this mapping", nil)
				return nil, false, nil
			}
			_, null := value.(expr.Null)
			return &ast.Scalar{Pos: pos, Value: text, Null: null}, true, nil
		},
	}
}

func anyMapOps(r *resolver) mapOps[*ast.Any] {
	return mapOps[*ast.Any]{
		value: func(ctx context.Context, v *ast.Any, env *expr.Object) (*ast.Any, error) {
			return v, r.expandAny(ctx, v, env)
		},
		eval: func(ctx context.Context, v *ast.Any, env *expr.Object) (expr.Value, bool, error) {
			value, err := r.resolveAny(ctx, v, env)
			return value, err == nil, err
		},
		from: func(ctx context.Context, _ string, value expr.Value, pos ast.Pos) (*ast.Any, bool, error) {
			converted, err := r.valueToAny(ctx, value, pos)
			return converted, err == nil, err
		},
	}
}

func matrixMapOps(r *resolver) mapOps[*ast.Map[*ast.Scalar]] {
	return mapOps[*ast.Map[*ast.Scalar]]{
		value: func(ctx context.Context, v *ast.Map[*ast.Scalar], env *expr.Object) (*ast.Map[*ast.Scalar], error) {
			if v == nil {
				return v, nil
			}
			return v, mapWalk(ctx, r, v, env, scalarMapOps(r), false)
		},
		eval: func(ctx context.Context, v *ast.Map[*ast.Scalar], env *expr.Object) (expr.Value, bool, error) {
			if v != nil && compileTime(v.Insertion) {
				return r.evalOK(ctx, v.Insertion, env)
			}
			return nil, false, nil
		},
		from: func(ctx context.Context, _ string, value expr.Value, pos ast.Pos) (*ast.Map[*ast.Scalar], bool, error) {
			object, ok := value.(*expr.Object)
			if !ok {
				r.addAt("TRL-EXPR-0005", pos, "matrix entries must be mappings", nil)
				return nil, false, nil
			}
			mapped, err := r.scalarMapFromObject(ctx, object, pos)
			return mapped, err == nil, err
		},
		insertion: func(v *ast.Map[*ast.Scalar]) *ast.Directive {
			if v == nil {
				return nil
			}
			return v.Insertion
		},
	}
}

func (r *resolver) scalarMapFromObject(ctx context.Context, object *expr.Object, pos ast.Pos) (*ast.Map[*ast.Scalar], error) {
	out := &ast.Map[*ast.Scalar]{Pos: pos}
	scalars := scalarMapOps(r)
	for _, property := range object.Properties {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, ok, err := scalars.from(ctx, property.Name, property.Value, pos)
		if err != nil {
			return nil, err
		}
		if ok {
			out.Entries = append(out.Entries, ast.MapEntry[*ast.Scalar]{Key: &ast.Scalar{Pos: pos, Value: property.Name}, Value: entry})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Free-form values.

func needsExpansion(node *ast.Any) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.AnyScalar:
		return hasExpandable(node.Scalar)
	case ast.AnySeq:
		for _, item := range node.Items {
			if item != nil && (isCondItem(item) || needsExpansion(item)) {
				return true
			}
		}
	case ast.AnyMap:
		for _, entry := range node.Entries {
			if entry == nil {
				continue
			}
			if entry.Directive != nil || hasExpandable(entry.Key) || needsExpansion(entry.Value) {
				return true
			}
		}
	}
	return false
}

func isCondItem(item *ast.Any) bool {
	return item != nil && item.Kind == ast.AnyMap && len(item.Entries) == 1 && item.Entries[0] != nil && item.Entries[0].Directive != nil
}

func (r *resolver) expandAny(ctx context.Context, node *ast.Any, env *expr.Object) error {
	if !needsExpansion(node) {
		return nil
	}
	value, err := r.resolveAnyMode(ctx, node, env, false)
	if err != nil {
		return err
	}
	converted, err := r.valueToAny(ctx, value, node.Pos)
	if err != nil {
		return err
	}
	*node = *converted
	return nil
}

func (r *resolver) resolveAny(ctx context.Context, node *ast.Any, env *expr.Object) (expr.Value, error) {
	return r.resolveAnyMode(ctx, node, env, false)
}

type objectBuilder struct {
	object *expr.Object
	index  map[string]int
	// duplicate is told about a key set twice; the last value wins.
	duplicate func(name string)
}

func (b *objectBuilder) set(name string, value expr.Value) {
	if i, ok := b.index[name]; ok {
		if b.duplicate != nil {
			b.duplicate(name)
		}
		b.object.Properties[i].Value = value
		return
	}
	b.index[name] = len(b.object.Properties)
	b.object.Properties = append(b.object.Properties, expr.Property{Name: name, Value: value})
}

// resolveAnyMode evaluates a free-form value. With infer set, literal scalars
// that read as booleans or numbers become typed values; otherwise literals are
// kept as text.
func (r *resolver) resolveAnyMode(ctx context.Context, node *ast.Any, env *expr.Object, infer bool) (expr.Value, error) {
	if node == nil {
		return expr.Null{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch node.Kind {
	case ast.AnyScalar:
		return r.resolveAnyScalar(ctx, node, env, infer)
	case ast.AnySeq:
		result := &expr.Array{}
		r.record(result, node.Pos)
		err := r.chain(ctx, env, chainOps{
			n: len(node.Items),
			at: func(i int) (*ast.Directive, *ast.Directive) {
				if isCondItem(node.Items[i]) {
					return node.Items[i].Entries[0].Directive, nil
				}
				return nil, nil
			},
			size: func(i int) int { return approximateSize(reflect.ValueOf(node.Items[i].Entries[0].Value), 0) },
			plain: func(i int) error {
				item := node.Items[i]
				value, err := r.resolveAnyMode(ctx, item, env, infer)
				if err != nil {
					return err
				}
				flatten := false
				if item != nil && item.Kind == ast.AnyScalar && item.Scalar != nil {
					if whole := item.Scalar.Whole(); whole != nil && whole.Kind == ast.DirectiveInsert {
						flatten = true
					}
				}
				return r.appendAny(result, value, flatten)
			},
			body: func(i int, bodyEnv *expr.Object, repeated bool) error {
				body := node.Items[i].Entries[0].Value
				if repeated {
					body = clone(body)
				}
				value, err := r.resolveAnyMode(ctx, body, bodyEnv, infer)
				if err != nil {
					return err
				}
				return r.appendAny(result, value, true)
			},
		})
		return result, err
	case ast.AnyMap:
		builder := &objectBuilder{object: &expr.Object{}, index: map[string]int{}}
		builder.duplicate = func(name string) {
			r.addAt("TRL-TPL-0010", node.Pos, "duplicate key "+name+" produced by expansion; the last value wins", map[string]any{"key": name})
		}
		r.record(builder.object, node.Pos)
		merge := func(value expr.Value, pos ast.Pos) {
			object, ok := value.(*expr.Object)
			if !ok {
				r.addAt("TRL-EXPR-0005", pos, "insert requires a mapping", nil)
				return
			}
			for _, property := range object.Properties {
				builder.set(property.Name, property.Value)
			}
		}
		err := r.chain(ctx, env, chainOps{
			n: len(node.Entries),
			at: func(i int) (*ast.Directive, *ast.Directive) {
				if entry := node.Entries[i]; entry != nil {
					return entry.Directive, nil
				}
				return nil, nil
			},
			size: func(i int) int { return approximateSize(reflect.ValueOf(node.Entries[i].Value), 0) },
			plain: func(i int) error {
				entry := node.Entries[i]
				if entry == nil || entry.Key == nil {
					return nil
				}
				if isInsertKey(entry.Key) {
					value, err := r.resolveAnyMode(ctx, entry.Value, env, infer)
					if err != nil {
						return err
					}
					merge(value, entry.Key.Pos)
					return nil
				}
				name, err := r.keyText(ctx, entry.Key, env)
				if err != nil {
					return err
				}
				value, err := r.resolveAnyMode(ctx, entry.Value, env, infer)
				if err != nil {
					return err
				}
				builder.set(name, value)
				return r.charge(1, len(name))
			},
			body: func(i int, bodyEnv *expr.Object, repeated bool) error {
				entry := node.Entries[i]
				body := entry.Value
				if repeated {
					body = clone(body)
				}
				value, err := r.resolveAnyMode(ctx, body, bodyEnv, infer)
				if err != nil {
					return err
				}
				merge(value, entry.Directive.Pos)
				return nil
			},
		})
		return builder.object, err
	default:
		return expr.Null{}, nil
	}
}

func (r *resolver) resolveAnyScalar(ctx context.Context, node *ast.Any, env *expr.Object, infer bool) (expr.Value, error) {
	s := node.Scalar
	if s == nil {
		return expr.Null{}, nil
	}
	whole := s.Whole()
	if whole != nil && whole.Kind == ast.DirectiveRuntime {
		return expr.String(s.Value), nil
	}
	if whole != nil && whole.Kind == ast.DirectiveInsert {
		value, ok, err := r.evalOK(ctx, whole, env)
		if err != nil || !ok {
			return expr.Null{}, err
		}
		return value, nil
	}
	text, changed, err := r.interpolate(ctx, s, env)
	if err != nil {
		return nil, err
	}
	if changed {
		return expr.String(text), nil
	}
	if s.Null {
		return expr.Null{}, nil
	}
	if infer {
		return scalarLiteral(s), nil
	}
	return expr.String(s.Value), nil
}

// appendAny appends value to array, splicing arrays when flatten is set.
func (r *resolver) appendAny(array *expr.Array, value expr.Value, flatten bool) error {
	if nested, ok := value.(*expr.Array); ok && flatten {
		array.Items = append(array.Items, nested.Items...)
		return r.charge(len(nested.Items), 0)
	}
	array.Items = append(array.Items, value)
	return r.charge(1, 0)
}

// record remembers where, and in which file scope, a composite value was written.
func (r *resolver) record(value expr.Value, pos ast.Pos) {
	if r.origins == nil {
		r.origins = map[expr.Value]ast.Pos{}
	}
	if r.scopes == nil {
		r.scopes = map[expr.Value]scope{}
	}
	if len(pos.Chain) == 0 && len(r.scope.chain) > 0 {
		pos.Chain = r.scope.chain[:len(r.scope.chain):len(r.scope.chain)]
	}
	r.origins[value] = pos
	r.scopes[value] = r.scope
}

// scopeOf returns the file scope in which a composite value was written.
func (r *resolver) scopeOf(value expr.Value) (scope, bool) {
	switch value.(type) {
	case *expr.Array, *expr.Object:
		written, ok := r.scopes[value]
		return written, ok
	}
	return scope{}, false
}

// inWrittenScope makes a template reference that was materialized from data
// resolve relative to the file that wrote it, not the file it is inserted
// into. VERIFY against Azure. The returned func restores the scope.
func (r *resolver) inWrittenScope(node any) func() {
	written, ok := r.written[node]
	if !ok {
		return func() {}
	}
	saved := r.scope
	r.scope.repo, r.scope.file = written.repo, written.file
	return func() { r.scope = saved }
}

// originOf returns where a composite value was written, or fallback.
func (r *resolver) originOf(value expr.Value, fallback ast.Pos) ast.Pos {
	switch value.(type) {
	case *expr.Array, *expr.Object:
		if pos, ok := r.origins[value]; ok {
			return pos
		}
	}
	return fallback
}

// valueToAny converts an evaluated value to a free-form node, charging each
// created node so shared expression values cannot expand without bound.
func (r *resolver) valueToAny(ctx context.Context, value expr.Value, pos ast.Pos) (*ast.Any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.charge(1, 0); err != nil {
		return nil, err
	}
	pos = r.originOf(value, pos)
	switch value := value.(type) {
	case *expr.Array:
		out := &ast.Any{Pos: pos, Kind: ast.AnySeq}
		for _, item := range value.Items {
			child, err := r.valueToAny(ctx, item, pos)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, child)
		}
		return out, nil
	case *expr.Object:
		out := &ast.Any{Pos: pos, Kind: ast.AnyMap}
		for _, property := range value.Properties {
			child, err := r.valueToAny(ctx, property.Value, pos)
			if err != nil {
				return nil, err
			}
			out.Entries = append(out.Entries, &ast.AnyEntry{Pos: pos, Key: &ast.Scalar{Pos: pos, Value: property.Name}, Value: child})
		}
		return out, nil
	case expr.Null:
		return &ast.Any{Pos: pos, Kind: ast.AnyScalar, Scalar: &ast.Scalar{Pos: pos, Null: true}}, nil
	default:
		text, _ := scalarText(value)
		if err := r.charge(0, len(text)); err != nil {
			return nil, err
		}
		return &ast.Any{Pos: pos, Kind: ast.AnyScalar, Scalar: &ast.Scalar{Pos: pos, Value: text}}, nil
	}
}

// ---------------------------------------------------------------------------
// Whole-value insertion of typed nodes and typed lists.

// materializer returns a list-item builder that renders an evaluated value as
// YAML under the given root key and parses it back into typed nodes.
func materializer[T any](r *resolver, key string, pick func(*ast.Pipeline) *ast.List[T]) func(context.Context, expr.Value, ast.Pos) ([]T, error) {
	return func(ctx context.Context, value expr.Value, pos ast.Pos) ([]T, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items := []expr.Value{value}
		if array, ok := value.(*expr.Array); ok {
			items = array.Items
		}
		encoded, err := r.encode(&expr.Array{Items: items})
		if err != nil {
			r.addAt("TRL-EXPR-0005", pos, err.Error(), nil)
			return nil, nil
		}
		if err := r.charge(len(items)+1, len(encoded)); err != nil {
			return nil, err
		}
		pipeline, ok := r.parseSnippet(key, encoded, 1, pos)
		if !ok {
			return nil, nil
		}
		list := pick(pipeline)
		if list == nil {
			return nil, nil
		}
		values := make([]T, 0, len(list.Items))
		for i, item := range list.Items {
			if item.Cond != nil || item.Insert != nil {
				r.addAt("TRL-EXPR-0005", pos, "inserted "+key+" contain unexpanded directives", nil)
				return nil, nil
			}
			origin := pos
			written, hasScope := r.scopeOf(value)
			if i < len(items) {
				origin = r.originOf(items[i], r.originOf(value, pos))
				if own, ok := r.scopeOf(items[i]); ok {
					written, hasScope = own, true
				}
			}
			setPos(reflect.ValueOf(item.Value), origin)
			restoreScalars(reflect.ValueOf(item.Value))
			if hasScope {
				r.markWritten(item.Value, written)
			}
			values = append(values, item.Value)
		}
		return values, nil
	}
}

// openProtected stands in for "${{" while inserted data is parsed back. Azure
// does not expand an expression result a second time, so a "${{" inside data
// must stay literal: the parser never sees it, and restoreScalars puts it back.
const openProtected = "${{"

func (r *resolver) markWritten(node any, written scope) {
	if r.written == nil {
		r.written = map[any]scope{}
	}
	r.written[node] = written
}

// encode renders an evaluated value as YAML (JSON) text for re-parsing, with
// every "${{" protected from directive recognition.
func (r *resolver) encode(value expr.Value) (string, error) {
	encoded, err := expr.EncodeJSON(value, false)
	if err != nil {
		return "", fmt.Errorf("inserted value cannot be encoded: %w", err)
	}
	if strings.Contains(encoded, "") {
		return "", fmt.Errorf("inserted value contains a reserved private-use character")
	}
	return strings.ReplaceAll(encoded, "${{", openProtected), nil
}

// restoreScalars undoes encode's protection in every scalar below value.
func restoreScalars(value reflect.Value) {
	walkScalars(value, func(s *ast.Scalar) {
		if strings.Contains(s.Value, openProtected) {
			s.Value = strings.ReplaceAll(s.Value, openProtected, "${{")
		}
	})
}

func walkScalars(value reflect.Value, apply func(*ast.Scalar)) {
	if !value.IsValid() {
		return
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			walkScalars(value.Elem(), apply)
		}
	case reflect.Struct:
		if value.CanAddr() {
			if s, ok := value.Addr().Interface().(*ast.Scalar); ok {
				apply(s)
				return
			}
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Field(i).CanInterface() {
				walkScalars(value.Field(i), apply)
			}
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			walkScalars(value.Index(i), apply)
		}
	}
}

// parseSnippet parses a generated YAML document. Findings at or after errorLine
// make the insertion invalid.
func (r *resolver) parseSnippet(header, body string, errorLine int, pos ast.Pos) (*ast.Pipeline, bool) {
	return r.parseDocument(header+": "+body+"\n", errorLine, pos)
}

func (r *resolver) parseDocument(document string, errorLine int, pos ast.Pos) (*ast.Pipeline, bool) {
	private := diag.NewCollector()
	pipeline := parse.Source(pos.Source.File, []byte(document), private)
	for _, finding := range private.List() {
		if finding.Severity == diag.SeverityError && (finding.Source == nil || finding.Source.Line >= errorLine) {
			r.addAt("TRL-EXPR-0005", pos, "inserted value has an invalid shape: "+finding.Message, nil)
			return nil, false
		}
	}
	return pipeline, pipeline != nil
}

// materializeField replaces a typed node written as one ${{ expr }} (for
// example pool: ${{ parameters.pool }}) with the node the value describes.
func (r *resolver) materializeField(ctx context.Context, parent reflect.Value, index int, env *expr.Object) error {
	field := parent.Field(index)
	insertion, _ := field.Elem().FieldByName("Insertable").FieldByName("Insertion").Interface().(*ast.Directive)
	value, ok, err := r.evalOK(ctx, insertion, env)
	if err != nil {
		return err
	}
	field.Set(reflect.Zero(field.Type()))
	if !ok {
		return nil
	}
	name := parent.Type().Field(index).Name
	key := strings.ToLower(name[:1]) + name[1:]
	encoded, err := r.encode(value)
	if err != nil {
		r.addAt("TRL-EXPR-0005", insertion.Pos, err.Error(), nil)
		return nil
	}
	if err := r.charge(1, len(encoded)); err != nil {
		return err
	}
	var document string
	var holder func(*ast.Pipeline) reflect.Value
	errorLine := 1
	switch parent.Addr().Interface().(type) {
	case *ast.Pipeline:
		document = key + ": " + encoded + "\n"
		holder = func(p *ast.Pipeline) reflect.Value { return reflect.ValueOf(p).Elem() }
	case *ast.JobDef:
		document, errorLine = "jobs:\n- job: x\n  "+key+": "+encoded+"\n", 3
		holder = func(p *ast.Pipeline) reflect.Value { return firstItem(p.Jobs) }
	case *ast.Deployment:
		document, errorLine = "jobs:\n- deployment: x\n  "+key+": "+encoded+"\n", 3
		holder = func(p *ast.Pipeline) reflect.Value { return firstItem(p.Jobs) }
	case *ast.StageDef:
		document, errorLine = "stages:\n- stage: x\n  "+key+": "+encoded+"\n", 3
		holder = func(p *ast.Pipeline) reflect.Value { return firstItem(p.Stages) }
	default:
		r.addAt("TRL-EXPR-0005", insertion.Pos, "whole-value insertion is not supported for "+name+" here", nil)
		return nil
	}
	pipeline, ok := r.parseDocument(document, errorLine, insertion.Pos)
	if !ok {
		return nil
	}
	owner := holder(pipeline)
	if !owner.IsValid() {
		return nil
	}
	produced := owner.FieldByName(name)
	if !produced.IsValid() || produced.IsNil() {
		return nil
	}
	setPos(produced, r.originOf(value, insertion.Pos))
	restoreScalars(produced)
	field.Set(produced)
	return nil
}

func firstItem[T any](list *ast.List[T]) reflect.Value {
	if list == nil || len(list.Items) == 0 {
		return reflect.Value{}
	}
	value := reflect.ValueOf(list.Items[0].Value)
	if value.Kind() == reflect.Pointer {
		return value.Elem()
	}
	return value
}

// ---------------------------------------------------------------------------
// Positions.

// appendChain prefixes the call-site chain of every node below value. The chain
// slice is shared, never copied per node: it is immutable once built (capacity
// is clipped so an append can never write into it). Only a node that already
// carries call sites of its own needs a fresh slice.
//
// It returns the number of positions stamped so the caller can charge them.
func appendChain(value reflect.Value, chain []diag.Source) int {
	shared := chain[:len(chain):len(chain)]
	stamped := 0
	walkPos(value, func(pos *ast.Pos) {
		stamped++
		if len(pos.Chain) == 0 {
			pos.Chain = shared
			return
		}
		pos.Chain = append(append(make([]diag.Source, 0, len(chain)+len(pos.Chain)), chain...), pos.Chain...)
	})
	return stamped
}

// setPos stamps a position (source and the shared, immutable call-site chain)
// on every node.
func setPos(value reflect.Value, to ast.Pos) {
	shared := to.Chain
	if len(shared) > 0 {
		shared = shared[:len(shared):len(shared)]
	}
	walkPos(value, func(pos *ast.Pos) {
		pos.Source = to.Source
		pos.Chain = shared
	})
}

func walkPos(value reflect.Value, apply func(*ast.Pos)) {
	if !value.IsValid() {
		return
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			walkPos(value.Elem(), apply)
		}
	case reflect.Struct:
		if value.CanAddr() {
			if pos, ok := value.Addr().Interface().(*ast.Pos); ok {
				apply(pos)
				return
			}
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Field(i).CanInterface() {
				walkPos(value.Field(i), apply)
			}
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			walkPos(value.Index(i), apply)
		}
	}
}

// overlayPipeline copies the fields the extending file defines over base.
// Pos and Parameters stay with the extending file; the caller restores them.
func overlayPipeline(base, overlay *ast.Pipeline) {
	destination, source := reflect.ValueOf(base).Elem(), reflect.ValueOf(overlay).Elem()
	for i := 0; i < source.NumField(); i++ {
		name := source.Type().Field(i).Name
		if name == "Pos" || name == "Shape" || name == "Extends" || name == "Parameters" {
			continue
		}
		value := source.Field(i)
		if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface || value.Kind() == reflect.Slice) && !value.IsNil() {
			destination.Field(i).Set(value)
		}
	}
	base.Extends = nil
	if overlay.Shape != ast.ShapeExtends {
		base.Shape = overlay.Shape
	}
}

// approximateSize estimates the byte size of a node. Past the depth bound it
// answers with a cost that always trips the budget instead of undercounting.
func approximateSize(value reflect.Value, depth int) int {
	if !value.IsValid() {
		return 0
	}
	if depth > maxWalkDepth {
		return 1 << 30
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0
		}
		return approximateSize(value.Elem(), depth+1)
	}
	switch value.Kind() {
	case reflect.String:
		return value.Len()
	case reflect.Slice:
		total := 0
		for i := 0; i < value.Len(); i++ {
			total += approximateSize(value.Index(i), depth+1)
		}
		return total
	case reflect.Struct:
		total := 0
		for i := 0; i < value.NumField(); i++ {
			if value.Field(i).CanInterface() {
				total += approximateSize(value.Field(i), depth+1)
			}
		}
		return total
	default:
		return int(value.Type().Size())
	}
}
