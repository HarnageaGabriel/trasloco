// Package expr parses and evaluates Azure Pipelines expressions.
package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Span is a half-open byte range in the source expression.
type Span struct {
	Start int
	End   int
}

// Expr is an expression AST node.
type Expr interface {
	Range() Span
	exprNode()
}

// LiteralKind identifies a literal's source type.
type LiteralKind uint8

const (
	// NullLiteral represents the null keyword.
	NullLiteral LiteralKind = iota
	// BooleanLiteral represents a true or false keyword.
	BooleanLiteral
	// NumberLiteral represents a floating-point number.
	NumberLiteral
	// StringLiteral represents a single-quoted string.
	StringLiteral
	// VersionLiteral represents a dotted version.
	VersionLiteral
)

// Literal is a null, boolean, number, string, or version literal.
type Literal struct {
	Span  Span
	Kind  LiteralKind
	Value any
}

func (*Literal) exprNode() {}

// Range returns the literal's source range.
func (n *Literal) Range() Span { return n.Span }

// Identifier names a context root such as variables or parameters.
type Identifier struct {
	Span Span
	Name string
}

func (*Identifier) exprNode() {}

// Range returns the identifier's source range.
func (n *Identifier) Range() Span { return n.Span }

// Call invokes a built-in function.
type Call struct {
	Span Span
	Name string
	Args []Expr
}

func (*Call) exprNode() {}

// Range returns the call's source range.
func (n *Call) Range() Span { return n.Span }

// Member selects a property. Wildcard represents the filtered-array `*`.
type Member struct {
	Span     Span
	Receiver Expr
	Name     string
	Wildcard bool
}

func (*Member) exprNode() {}

// Range returns the member expression's source range.
func (n *Member) Range() Span { return n.Span }

// Index selects an object property or array element.
type Index struct {
	Span     Span
	Receiver Expr
	Key      Expr
}

func (*Index) exprNode() {}

// Range returns the index expression's source range.
func (n *Index) Range() Span { return n.Span }

// Version is an Azure Pipelines version literal with two to four components.
type Version struct {
	Parts []int
}

func (v Version) String() string {
	parts := make([]string, len(v.Parts))
	for i, part := range v.Parts {
		parts[i] = strconv.Itoa(part)
	}
	return strings.Join(parts, ".")
}

func parseVersion(s string) (Version, error) {
	fields := strings.Split(s, ".")
	if len(fields) < 2 || len(fields) > 4 {
		return Version{}, fmt.Errorf("version must have 2 to 4 components")
	}
	parts := make([]int, len(fields))
	for i, field := range fields {
		part, err := strconv.Atoi(field)
		if err != nil || part < 0 {
			return Version{}, fmt.Errorf("invalid version component %q", field)
		}
		parts[i] = part
	}
	return Version{Parts: parts}, nil
}
