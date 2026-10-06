package expr

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

type functionSpec struct {
	minArgs int
	maxArgs int // -1 means unbounded.
	phases  uint8
}

const (
	compileFunctions = 1 << iota
	runtimeFunctions
)

func findFunction(name string) (functionSpec, bool) {
	both := uint8(compileFunctions | runtimeFunctions)
	switch name {
	case "and", "or", "in", "notin", "coalesce":
		return functionSpec{2, -1, both}, true
	case "not", "length", "lower", "upper", "trim":
		return functionSpec{1, 1, both}, true
	case "xor", "eq", "ne", "gt", "ge", "lt", "le", "contains", "containsvalue", "startswith", "endswith", "join", "split":
		return functionSpec{2, 2, both}, true
	case "iif", "replace":
		return functionSpec{3, 3, both}, true
	case "format":
		return functionSpec{1, -1, both}, true
	case "converttojson":
		return functionSpec{1, 1, compileFunctions}, true
	case "counter":
		return functionSpec{1, 2, runtimeFunctions}, true
	case "always", "canceled", "succeededorfailed":
		return functionSpec{0, 0, runtimeFunctions}, true
	case "failed", "succeeded":
		return functionSpec{0, -1, runtimeFunctions}, true
	default:
		return functionSpec{}, false
	}
}

func (e evaluator) evalCall(call *Call) (any, error) {
	name := strings.ToLower(call.Name)
	spec, ok := findFunction(name)
	if !ok {
		return nil, e.failure(call, "unknown function "+call.Name)
	}
	if len(call.Args) < spec.minArgs || spec.maxArgs >= 0 && len(call.Args) > spec.maxArgs {
		return nil, e.failure(call, fmt.Sprintf("%s expects %s arguments, got %d", call.Name, arity(spec), len(call.Args)))
	}
	phaseFlag := uint8(compileFunctions)
	if e.options.Phase == PhaseRuntime {
		phaseFlag = runtimeFunctions
	}
	if spec.phases&phaseFlag == 0 {
		return nil, e.failure(call, fmt.Sprintf("%s is unavailable in this phase", call.Name))
	}
	if name == "and" || name == "or" {
		return e.evalLogical(call, name == "and")
	}
	if name == "iif" {
		condition, err := e.eval(call.Args[0])
		if err != nil {
			return nil, err
		}
		if truthy(condition) {
			return e.eval(call.Args[1])
		}
		return e.eval(call.Args[2])
	}
	args := make([]any, len(call.Args))
	for i, arg := range call.Args {
		value, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	return e.apply(call, name, args)
}

func (e evaluator) evalLogical(call *Call, and bool) (any, error) {
	for _, arg := range call.Args {
		value, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		result := truthy(value)
		if and && !result {
			return false, nil
		}
		if !and && result {
			return true, nil
		}
	}
	return and, nil
}

func (e evaluator) apply(call *Call, name string, args []any) (any, error) {
	switch name {
	case "not":
		return !truthy(args[0]), nil
	case "xor":
		return truthy(args[0]) != truthy(args[1]), nil
	case "eq", "ne":
		equal, ok := equalConverted(args[0], args[1])
		if !ok {
			equal = false
		}
		if name == "ne" {
			equal = !equal
		}
		return equal, nil
	case "gt", "ge", "lt", "le":
		comparison, ok := compareConverted(args[0], args[1])
		if !ok {
			return nil, e.typeFailure(call, "comparison conversion failed")
		}
		switch name {
		case "gt":
			return comparison > 0, nil
		case "ge":
			return comparison >= 0, nil
		case "lt":
			return comparison < 0, nil
		default:
			return comparison <= 0, nil
		}
	case "in", "notin":
		found := false
		for _, candidate := range args[1:] {
			equal, _ := equalConverted(args[0], candidate)
			if equal {
				found = true
				break
			}
		}
		if name == "notin" {
			found = !found
		}
		return found, nil
	case "coalesce":
		for _, value := range args {
			if value != nil && stringify(value) != "" {
				return value, nil
			}
		}
		return nil, nil
	case "contains", "startswith", "endswith":
		left, right := strings.ToLower(stringify(args[0])), strings.ToLower(stringify(args[1]))
		switch name {
		case "contains":
			return strings.Contains(left, right), nil
		case "startswith":
			return strings.HasPrefix(left, right), nil
		default:
			return strings.HasSuffix(left, right), nil
		}
	case "containsvalue":
		items, ok := valuesOf(args[0])
		if !ok {
			return false, nil
		}
		for _, item := range items {
			if equal, _ := equalConverted(args[1], item); equal {
				return true, nil
			}
		}
		return false, nil
	case "format":
		value, err := formatValue(stringify(args[0]), args[1:])
		if err != nil {
			return nil, e.typeFailure(call, err.Error())
		}
		return value, nil
	case "join":
		items, ok := valuesOf(args[1])
		if !ok {
			return stringify(args[1]), nil
		}
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = stringify(item)
		}
		return strings.Join(parts, stringify(args[0])), nil
	case "split":
		return strings.Split(stringify(args[0]), stringify(args[1])), nil
	case "replace":
		return strings.ReplaceAll(stringify(args[0]), stringify(args[1]), stringify(args[2])), nil
	case "length":
		rv := indirect(reflect.ValueOf(args[0]))
		if !rv.IsValid() {
			return float64(0), nil
		}
		switch rv.Kind() {
		case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
			return float64(rv.Len()), nil
		default:
			return nil, e.typeFailure(call, "length requires a string, array, or object")
		}
	case "lower":
		return strings.ToLower(stringify(args[0])), nil
	case "upper":
		return strings.ToUpper(stringify(args[0])), nil
	case "trim":
		return strings.TrimSpace(stringify(args[0])), nil
	case "converttojson":
		data, err := json.MarshalIndent(args[0], "", "  ")
		if err != nil {
			return nil, e.typeFailure(call, "value cannot be encoded as JSON")
		}
		return string(data), nil
	case "counter":
		if !e.options.VariableDefinition {
			return nil, e.failure(call, "counter is only valid in a variable definition")
		}
		if e.options.Counter == nil {
			return nil, e.failure(call, "counter provider is not configured")
		}
		seed := int64(0)
		if len(args) == 2 {
			number, ok := toFloat(args[1])
			if !ok || number != float64(int64(number)) {
				return nil, e.typeFailure(call, "counter seed must be an integer")
			}
			seed = int64(number)
		}
		value, err := e.options.Counter.Next(e.ctx, stringify(args[0]), seed)
		if err != nil {
			return nil, fmt.Errorf("counter: %w", err)
		}
		return float64(value), nil
	case "always", "canceled", "failed", "succeeded", "succeededorfailed":
		if e.options.Status == nil {
			return nil, e.failure(call, "status provider is not configured")
		}
		names := make([]string, len(args))
		for i, arg := range args {
			names[i] = stringify(arg)
		}
		return e.options.Status.Status(e.ctx, name, names)
	default:
		return nil, e.failure(call, "function not implemented: "+name)
	}
}

func arity(spec functionSpec) string {
	if spec.minArgs == spec.maxArgs {
		return strconv.Itoa(spec.minArgs)
	}
	if spec.maxArgs < 0 {
		return fmt.Sprintf("at least %d", spec.minArgs)
	}
	return fmt.Sprintf("%d to %d", spec.minArgs, spec.maxArgs)
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case Version:
		return true
	}
	if number, ok := toFloat(value); ok {
		return number != 0
	}
	rv := indirect(reflect.ValueOf(value))
	if rv.IsValid() {
		switch rv.Kind() {
		case reflect.Array, reflect.Slice, reflect.Map:
			return rv.Len() != 0
		}
	}
	return true
}

func equalConverted(left, right any) (bool, bool) {
	converted, ok := convertLike(right, left)
	if !ok {
		return false, false
	}
	switch l := left.(type) {
	case string:
		return strings.EqualFold(l, converted.(string)), true
	case Version:
		return compareVersions(l, converted.(Version)) == 0, true
	case nil:
		return converted == nil, true
	}
	return reflect.DeepEqual(normalizeJSONNumber(left), normalizeJSONNumber(converted)), true
}

func compareConverted(left, right any) (int, bool) {
	converted, ok := convertLike(right, left)
	if !ok {
		return 0, false
	}
	switch l := left.(type) {
	case string:
		return strings.Compare(strings.ToLower(l), strings.ToLower(converted.(string))), true
	case Version:
		return compareVersions(l, converted.(Version)), true
	case bool:
		r := converted.(bool)
		if l == r {
			return 0, true
		}
		if !l {
			return -1, true
		}
		return 1, true
	default:
		ln, lok := toFloat(left)
		rn, rok := toFloat(converted)
		if !lok || !rok {
			return 0, false
		}
		if ln < rn {
			return -1, true
		}
		if ln > rn {
			return 1, true
		}
		return 0, true
	}
}

func convertLike(value, target any) (any, bool) {
	switch target.(type) {
	case nil:
		return nil, value == nil
	case string:
		return stringify(value), true
	case bool:
		return toBool(value)
	case Version:
		switch v := value.(type) {
		case Version:
			return v, true
		case string:
			version, err := parseVersion(v)
			return version, err == nil
		default:
			return nil, false
		}
	default:
		if _, ok := toFloat(target); ok {
			return toFloat(value)
		}
		return value, reflect.TypeOf(value) == reflect.TypeOf(target)
	}
}

func toBool(value any) (any, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.ToLower(v))
		return parsed, err == nil
	default:
		if number, ok := toFloat(value); ok {
			return number != 0, true
		}
		return nil, false
	}
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return finite(v)
	case float32:
		return finite(float64(v))
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return finite(n)
	case string:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return finite(n)
	default:
		return 0, false
	}
}

func finite(value float64) (float64, bool) {
	return value, !math.IsNaN(value) && !math.IsInf(value, 0)
}

func normalizeJSONNumber(value any) any {
	if number, ok := toFloat(value); ok {
		return number
	}
	return value
}

func compareVersions(left, right Version) int {
	for i := 0; i < 4; i++ {
		var l, r int
		if i < len(left.Parts) {
			l = left.Parts[i]
		}
		if i < len(right.Parts) {
			r = right.Parts[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}

func formatValue(format string, args []any) (string, error) {
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
				return "", fmt.Errorf("format: unmatched {")
			}
			end += pos + 1
			field := format[pos+1 : end]
			indexText := strings.SplitN(field, ":", 2)[0]
			index, err := strconv.Atoi(indexText)
			if err != nil || index < 0 || index >= len(args) {
				return "", fmt.Errorf("format: invalid placeholder {%s}", field)
			}
			b.WriteString(stringify(args[index]))
			pos = end + 1
		case format[pos] == '}':
			return "", fmt.Errorf("format: unmatched }")
		default:
			b.WriteByte(format[pos])
			pos++
		}
	}
	return b.String(), nil
}
