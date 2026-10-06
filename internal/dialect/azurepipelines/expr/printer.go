package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Print returns the canonical unwrapped representation of an AST.
func Print(node Expr) string {
	var b strings.Builder
	printExpr(&b, node)
	return b.String()
}

func printExpr(b *strings.Builder, node Expr) {
	switch n := node.(type) {
	case *Literal:
		switch n.Kind {
		case NullLiteral:
			b.WriteString("null")
		case BooleanLiteral:
			b.WriteString(strconv.FormatBool(bool(n.Value.(Bool))))
		case NumberLiteral:
			b.WriteString(n.Value.(Number).ExactString())
		case StringLiteral:
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(string(n.Value.(String)), "'", "''"))
			b.WriteByte('\'')
		case VersionLiteral:
			b.WriteString(n.Value.(Version).String())
		}
	case *Identifier:
		b.WriteString(n.Name)
	case *Call:
		b.WriteString(strings.ToLower(n.Name))
		b.WriteByte('(')
		for i, arg := range n.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			printExpr(b, arg)
		}
		b.WriteByte(')')
	case *Member:
		printExpr(b, n.Receiver)
		b.WriteByte('.')
		if n.Wildcard {
			b.WriteByte('*')
		} else {
			b.WriteString(n.Name)
		}
	case *Index:
		printExpr(b, n.Receiver)
		b.WriteByte('[')
		printExpr(b, n.Key)
		b.WriteByte(']')
	default:
		panic(fmt.Sprintf("expr.Print: unknown node %T", node))
	}
}
