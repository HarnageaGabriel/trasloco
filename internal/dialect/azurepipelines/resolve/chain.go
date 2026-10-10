package resolve

import (
	"context"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// chainOps adapts one container (list, mapping, free-form value, typed
// branch list) to the single if/elseif/else/each/insert implementation.
type chainOps struct {
	n int
	// at returns the key directive or the whole-value insert of entry i, or
	// two nils for a plain entry.
	at func(i int) (cond, insert *ast.Directive)
	// plain handles a plain entry.
	plain func(i int) error
	// insert handles the value of a whole-value insert entry.
	insert func(i int, value expr.Value, d *ast.Directive) error
	// body expands the body of entry i. repeated is true for each iterations,
	// whose body must be cloned before use.
	body func(i int, env *expr.Object, repeated bool) error
	// size is the approximate size of the body of entry i, charged per iteration.
	size func(i int) int
	// noEach reports each as invalid for this container.
	noEach bool
}

// chain walks the entries in source order, applying the conditional chain
// semantics: elseif/else attach only to the immediately preceding chain.
func (r *resolver) chain(ctx context.Context, env *expr.Object, ops chainOps) error {
	open, taken := false, false
	for i := 0; i < ops.n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cond, insert := ops.at(i)
		switch {
		case cond != nil:
			d := cond
			selected := false
			switch d.Kind {
			case ast.DirectiveIf:
				open = true
				ok, err := r.test(ctx, d, env)
				if err != nil {
					return err
				}
				selected, taken = ok, ok
			case ast.DirectiveElseIf:
				if !open {
					r.addAt("TRL-EXPR-0001", d.Pos, "elseif without preceding if", nil)
				} else if !taken {
					ok, err := r.test(ctx, d, env)
					if err != nil {
						return err
					}
					selected, taken = ok, ok
				}
			case ast.DirectiveElse:
				if !open {
					r.addAt("TRL-EXPR-0001", d.Pos, "else without preceding if", nil)
				} else {
					selected = !taken
					taken = true
				}
				open = false
			case ast.DirectiveEach:
				open = false
				if ops.noEach {
					r.addAt("TRL-EXPR-0005", d.Pos, "each is not valid for a typed mapping body", nil)
					continue
				}
				value, ok, err := r.evalOK(ctx, d, env)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				size := ops.size(i)
				for _, binding := range iterations(value) {
					if err := r.iterate(size); err != nil {
						return err
					}
					if err := ops.body(i, with(env, d.Variable, binding), true); err != nil {
						return err
					}
				}
				continue
			default:
				r.addAt("TRL-EXPR-0005", d.Pos, "directive is not valid here", nil)
				continue
			}
			if selected {
				if err := ops.body(i, env, false); err != nil {
					return err
				}
			}
		case insert != nil:
			open = false
			value, ok, err := r.evalOK(ctx, insert, env)
			if err != nil {
				return err
			}
			if ok {
				if err := ops.insert(i, value, insert); err != nil {
					return err
				}
			}
		default:
			open = false
			if err := ops.plain(i); err != nil {
				return err
			}
		}
	}
	return nil
}

// test evaluates a condition; a failed evaluation is a reported error and
// selects nothing.
func (r *resolver) test(ctx context.Context, d *ast.Directive, env *expr.Object) (bool, error) {
	value, ok, err := r.evalOK(ctx, d, env)
	return ok && truthy(value), err
}

func iterations(value expr.Value) []expr.Value {
	switch value := value.(type) {
	case *expr.Array:
		return value.Items
	case *expr.Object:
		out := make([]expr.Value, 0, len(value.Properties))
		for _, property := range value.Properties {
			out = append(out, &expr.Object{Properties: []expr.Property{{Name: "Key", Value: expr.String(property.Name)}, {Name: "Value", Value: property.Value}}})
		}
		return out
	default:
		return nil
	}
}
