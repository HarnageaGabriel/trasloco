package ir

import "bytes"

// Equal reports semantic deep equality, including origins and extension JSON.
func Equal(left, right *Pipeline) bool {
	leftCanonical, leftErr := Canonical(left)
	rightCanonical, rightErr := Canonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
