package vars

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// onode is an order-preserving JSON tree. Vector fixtures are JSON-as-YAML where
// key order is YAML order, so Go maps cannot carry them.
type onode struct {
	kind  byte // 'o' object, 'a' array, 's' string, 'x' other scalar
	keys  []string
	vals  []*onode
	items []*onode
	str   string
	raw   string
}

func parseOrdered(data []byte) (*onode, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return parseOrderedValue(dec)
}

func parseOrderedValue(dec *json.Decoder) (*onode, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			n := &onode{kind: 'o'}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				child, err := parseOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, keyToken.(string))
				n.vals = append(n.vals, child)
			}
			_, err = dec.Token()
			return n, err
		}
		n := &onode{kind: 'a'}
		for dec.More() {
			child, err := parseOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, child)
		}
		_, err = dec.Token()
		return n, err
	case string:
		return &onode{kind: 's', str: value}, nil
	case json.Number:
		return &onode{kind: 'x', raw: value.String()}, nil
	case bool:
		if value {
			return &onode{kind: 'x', raw: "true"}, nil
		}
		return &onode{kind: 'x', raw: "false"}, nil
	default:
		return &onode{kind: 'x', raw: "null"}, nil
	}
}

func (n *onode) JSON() string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n *onode) write(b *strings.Builder) {
	switch n.kind {
	case 'o':
		b.WriteByte('{')
		for i, key := range n.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			quoted, _ := json.Marshal(key)
			b.Write(quoted)
			b.WriteByte(':')
			n.vals[i].write(b)
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, item := range n.items {
			if i > 0 {
				b.WriteByte(',')
			}
			item.write(b)
		}
		b.WriteByte(']')
	case 's':
		quoted, _ := json.Marshal(n.str)
		b.Write(quoted)
	default:
		b.WriteString(n.raw)
	}
}

func (n *onode) get(key string) *onode {
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

func (n *onode) set(key string, value *onode) {
	for i, k := range n.keys {
		if k == key {
			n.vals[i] = value
			return
		}
	}
	n.keys = append(n.keys, key)
	n.vals = append(n.vals, value)
}

var (
	conditionalKey = regexp.MustCompile(`^\$\{\{\s*(if|elseif|else)\b\s*(.*?)\s*\}\}$`)
	insertKey      = regexp.MustCompile(`^\$\{\{\s*insert\s*\}\}$`)
)

// expander is the minimal STRUCTURAL template layer the vector harness needs to
// feed the evaluator an already-expanded AST: template inclusion with
// parameters, the shape of ${{ if/elseif/else }} and ${{ insert }} keys, and the
// walk over the fixture tree. It decides nothing about variables: every
// ${{ variables.x }} lookup, condition, substitution and lexical snapshot is
// delegated to the production CompileEvaluator (the engine behind
// Graph.EvalCompile), so a broken production evaluator breaks the vectors.
type expander struct {
	files map[string]*onode
	eval  *CompileEvaluator
}

func (e *expander) record(name string, value *onode) {
	if value == nil {
		return
	}
	e.eval.Declare(name, nodeToValue(value))
}

func (e *expander) truthy(condition string) bool {
	ok, err := e.eval.Condition(condition)
	return err == nil && ok
}

func (e *expander) expand(n *onode, container string) *onode {
	switch n.kind {
	case 's':
		return &onode{kind: 's', str: e.eval.Substitute(n.str)}
	case 'a':
		out := &onode{kind: 'a'}
		for _, item := range n.items {
			for _, produced := range e.expandItem(item, container) {
				if container == "variables" && produced.kind == 'o' {
					if name := produced.get("name"); name != nil && name.kind == 's' {
						e.record(name.str, produced.get("value"))
					}
				}
				out.items = append(out.items, produced)
			}
		}
		return out
	case 'o':
		return e.expandObject(n, container)
	}
	return n
}

func (e *expander) expandItem(item *onode, container string) []*onode {
	if item.kind == 'o' && len(item.keys) > 0 {
		if template := item.get("template"); template != nil && template.kind == 's' {
			if produced, ok := e.include(item, template.str, container); ok {
				return produced
			}
		}
		if conditionalKey.MatchString(item.keys[0]) {
			return e.chooseBranch(item, container)
		}
	}
	if container == "variables" {
		container = "variableItem"
	}
	return []*onode{e.expand(item, container)}
}

func (e *expander) chooseBranch(item *onode, container string) []*onode {
	taken := false
	var out []*onode
	for i, key := range item.keys {
		match := conditionalKey.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		switch match[1] {
		case "if":
			taken = false
			fallthrough
		case "elseif":
			if taken || !e.truthy(match[2]) {
				continue
			}
		case "else":
			if taken {
				continue
			}
		}
		taken = true
		value := item.vals[i]
		if value.kind == 'a' {
			out = append(out, e.expand(value, container).items...)
		} else {
			out = append(out, e.expand(value, container))
		}
	}
	return out
}

func (e *expander) expandObject(n *onode, container string) *onode {
	if n.get("stage") != nil || n.get("job") != nil || n.get("deployment") != nil {
		e.eval.Push()
		defer e.eval.Pop()
	}
	out := &onode{kind: 'o'}
	emit := func(key string, value *onode) {
		out.set(key, value)
		if container == "variables" {
			e.record(key, value)
		}
	}
	taken := false
	for i, key := range n.keys {
		value := n.vals[i]
		if match := conditionalKey.FindStringSubmatch(key); match != nil {
			switch match[1] {
			case "if":
				taken = false
				fallthrough
			case "elseif":
				if taken || !e.truthy(match[2]) {
					continue
				}
			case "else":
				if taken {
					continue
				}
			}
			taken = true
			if branch := e.expand(value, container); branch.kind == 'o' {
				for j, inner := range branch.keys {
					emit(inner, branch.vals[j])
				}
			}
			continue
		}
		if insertKey.MatchString(key) {
			inserted := e.expand(value, container)
			if inserted.kind == 's' {
				if resolved, err := e.eval.Eval(strings.TrimSpace(value.str)); err == nil {
					inserted = valueToNode(resolved)
				}
			}
			if inserted.kind == 'o' {
				for j, inner := range inserted.keys {
					emit(inner, inserted.vals[j])
				}
			}
			continue
		}
		childContainer := container
		if key == "variables" || key == "jobs" || key == "steps" || key == "stages" {
			childContainer = key
		} else if container != "variables" {
			childContainer = ""
		}
		emit(e.eval.Substitute(key), e.expand(value, childContainer))
	}
	return out
}

func (e *expander) include(call *onode, file, container string) ([]*onode, bool) {
	template, ok := e.files[file]
	if !ok {
		return nil, false
	}
	section := template.get(container)
	if section == nil {
		return nil, false
	}
	child := &expander{files: e.files, eval: e.eval.Child()}
	if declared := template.get("parameters"); declared != nil && declared.kind == 'a' {
		child.declareParameters(declared)
	}
	if supplied := call.get("parameters"); supplied != nil && supplied.kind == 'o' {
		for i, key := range supplied.keys {
			child.eval.SetParameter(key, nodeToValue(e.expand(supplied.vals[i], "")))
		}
	}
	if section.kind == 'o' && container == "variables" {
		list := &onode{kind: 'a'}
		for i, key := range section.keys {
			list.items = append(list.items, &onode{kind: 'o', keys: []string{"name", "value"}, vals: []*onode{{kind: 's', str: key}, section.vals[i]}})
		}
		section = list
	}
	expanded := child.expand(section, container)
	return expanded.items, true
}

// declareParameters loads parameter defaults, keeping their declared types.
func (e *expander) declareParameters(declared *onode) {
	for _, parameter := range declared.items {
		name, def := parameter.get("name"), parameter.get("default")
		if name != nil && def != nil {
			e.eval.SetParameter(name.str, nodeToValue(def))
		}
	}
}

func nodeToValue(n *onode) expr.Value {
	switch n.kind {
	case 's':
		return expr.String(n.str)
	case 'o':
		object := &expr.Object{}
		for i, key := range n.keys {
			object.Properties = append(object.Properties, expr.Property{Name: key, Value: nodeToValue(n.vals[i])})
		}
		return object
	case 'a':
		array := &expr.Array{}
		for _, item := range n.items {
			array.Items = append(array.Items, nodeToValue(item))
		}
		return array
	}
	switch n.raw {
	case "true":
		return expr.Bool(true)
	case "false":
		return expr.Bool(false)
	case "null":
		return expr.Null{}
	}
	if number, err := expr.ParseNumber(n.raw); err == nil {
		return number
	}
	return expr.String(n.raw)
}

func valueToNode(v expr.Value) *onode {
	switch value := v.(type) {
	case *expr.Object:
		n := &onode{kind: 'o'}
		for _, p := range value.Properties {
			n.keys = append(n.keys, p.Name)
			n.vals = append(n.vals, valueToNode(p.Value))
		}
		return n
	case *expr.Array:
		n := &onode{kind: 'a'}
		for _, item := range value.Items {
			n.items = append(n.items, valueToNode(item))
		}
		return n
	case expr.String:
		return &onode{kind: 's', str: string(value)}
	}
	return &onode{kind: 'x', raw: "null"}
}

// expandFixture turns a fixture into the YAML the real parser receives. The
// predefined variables seed the production compile evaluator, which alone
// decides which of them templates can see.
func expandFixture(t *testing.T, yaml string, files map[string]*onode, predefined []PlainVariable) string {
	t.Helper()
	root, err := parseOrdered([]byte(yaml))
	if err != nil {
		t.Fatalf("fixture YAML is not ordered JSON: %v", err)
	}
	e := &expander{files: files, eval: NewCompileEvaluator(predefined)}
	if declared := root.get("parameters"); declared != nil && declared.kind == 'a' {
		e.declareParameters(declared)
	}
	return e.expand(root, "").JSON()
}
