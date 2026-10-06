package expr

// Visitor receives AST nodes. Returning nil skips a node's children. The
// returned visitor is called with nil after those children have been visited.
type Visitor interface {
	Visit(Expr) Visitor
}

// VisitorFunc adapts a function to Visitor. It has pre-order semantics and no
// post-order nil callback.
type VisitorFunc func(Expr)

// Visit implements Visitor.
func (f VisitorFunc) Visit(node Expr) Visitor {
	if node != nil {
		f(node)
	}
	return f
}

// Walk traverses an expression depth-first.
func Walk(visitor Visitor, node Expr) {
	if visitor == nil || node == nil {
		return
	}
	next := visitor.Visit(node)
	if next == nil {
		return
	}
	switch n := node.(type) {
	case *Call:
		for _, arg := range n.Args {
			Walk(next, arg)
		}
	case *Member:
		Walk(next, n.Receiver)
	case *Index:
		Walk(next, n.Receiver)
		Walk(next, n.Key)
	}
	next.Visit(nil)
}
