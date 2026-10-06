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
	// TokenNumber is a decimal number.
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
	if utf16Length(input) > MaxExpressionLength {
		return nil, syntaxError(0, "expression exceeds length limit")
	}
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
			startsNumber := len(tokens) == 0
			if len(tokens) > 0 {
				k := tokens[len(tokens)-1].Kind
				startsNumber = k == TokenLeftParen || k == TokenLeftBracket || k == TokenComma
			}
			if startsNumber {
				end, kind, err := scanNumber(input, pos)
				if err != nil {
					return nil, err
				}
				tokens = appendToken(tokens, kind, input, start, end)
				pos = end
				continue
			}
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
	}
	dots, digits := 0, 0
	for pos < len(input) {
		c := input[pos]
		switch {
		case c == '.':
			dots++
		case c >= '0' && c <= '9':
			digits++
		default:
			goto done
		}
		pos++
	}
done:
	if digits == 0 {
		return 0, 0, syntaxError(start, "invalid number")
	}
	if dots >= 2 {
		if dots > 3 || input[start] == '-' {
			return 0, 0, syntaxError(start, "invalid version")
		}
		return pos, TokenVersion, nil
	}
	return pos, TokenNumber, nil
}

func isIdentStart(r rune) bool    { return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }
func isIdentContinue(r rune) bool { return isIdentStart(r) || r >= '0' && r <= '9' }

func syntaxError(offset int, msg string) error { return &SyntaxError{Offset: offset, Msg: msg} }
