package modernize

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize/program"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

type inputValue struct {
	pair             yamlx.Pair
	canonical        string
	old, accepted    bool
	dropped, renamed bool
}

type stepPlan struct {
	src             []byte
	step, task      *yamlx.Node
	row             catalog.Modernization
	program         program.Program
	cat             *catalog.Catalog
	inputs          *yamlx.Node
	inputKey        *yamlx.Node
	values          []*inputValue
	old, new        *catalog.TaskVersion
	failures        []diag.Diagnostic
	failed          map[string]bool
	edits           []yamlx.Patch
	changes, review []map[string]any
	appendOps       []program.Operation
	err             error
}

func taskVersion(cat *catalog.Catalog, ref string) *catalog.TaskVersion {
	if cat == nil {
		return &catalog.TaskVersion{}
	}
	_, major, _, _ := splitReference(ref)
	if task, ok := cat.ResolveTask(ref); ok {
		if v, ok := task.Version(major); ok {
			return v
		}
	}
	return &catalog.TaskVersion{}
}

func (s *stepPlan) canonical(key string) string {
	if v, ok := s.old.ResolveInput(key); ok {
		// New aliases connect an old canonical name to a renamed replacement input.
		if next, ok := s.new.ResolveInput(v.Name); ok {
			return next.Name
		}
		return v.Name
	}
	if v, ok := s.new.ResolveInput(key); ok {
		return v.Name
	}
	return key
}

func (s *stepPlan) find(key string) *inputValue {
	canonical := s.canonical(key)
	for _, v := range s.values {
		if strings.EqualFold(v.canonical, canonical) {
			return v
		}
	}
	return nil
}

func (s *stepPlan) fail(key, reason string, anchor *yamlx.Node) {
	canonical := strings.ToLower(s.canonical(key))
	if s.failed[canonical] {
		return
	}
	s.failed[canonical] = true
	if anchor == nil {
		anchor = s.task
	}
	s.failures = append(s.failures, rewriteDiagnostic("TRL-MOD-0006", anchor, map[string]any{"from": s.row.Task, "input": key, "reason": reason}))
}

func (s *stepPlan) failValue(v *inputValue, reason string) {
	s.fail(v.pair.Key.Value(), reason, v.pair.Key)
}

func decoded(n *yamlx.Node) string {
	if n.Tag() == "!!null" {
		return ""
	}
	return n.Value()
}
func literal(n *yamlx.Node) bool { return n.Kind() == yamlx.ScalarKind && !dynamic(decoded(n)) }

func (s *stepPlan) condition(c *program.Condition) (bool, bool) {
	if c == nil {
		return true, true
	}
	v := s.find(c.Input)
	if v == nil {
		return false, true
	}
	if !literal(v.pair.Value) {
		s.failValue(v, "non-literal-condition")
		return false, false
	}
	value := decoded(v.pair.Value)
	switch c.Op {
	case "multiline":
		return strings.ContainsAny(value, "\r\n"), true
	case "!=":
		return !strings.EqualFold(value, c.Value), true
	default:
		return strings.EqualFold(value, c.Value), true
	}
}

func (s *stepPlan) layout(reason string) ([]yamlx.Patch, []diag.Diagnostic, error) {
	return nil, []diag.Diagnostic{rewriteDiagnostic("TRL-MOD-0009", s.task, map[string]any{"from": s.row.Task, "reason": reason})}, nil
}

func (s *stepPlan) plan(raw, name string, full bool) ([]yamlx.Patch, []diag.Diagnostic, error) {
	s.old = oldTaskVersion(s.cat, s.row.Task)
	s.new = taskVersion(s.cat, s.row.Replacement[0])
	s.failed = map[string]bool{}
	s.changes = []map[string]any{}
	s.review = []map[string]any{}
	pairs, _ := s.step.Map()
	for _, p := range pairs {
		if strings.EqualFold(p.Key.Value(), "inputs") {
			s.inputs = p.Value
			s.inputKey = p.Key
		}
	}
	if s.inputs != nil {
		if s.inputs.Kind() != yamlx.MappingKind {
			return s.layout("invalid-inputs")
		}
		entries, _ := s.inputs.Map()
		for _, p := range entries {
			if dynamic(p.Key.Value()) {
				return s.layout("dynamic-input-key")
			}
		}
		r, e := s.inputs.ByteRange()
		if e != nil {
			return nil, nil, e
		}
		if s.row.Class == "B" && r.Start < len(s.src) && s.src[r.Start] == '{' {
			return s.layout("flow-inputs")
		}
		for _, p := range entries {
			canonical := s.canonical(p.Key.Value())
			if s.find(p.Key.Value()) != nil {
				s.failures = append(s.failures, rewriteDiagnostic("TRL-MOD-0006", p.Key, map[string]any{"from": s.row.Task, "input": p.Key.Value(), "reason": "duplicate-input"}))
				continue
			}
			_, old := s.old.ResolveInput(p.Key.Value())
			_, accepted := s.new.ResolveInput(p.Key.Value())
			// A replacement alias of an old name is still an old logical input.
			for _, in := range s.old.Inputs {
				if strings.EqualFold(s.canonical(in.Name), canonical) {
					old = true
				}
			}
			s.values = append(s.values, &inputValue{pair: p, canonical: canonical, old: old, accepted: accepted})
		}
	}
	if len(s.failures) > 0 {
		return nil, s.failures, nil
	}
	for _, op := range s.program.Operations {
		s.precondition(op)
	}
	for _, v := range s.values {
		switch {
		case v.old && !v.accepted && !v.dropped && !v.renamed:
			s.failValue(v, "removed-input")
		case !v.old && v.accepted:
			s.failValue(v, "new-only-input")
		}
	}
	if len(s.failures) > 0 {
		return nil, s.failures, nil
	}
	for _, phase := range [][]string{{"drop"}, {"rename"}, {"map", "invert"}, {"pin", "add"}} {
		for _, op := range s.program.Operations {
			for _, kind := range phase {
				if op.Kind == kind {
					s.operation(op)
				}
			}
		}
	}
	for _, op := range s.program.Operations {
		if op.Kind != "review" {
			continue
		}
		r := map[string]any{"kind": op.Mode, "input": op.Input}
		if op.Mode == "default" {
			r["value"] = typedValue(op.Value)
		} else {
			r["from"] = op.From
			r["to"] = op.To
		}
		s.review = append(s.review, r)
	}
	s.mappingEdits()
	to := s.row.Replacement[0]
	written := to
	guidChanged := false
	if strings.EqualFold(name, s.row.GUID) {
		replacement, ok := s.cat.ResolveTask(to)
		if ok && strings.EqualFold(replacement.ID, s.row.GUID) {
			_, major, _, _ := splitReference(to)
			written = name + "@" + itoa(major)
		} else {
			guidChanged = true
		}
	}
	s.scalarEdit(s.task, written, false)
	if s.err != nil {
		return nil, nil, s.err
	}
	params := map[string]any{"from": s.row.Task, "to": to}
	code := "TRL-MOD-0001"
	if s.row.Class == "B" {
		code = "TRL-MOD-0002"
		params["changes"] = s.changes
		params["review"] = s.review
	}
	ds := []diag.Diagnostic{rewriteDiagnostic(code, s.task, params)}
	if guidChanged {
		ds = append(ds, rewriteDiagnostic("TRL-MOD-0008", s.task, map[string]any{"from": raw, "to": written}))
	}
	if full {
		ds = append(ds, rewriteDiagnostic("TRL-MOD-0011", s.task, map[string]any{"from": raw, "to": written}))
	}
	for _, v := range s.values {
		if !v.old && !v.accepted {
			ds = append(ds, rewriteDiagnostic("TRL-MOD-0007", v.pair.Key, map[string]any{"from": s.row.Task, "input": v.pair.Key.Value()}))
		}
	}
	return s.edits, ds, nil
}

func (s *stepPlan) precondition(op program.Operation) {
	v := s.find(op.Input)
	switch op.Kind {
	case "guard":
		yes, ok := s.condition(op.If)
		if yes && ok {
			s.failValue(s.find(op.If.Input), "guard-condition")
		}
	case "literal", "map", "invert":
		if v == nil {
			return
		}
		if !literal(v.pair.Value) {
			s.failValue(v, "non-literal-input")
			return
		}
		if op.Kind == "invert" {
			if !strings.EqualFold(decoded(v.pair.Value), "true") && !strings.EqualFold(decoded(v.pair.Value), "false") {
				s.failValue(v, "invalid-boolean")
			}
			v.renamed = true
		}
	case "rename":
		if v != nil {
			v.renamed = true
		}
	case "drop":
		if v == nil {
			return
		}
		if op.If != nil && !literal(v.pair.Value) {
			// When the condition tests K itself it owns the non-literal failure.
			if strings.EqualFold(s.canonical(op.If.Input), v.canonical) {
				s.failValue(v, "non-literal-condition")
			} else {
				s.failValue(v, "non-literal-input")
			}
			return
		}
		yes, ok := s.condition(op.If)
		if ok {
			if yes {
				v.dropped = true
			} else {
				s.failValue(v, "removed-input")
			}
		}
	case "require":
		yes, ok := s.condition(op.If)
		if ok && yes && v == nil {
			if op.If != nil {
				s.failValue(s.find(op.If.Input), "required-input")
			} else {
				s.fail(op.Input, "required-input", s.task)
			}
		}
	case "pin", "add":
		if v != nil && (!v.old || op.Kind == "add") {
			s.failValue(v, "new-input-collision")
		}
		// Evaluate conditions on original inputs even when the pin is unnecessary.
		s.condition(op.If)
		if op.Unless != nil {
			s.condition(op.Unless)
		}
	}
}

func (s *stepPlan) operation(op program.Operation) {
	v := s.find(op.Input)
	change := map[string]any{"op": op.Kind, "input": op.Input}
	switch op.Kind {
	case "drop":
		if v == nil || !v.dropped {
			return
		}
		change["value"] = decoded(v.pair.Value)
	case "rename":
		if v == nil {
			return
		}
		s.scalarEdit(v.pair.Key, op.Target, false)
		change["to"] = op.Target
	case "map":
		if v == nil {
			return
		}
		found := false
		for _, pair := range op.Pairs {
			if strings.EqualFold(decoded(v.pair.Value), pair.From) {
				if decoded(v.pair.Value) == pair.To {
					return
				}
				s.scalarEdit(v.pair.Value, pair.To, false)
				change["from"] = decoded(v.pair.Value)
				change["to"] = pair.To
				found = true
				break
			}
		}
		if !found {
			return
		}
	case "invert":
		if v == nil {
			return
		}
		from := strings.EqualFold(decoded(v.pair.Value), "true")
		s.scalarEdit(v.pair.Key, op.Target, false)
		s.scalarEdit(v.pair.Value, fmt.Sprint(!from), true)
		change["to"] = op.Target
		change["from"] = from
		change["value"] = !from
	case "pin", "add":
		if v != nil {
			return
		}
		yes, _ := s.condition(op.If)
		if !yes {
			return
		}
		if op.Unless != nil {
			unless, _ := s.condition(op.Unless)
			if unless {
				return
			}
		}
		s.appendOps = append(s.appendOps, op)
		change["value"] = typedValue(op.Value)
	}
	s.changes = append(s.changes, change)
}

const headerValue = "{\n\"Content-Type\":\"application/json\"\n}"

func typedValue(v program.Value) any {
	switch v.Kind {
	case "null":
		return nil
	case "boolean":
		return v.Text == "true"
	case "integer":
		return json.Number(v.Text)
	case "K1":
		return headerValue
	default:
		return v.Text
	}
}
