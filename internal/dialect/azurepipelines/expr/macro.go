package expr

import (
	"strings"
)

// Macro is one $(name) occurrence in a scalar value.
type Macro struct {
	Span Span
	Name string
}

// ScanMacros finds macros in arbitrary text. `$$(name)` escapes a macro and
// produces no occurrence; ExpandMacros removes the extra dollar.
func ScanMacros(input string) ([]Macro, error) {
	var macros []Macro
	for pos := 0; pos < len(input); {
		start := strings.Index(input[pos:], "$(")
		if start < 0 {
			break
		}
		start += pos
		if start > 0 && input[start-1] == '$' {
			pos = start + 2
			continue
		}
		end := strings.IndexByte(input[start+2:], ')')
		if end < 0 {
			return nil, syntaxError(start, "unterminated macro")
		}
		end += start + 2
		name := strings.TrimSpace(input[start+2 : end])
		if name == "" || strings.ContainsAny(name, "()") {
			return nil, syntaxError(start, "invalid macro name")
		}
		macros = append(macros, Macro{Span: Span{Start: start, End: end + 1}, Name: name})
		pos = end + 1
	}
	return macros, nil
}

// ExpandMacros replaces known macros and leaves unknown macros literal.
func ExpandMacros(input string, context Context) (string, error) {
	var b strings.Builder
	for pos := 0; pos < len(input); {
		if strings.HasPrefix(input[pos:], "$$(") {
			end := strings.IndexByte(input[pos+3:], ')')
			if end < 0 {
				return "", syntaxError(pos+1, "unterminated macro")
			}
			end += pos + 3
			b.WriteString(input[pos+1 : end+1])
			pos = end + 1
			continue
		}
		if !strings.HasPrefix(input[pos:], "$(") {
			b.WriteByte(input[pos])
			pos++
			continue
		}
		end := strings.IndexByte(input[pos+2:], ')')
		if end < 0 {
			return "", syntaxError(pos, "unterminated macro")
		}
		end += pos + 2
		name := strings.TrimSpace(input[pos+2 : end])
		if name == "" || strings.ContainsAny(name, "()") {
			return "", syntaxError(pos, "invalid macro name")
		}
		value, ok := lookupMacro(context, name)
		if ok {
			b.WriteString(stringify(value))
		} else {
			b.WriteString(input[pos : end+1])
		}
		pos = end + 1
	}
	return b.String(), nil
}

func lookupMacro(context Context, name string) (any, bool) {
	if context == nil {
		return nil, false
	}
	variables, ok := context.Lookup("variables")
	if ok {
		if value, found := lookupProperty(variables, name); found {
			return value, true
		}
	}
	return context.Lookup(name)
}
