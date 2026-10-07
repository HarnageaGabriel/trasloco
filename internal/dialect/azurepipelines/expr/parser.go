package expr

import (
	"fmt"
	"strings"
)

// Phase is the Azure Pipelines evaluation phase.
type Phase uint8

const (
	// PhaseCompile evaluates template expressions.
	PhaseCompile Phase = iota
	// PhaseRuntime evaluates runtime expressions.
	PhaseRuntime
	// PhaseMacro expands macro values immediately before a task.
	PhaseMacro
)

// Parse parses an unwrapped expression. Exact `${{ }}` and `$[ ]` wrappers are
// also accepted for callers that work with scalar values.
func Parse(input string) (Expr, error) {
	inner, base, err := unwrap(input)
	if err != nil {
		return nil, err
	}
	tokens, err := Lex(inner)
	if err != nil {
		return nil, shiftSyntaxError(err, base)
	}
	p := parser{tokens: tokens, base: base}
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if token := p.peek(); token.Kind != TokenEOF {
		return nil, p.err(token, fmt.Sprintf("unexpected token %q", token.Text))
	}
	if astDepth(node) > MaxDepth {
		return nil, syntaxError(base, "expression exceeds depth limit")
	}
	return node, nil
}

func unwrap(input string) (string, int, error) {
	trimmed := strings.TrimSpace(input)
	leading := strings.Index(input, trimmed)
	if strings.HasPrefix(trimmed, "${{") {
		if !strings.HasSuffix(trimmed, "}}") {
			return "", 0, syntaxError(leading, "unterminated template expression")
		}
		start := leading + 3
		return input[start : leading+len(trimmed)-2], start, nil
	}
	if strings.HasPrefix(trimmed, "$[") {
		if !strings.HasSuffix(trimmed, "]") {
			return "", 0, syntaxError(leading, "unterminated runtime expression")
		}
		start := leading + 2
		return input[start : leading+len(trimmed)-1], start, nil
	}
	return input, 0, nil
}

type parser struct {
	depth  int
	tokens []Token
	pos    int
	base   int
}

func (p *parser) parseExpr() (Expr, error) {
	if p.depth >= MaxDepth {
		return nil, p.err(p.peek(), "expression exceeds depth limit")
	}
	p.depth++
	defer func() { p.depth-- }()
	token := p.next()
	var node Expr
	switch token.Kind {
	case TokenIdentifier:
		if p.peek().Kind == TokenLeftParen {
			call, err := p.parseCall(token)
			if err != nil {
				return nil, err
			}
			node = call
		} else {
			switch strings.ToLower(token.Text) {
			case "true", "false":
				node = &Literal{Span: p.span(token.Span), Kind: BooleanLiteral, Value: Bool(strings.EqualFold(token.Text, "true"))}
			case "null":
				return nil, p.err(token, "null is not a literal")
			default:
				if _, function := findFunction(strings.ToLower(token.Text)); function {
					return nil, p.err(p.peek(), "expected ( after function name")
				}
				node = &Identifier{Span: p.span(token.Span), Name: token.Text}
			}
		}
	case TokenString:
		node = &Literal{Span: p.span(token.Span), Kind: StringLiteral, Value: String(token.Text)}
	case TokenNumber:
		number, err := ParseNumber(token.Text)
		if err != nil {
			return nil, p.err(token, "invalid number")
		}
		node = &Literal{Span: p.span(token.Span), Kind: NumberLiteral, Value: number}
	case TokenVersion:
		version, err := parseVersion(token.Text)
		if err != nil {
			return nil, p.err(token, err.Error())
		}
		node = &Literal{Span: p.span(token.Span), Kind: VersionLiteral, Value: version}
	default:
		return nil, p.err(token, "expected expression")
	}

	for {
		if _, literal := node.(*Literal); literal && (p.peek().Kind == TokenDot || p.peek().Kind == TokenLeftBracket) {
			return nil, p.err(p.peek(), "literal cannot be indexed")
		}
		switch p.peek().Kind {
		case TokenDot:
			p.next()
			property := p.next()
			if property.Kind != TokenIdentifier && property.Kind != TokenStar {
				return nil, p.err(property, "expected property name or *")
			}
			r := node.Range()
			node = &Member{
				Span:     Span{Start: r.Start, End: property.Span.End + p.base},
				Receiver: node,
				Name:     property.Text,
				Wildcard: property.Kind == TokenStar,
			}
		case TokenLeftBracket:
			p.next()
			if p.peek().Kind == TokenStar {
				p.next()
				closeToken := p.next()
				if closeToken.Kind != TokenRightBracket {
					return nil, p.err(closeToken, "expected ]")
				}
				node = &Member{Span: Span{node.Range().Start, closeToken.Span.End + p.base}, Receiver: node, Name: "*", Wildcard: true}
				continue
			}
			key, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			closeToken := p.next()
			if closeToken.Kind != TokenRightBracket {
				return nil, p.err(closeToken, "expected ]")
			}
			r := node.Range()
			node = &Index{Span: Span{Start: r.Start, End: closeToken.Span.End + p.base}, Receiver: node, Key: key}
		default:
			return node, nil
		}
	}
}

func (p *parser) parseCall(name Token) (*Call, error) {
	p.next() // (
	args := make([]Expr, 0)
	if p.peek().Kind != TokenRightParen {
		for {
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if p.peek().Kind != TokenComma {
				break
			}
			p.next()
		}
	}
	closeToken := p.next()
	if closeToken.Kind != TokenRightParen {
		return nil, p.err(closeToken, "expected )")
	}
	return &Call{Span: Span{Start: name.Span.Start + p.base, End: closeToken.Span.End + p.base}, Name: strings.ToLower(name.Text), Args: args}, nil
}

func (p *parser) peek() Token { return p.tokens[p.pos] }
func (p *parser) next() Token {
	token := p.tokens[p.pos]
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return token
}
func (p *parser) span(s Span) Span { return Span{Start: s.Start + p.base, End: s.End + p.base} }
func (p *parser) err(token Token, msg string) error {
	return syntaxError(token.Span.Start+p.base, msg)
}

func shiftSyntaxError(err error, base int) error {
	if span, ok := expressionErrorSpan(err); ok {
		return syntaxError(span.Start+base, expressionErrorMessage(err))
	}
	return err
}

func astDepth(node Expr) int {
	switch n := node.(type) {
	case *Call:
		depth := 0
		for _, arg := range n.Args {
			depth = maxInt(depth, astDepth(arg))
		}
		return depth + 1
	case *Member:
		return astDepth(n.Receiver) + 1
	case *Index:
		return maxInt(astDepth(n.Receiver), astDepth(n.Key)) + 1
	default:
		return 1
	}
}
