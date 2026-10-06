package expr

import (
	"fmt"
	"strings"
)

// DirectiveKind identifies an insertion directive used as a YAML key.
type DirectiveKind uint8

const (
	// DirectiveIf starts a conditional insertion branch.
	DirectiveIf DirectiveKind = iota
	// DirectiveElseIf continues a conditional insertion branch.
	DirectiveElseIf
	// DirectiveElse is the final conditional insertion branch.
	DirectiveElse
	// DirectiveEach iterates an insertion body.
	DirectiveEach
)

// Directive is a parsed `${{ if }}`, `elseif`, `else`, or `each` key.
type Directive struct {
	Span     Span
	Kind     DirectiveKind
	Variable string
	Expr     Expr
}

// ParseDirective parses an entire insertion-directive key.
func ParseDirective(input string) (*Directive, error) {
	trimmed := strings.TrimSpace(input)
	leading := strings.Index(input, trimmed)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") {
		return nil, syntaxError(leading, "directive must occupy a complete ${{ }} key")
	}
	bodyStart := leading + 3
	body := strings.TrimSpace(input[bodyStart : leading+len(trimmed)-2])
	bodyOffset := strings.Index(input[bodyStart:], body) + bodyStart
	directive := &Directive{Span: Span{Start: leading, End: leading + len(trimmed)}}
	switch {
	case body == "else":
		directive.Kind = DirectiveElse
		return directive, nil
	case strings.HasPrefix(body, "if "):
		directive.Kind = DirectiveIf
		body = strings.TrimSpace(body[3:])
		bodyOffset += strings.Index(input[bodyOffset:], body)
	case strings.HasPrefix(body, "elseif "):
		directive.Kind = DirectiveElseIf
		body = strings.TrimSpace(body[7:])
		bodyOffset += strings.Index(input[bodyOffset:], body)
	case strings.HasPrefix(body, "each "):
		directive.Kind = DirectiveEach
		rest := strings.TrimSpace(body[5:])
		pieces := strings.SplitN(rest, " in ", 2)
		if len(pieces) != 2 || !validDirectiveName(strings.TrimSpace(pieces[0])) {
			return nil, syntaxError(bodyOffset, "each syntax is: each name in expression")
		}
		directive.Variable = strings.TrimSpace(pieces[0])
		body = strings.TrimSpace(pieces[1])
		bodyOffset += strings.Index(input[bodyOffset:], body)
	default:
		return nil, syntaxError(bodyOffset, "unknown directive")
	}
	if body == "" {
		return nil, syntaxError(bodyOffset, "directive expression is empty")
	}
	node, err := Parse(body)
	if err != nil {
		return nil, shiftSyntaxError(err, bodyOffset)
	}
	directive.Expr = node
	return directive, nil
}

func validDirectiveName(name string) bool {
	first := true
	for _, r := range name {
		if first {
			if !isIdentStart(r) {
				return false
			}
			first = false
			continue
		}
		if !isIdentContinue(r) {
			return false
		}
	}
	return !first
}

func (d DirectiveKind) String() string {
	switch d {
	case DirectiveIf:
		return "if"
	case DirectiveElseIf:
		return "elseif"
	case DirectiveElse:
		return "else"
	case DirectiveEach:
		return "each"
	default:
		return fmt.Sprintf("DirectiveKind(%d)", d)
	}
}
