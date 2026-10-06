package expr

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

type functionSpec struct {
	minArgs, maxArgs int
	runtimeOnly      bool
}

func findFunction(name string) (functionSpec, bool) {
	switch name {
	case "and", "or", "coalesce":
		return functionSpec{2, -1, false}, true
	case "in", "notin", "format":
		return functionSpec{1, -1, false}, true
	case "not", "length", "lower", "upper", "trim", "converttojson":
		return functionSpec{1, 1, false}, true
	case "xor", "eq", "ne", "gt", "ge", "lt", "le", "contains", "containsvalue", "startswith", "endswith", "join", "split":
		return functionSpec{2, 2, false}, true
	case "replace":
		return functionSpec{3, 3, false}, true
	case "iif":
		return functionSpec{2, 3, false}, true
	case "counter":
		return functionSpec{1, 2, true}, true
	case "always", "canceled":
		return functionSpec{0, 0, true}, true
	case "failed", "succeeded", "succeededorfailed":
		return functionSpec{0, -1, true}, true
	default:
		return functionSpec{}, false
	}
}

func (e *evaluator) evalCall(call *Call) (Value, error) {
	name := strings.ToLower(call.Name)
	spec, ok := findFunction(name)
	if !ok {
		return nil, e.failure(call, "unknown function")
	}
	if len(call.Args) < spec.minArgs || spec.maxArgs >= 0 && len(call.Args) > spec.maxArgs {
		return nil, e.failure(call, "invalid function arity")
	}
	if spec.runtimeOnly && e.options.Phase != PhaseRuntime {
		return nil, e.failure(call, "function unavailable at compile time")
	}
	switch name {
	case "and", "or", "coalesce", "in", "notin":
		return e.evalLazy(call, name)
	case "iif":
		condition, err := e.eval(call.Args[0])
		if err != nil {
			return nil, err
		}
		if truthy(condition) {
			return e.eval(call.Args[1])
		}
		if len(call.Args) == 3 {
			return e.eval(call.Args[2])
		}
		return Null{}, nil
	}
	args := make([]Value, len(call.Args))
	for i, arg := range call.Args {
		v, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return e.apply(call, name, args)
}

func (e *evaluator) evalLazy(call *Call, name string) (Value, error) {
	var left Value
	for i, arg := range call.Args {
		v, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		switch name {
		case "and":
			if !truthy(v) {
				return Bool(false), nil
			}
		case "or":
			if truthy(v) {
				return Bool(true), nil
			}
		case "coalesce":
			if _, null := v.(Null); !null {
				if s, ok := v.(String); !ok || s != "" {
					return v, nil
				}
			}
		case "in", "notin":
			if i == 0 {
				left = v
			} else if equal, _ := equalConverted(left, v); equal {
				return Bool(name == "in"), nil
			}
		}
	}
	if name == "coalesce" {
		return String(""), nil
	}
	return Bool(name == "and" || name == "notin"), nil
}

func (e *evaluator) apply(call *Call, name string, args []Value) (Value, error) {
	switch name {
	case "not":
		return Bool(!truthy(args[0])), nil
	case "xor":
		return Bool(truthy(args[0]) != truthy(args[1])), nil
	case "eq", "ne":
		equal, _ := equalConverted(args[0], args[1])
		return Bool(equal == (name == "eq")), nil
	case "gt", "ge", "lt", "le":
		comparison, ok := compareConverted(args[0], args[1])
		if !ok {
			return nil, e.typeFailure(call, "comparison conversion failed")
		}
		return Bool(name == "gt" && comparison > 0 || name == "ge" && comparison >= 0 || name == "lt" && comparison < 0 || name == "le" && comparison <= 0), nil
	case "containsvalue":
		items, _ := valuesOf(args[0])
		for _, item := range items {
			if equal, _ := equalConverted(args[1], item); equal {
				return Bool(true), nil
			}
		}
		return Bool(false), nil
	case "join":
		separator, ok := stringify(args[0])
		if !ok {
			return nil, e.typeFailure(call, "separator cannot convert to string")
		}
		var items []Value
		switch v := args[1].(type) {
		case *Array:
			items = v.Items
		case *projection:
			items = v.Items
		default:
			s, _ := stringify(v)
			return String(s), nil
		}
		var b strings.Builder
		for i, item := range items {
			s, _ := stringify(item)
			if i > 0 {
				if b.Len()+len(separator) > MaxEvaluationMemory/2 {
					return nil, e.failure(call, "join exceeds evaluation memory")
				}
				b.WriteString(separator)
			}
			if b.Len()+len(s) > MaxEvaluationMemory/2 {
				return nil, e.failure(call, "join exceeds evaluation memory")
			}
			b.WriteString(s)
		}
		return String(b.String()), nil
	case "length":
		var length int
		switch v := args[0].(type) {
		case String:
			length = utf16Length(string(v))
		case *Array:
			length = len(v.Items)
		case *projection:
			length = len(v.Items)
		case *Object:
			length = len(v.Properties)
		default:
			return nil, e.typeFailure(call, "length requires string, array or object")
		}
		return integer(int64(length)), nil
	case "converttojson":
		value := args[0]
		if p, ok := value.(*projection); ok {
			value = &p.Array
		}
		s, err := EncodeJSON(value, true)
		if err != nil {
			return nil, e.typeFailure(call, err.Error())
		}
		return String(s), nil
	case "counter":
		return e.counter(call, args)
	case "always", "canceled", "failed", "succeeded", "succeededorfailed":
		return e.status(call, name, args)
	case "format":
		format, ok := stringify(args[0])
		if !ok {
			return nil, e.typeFailure(call, "format requires a string")
		}
		s, err := formatValue(format, args[1:])
		if err != nil {
			return nil, e.typeFailure(call, err.Error())
		}
		return String(s), nil
	default:
		return e.stringFunction(call, name, args)
	}
}

func (e *evaluator) stringFunction(call *Call, name string, args []Value) (Value, error) {
	texts := make([]string, len(args))
	for i, arg := range args {
		s, ok := stringify(arg)
		if !ok {
			return nil, e.typeFailure(call, "value cannot convert to string")
		}
		texts[i] = s
	}
	switch name {
	case "contains":
		return Bool(strings.Contains(ordinalFold(texts[0]), ordinalFold(texts[1]))), nil
	case "startswith":
		return Bool(strings.HasPrefix(ordinalFold(texts[0]), ordinalFold(texts[1]))), nil
	case "endswith":
		return Bool(strings.HasSuffix(ordinalFold(texts[0]), ordinalFold(texts[1]))), nil
	case "lower":
		return String(strings.ToLower(texts[0])), nil
	case "upper":
		return String(strings.ToUpper(texts[0])), nil
	case "trim":
		return String(strings.TrimSpace(texts[0])), nil
	case "replace":
		if texts[1] == "" {
			return nil, e.typeFailure(call, "empty search string")
		}
		count := strings.Count(texts[0], texts[1])
		growth := len(texts[2]) - len(texts[1])
		if growth > 0 && count > (MaxEvaluationMemory/2-len(texts[0]))/growth {
			return nil, e.failure(call, "replace exceeds evaluation memory")
		}
		return String(strings.ReplaceAll(texts[0], texts[1], texts[2])), nil
	case "split":
		a := &Array{}
		start := 0
		for i, r := range texts[0] {
			if strings.ContainsRune(texts[1], r) {
				a.Items = append(a.Items, String(texts[0][start:i]))
				start = i + len(string(r))
			}
		}
		a.Items = append(a.Items, String(texts[0][start:]))
		return a, nil
	default:
		return nil, e.failure(call, "function not implemented")
	}
}

func (e *evaluator) counter(call *Call, args []Value) (Value, error) {
	if !e.options.VariableDefinition {
		return nil, e.failure(call, "counter only valid in variable definitions")
	}
	if e.options.Counter == nil {
		return nil, e.failure(call, "counter provider not configured")
	}
	prefix, ok := stringify(args[0])
	if !ok {
		return nil, e.typeFailure(call, "counter prefix cannot convert to string")
	}
	seed := int64(0)
	if len(args) == 2 {
		n, ok := toNumber(args[1])
		if !ok {
			return nil, e.typeFailure(call, "counter seed cannot convert to number")
		}
		r := n.rat()
		q := newIntegerQuotient(r)
		rem := new(big.Int).Rem(r.Num(), r.Denom())
		rem.Abs(rem).Lsh(rem, 1)
		cmp := rem.Cmp(r.Denom())
		if cmp > 0 || cmp == 0 && q.Bit(0) == 1 {
			q.Add(q, big.NewInt(int64(r.Sign())))
		}
		if !q.IsInt64() || q.Int64() < -2147483648 || q.Int64() > 2147483647 {
			return nil, e.typeFailure(call, "counter seed exceeds Int32")
		}
		seed = q.Int64()
	}
	value, err := e.options.Counter.Next(e.ctx, prefix, seed)
	if err != nil {
		if e.ctx.Err() != nil {
			return nil, e.ctx.Err()
		}
		return nil, fmt.Errorf("counter: %w", err)
	}
	return integer(value), nil
}

func (e *evaluator) status(call *Call, name string, args []Value) (Value, error) {
	if e.options.VariableDefinition {
		return nil, e.failure(call, "status unavailable in variable definitions")
	}
	if e.options.Scope == "step" && len(args) > 0 {
		return nil, e.failure(call, "job names unavailable in step conditions")
	}
	if e.options.Status == nil {
		return nil, e.failure(call, "status provider not configured")
	}
	names := make([]string, len(args))
	for i, arg := range args {
		s, ok := stringify(arg)
		if !ok {
			return nil, e.typeFailure(call, "job name cannot convert to string")
		}
		names[i] = s
	}
	value, err := e.options.Status.Status(e.ctx, name, names)
	if err != nil {
		return nil, err
	}
	return Bool(value), nil
}

func formatValue(format string, args []Value) (string, error) {
	var b strings.Builder
	for pos := 0; pos < len(format); {
		switch {
		case strings.HasPrefix(format[pos:], "{{"):
			b.WriteByte('{')
			pos += 2
		case strings.HasPrefix(format[pos:], "}}"):
			b.WriteByte('}')
			pos += 2
		case format[pos] == '{':
			end := strings.IndexByte(format[pos+1:], '}')
			if end < 0 {
				return "", fmt.Errorf("unmatched format brace")
			}
			end += pos + 1
			field := format[pos+1 : end]
			indexText, spec, hasSpec := strings.Cut(field, ":")
			for _, r := range indexText {
				if r < '0' || r > '9' {
					return "", fmt.Errorf("invalid format index")
				}
			}
			index, err := strconv.Atoi(indexText)
			if err != nil || index < 0 || index >= len(args) {
				return "", fmt.Errorf("invalid format index")
			}
			var value string
			if hasSpec {
				date, ok := args[index].(DateTime)
				if !ok {
					return "", fmt.Errorf("format specifier requires DateTime")
				}
				value, err = formatDate(date, spec)
				if err != nil {
					return "", err
				}
			} else {
				var ok bool
				value, ok = stringify(args[index])
				if !ok {
					return "", fmt.Errorf("format argument cannot convert to string")
				}
			}
			if b.Len()+len(value) > MaxEvaluationMemory/2 {
				return "", fmt.Errorf("format exceeds evaluation memory")
			}
			b.WriteString(value)
			pos = end + 1
		case format[pos] == '}':
			return "", fmt.Errorf("unmatched format brace")
		default:
			b.WriteByte(format[pos])
			pos++
		}
	}
	return b.String(), nil
}
