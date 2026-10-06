package expr

import "strings"

// Macro is one valid $(name) occurrence in a scalar value.
type Macro struct {
	Span Span
	Name string
}

// ScanMacros is lenient: malformed shell constructs and unknown syntax stay literal.
// The error result is retained for compatibility and is always nil.
func ScanMacros(input string) ([]Macro, error) {
	var macros []Macro
	for pos := 0; pos < len(input); {
		start := strings.Index(input[pos:], "$(")
		if start < 0 {
			break
		}
		start += pos
		end := strings.IndexByte(input[start+2:], ')')
		if end < 0 {
			break
		}
		end += start + 2
		name := input[start+2 : end]
		if validMacroName(name) {
			macros = append(macros, Macro{Span{start, end + 1}, name})
			pos = end + 1
		} else {
			pos = start + 1
		}
	}
	return macros, nil
}

func validMacroName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !isIdentContinue(r) && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// ExpandMacros expands variable definitions recursively, then substitutes once.
// Cycles, over-depth definitions, and excessive output remain unexpanded.
func ExpandMacros(input string, values Context) (string, error) {
	return expandMacroText(input, func(name string) (string, bool) {
		value, ok := lookupMacro(values, name)
		if !ok {
			return "", false
		}
		raw, ok := stringify(value)
		if !ok {
			return "", false
		}
		expanded, valid := expandVariable(raw, values, []string{name})
		if !valid {
			return raw, true
		}
		return expanded, true
	}), nil
}

func expandVariable(raw string, values Context, stack []string) (string, bool) {
	valid := true
	result := expandMacroText(raw, func(name string) (string, bool) {
		for _, ancestor := range stack {
			if ordinalFold(ancestor) == ordinalFold(name) {
				valid = false
				return "", false
			}
		}
		value, ok := lookupMacro(values, name)
		if !ok {
			return "", false
		}
		text, ok := stringify(value)
		if !ok {
			return "", false
		}
		if len(stack) >= 50 {
			valid = false
			return "", false
		}
		expanded, ok := expandVariable(text, values, append(stack, name))
		if !ok {
			valid = false
			return "", false
		}
		return expanded, true
	})
	return result, valid
}

func expandMacroText(input string, lookup func(string) (string, bool)) string {
	macros, _ := ScanMacros(input)
	var b strings.Builder
	start := 0
	for _, macro := range macros {
		b.WriteString(input[start:macro.Span.Start])
		replacement, ok := lookup(macro.Name)
		if !ok {
			replacement = input[macro.Span.Start:macro.Span.End]
		}
		if b.Len()+len(replacement)+len(input)-macro.Span.End > MaxEvaluationMemory {
			return input
		}
		b.WriteString(replacement)
		start = macro.Span.End
	}
	b.WriteString(input[start:])
	return b.String()
}

func lookupMacro(values Context, name string) (Value, bool) {
	if values == nil {
		return Null{}, false
	}
	if variables, ok := values.Lookup("variables"); ok {
		return lookupProperty(variables, name)
	}
	return values.Lookup(name)
}
