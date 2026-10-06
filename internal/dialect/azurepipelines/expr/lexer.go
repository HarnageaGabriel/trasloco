package expr

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenKind identifies a lexical token.
type TokenKind uint8

const (
	// TokenEOF marks the end of the input.
	TokenEOF TokenKind = iota
	// TokenIdentifier is a name.
	TokenIdentifier
	// TokenNumber is a decimal or exponential number.
	TokenNumber
	// TokenVersion is a three- or four-part version.
	TokenVersion
	// TokenString is a decoded single-quoted string.
	TokenString
	// TokenLeftParen is `(`.
	TokenLeftParen
	// TokenRightParen is `)`.
	TokenRightParen
	// TokenLeftBracket is `[`.
	TokenLeftBracket
	// TokenRightBracket is `]`.
	TokenRightBracket
	// TokenComma is `,`.
	TokenComma
	// TokenDot is `.`.
	TokenDot
	// TokenStar is `*`.
	TokenStar
)

// Token is one source token with half-open byte offsets.
type Token struct {
	Kind TokenKind
	Text string
	Span Span
}

// SyntaxError reports a byte offset and stable diagnostic code.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("TRL-EXPR-0001 at byte %d: %s", e.Offset, e.Msg)
}

// Lex tokenizes one unwrapped expression.
func Lex(input string) ([]Token, error) {
	var tokens []Token
	for pos := 0; pos < len(input); {
		r, size := utf8.DecodeRuneInString(input[pos:])
		if r == utf8.RuneError && size == 1 {
			return nil, syntaxError(pos, "invalid UTF-8")
		}
		if unicode.IsSpace(r) {
			pos += size
			continue
		}
		start := pos
		switch r {
		case '(':
			tokens = appendToken(tokens, TokenLeftParen, input, start, start+size)
			pos += size
		case ')':
			tokens = appendToken(tokens, TokenRightParen, input, start, start+size)
			pos += size
		case '[':
			tokens = appendToken(tokens, TokenLeftBracket, input, start, start+size)
			pos += size
		case ']':
			tokens = appendToken(tokens, TokenRightBracket, input, start, start+size)
			pos += size
		case ',':
			tokens = appendToken(tokens, TokenComma, input, start, start+size)
			pos += size
		case '.':
			tokens = appendToken(tokens, TokenDot, input, start, start+size)
			pos += size
		case '*':
			tokens = appendToken(tokens, TokenStar, input, start, start+size)
			pos += size
		case '\'':
			pos += size
			var value strings.Builder
			closed := false
			for pos < len(input) {
				if input[pos] != '\'' {
					r2, n := utf8.DecodeRuneInString(input[pos:])
					if r2 == utf8.RuneError && n == 1 {
						return nil, syntaxError(pos, "invalid UTF-8")
					}
					value.WriteString(input[pos : pos+n])
					pos += n
					continue
				}
				if pos+1 < len(input) && input[pos+1] == '\'' {
					value.WriteByte('\'')
					pos += 2
					continue
				}
				pos++
				closed = true
				break
			}
			if !closed {
				return nil, syntaxError(start, "unterminated string")
			}
			tokens = append(tokens, Token{Kind: TokenString, Text: value.String(), Span: Span{start, pos}})
		default:
			if isIdentStart(r) {
				pos += size
				for pos < len(input) {
					r2, n := utf8.DecodeRuneInString(input[pos:])
					if !isIdentContinue(r2) {
						break
					}
					pos += n
				}
				tokens = appendToken(tokens, TokenIdentifier, input, start, pos)
				continue
			}
			if r == '-' || r >= '0' && r <= '9' {
				end, kind, err := scanNumber(input, pos)
				if err != nil {
					return nil, err
				}
				tokens = appendToken(tokens, kind, input, start, end)
				pos = end
				continue
			}
			return nil, syntaxError(pos, fmt.Sprintf("unexpected character %q", r))
		}
	}
	tokens = append(tokens, Token{Kind: TokenEOF, Span: Span{len(input), len(input)}})
	return tokens, nil
}

func appendToken(tokens []Token, kind TokenKind, input string, start, end int) []Token {
	return append(tokens, Token{Kind: kind, Text: input[start:end], Span: Span{start, end}})
}

func scanNumber(input string, start int) (int, TokenKind, error) {
	pos := start
	if input[pos] == '-' {
		pos++
		if pos == len(input) || input[pos] < '0' || input[pos] > '9' {
			return 0, 0, syntaxError(start, "minus must precede a number")
		}
	}
	for pos < len(input) && input[pos] >= '0' && input[pos] <= '9' {
		pos++
	}
	dots := 0
	for pos < len(input) && input[pos] == '.' {
		if pos+1 == len(input) || input[pos+1] < '0' || input[pos+1] > '9' {
			break
		}
		dots++
		pos++
		for pos < len(input) && input[pos] >= '0' && input[pos] <= '9' {
			pos++
		}
	}
	if dots >= 2 {
		if strings.HasPrefix(input[start:pos], "-") {
			return 0, 0, syntaxError(start, "version literals cannot be negative")
		}
		if dots > 3 {
			return 0, 0, syntaxError(start, "version has more than 4 components")
		}
		return pos, TokenVersion, nil
	}
	if pos < len(input) && (input[pos] == 'e' || input[pos] == 'E') {
		pos++
		if pos < len(input) && (input[pos] == '+' || input[pos] == '-') {
			pos++
		}
		digit := pos
		for pos < len(input) && input[pos] >= '0' && input[pos] <= '9' {
			pos++
		}
		if digit == pos {
			return 0, 0, syntaxError(start, "invalid exponent")
		}
	}
	return pos, TokenNumber, nil
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentContinue(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func syntaxError(offset int, msg string) error { return &SyntaxError{Offset: offset, Msg: msg} }
