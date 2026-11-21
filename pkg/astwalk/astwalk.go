// Package astwalk walks go/ast trees, yielding typed nodes.
//
// The walks scattered through inspect, vet, and index all repeat the same
// loop — ast.Inspect with a type assertion for the one node kind they want.
// Each[N] is that loop as a range-over-func iterator. Callers that need
// post-order nil events or subtree pruning keep ast.Inspect.
package astwalk

import (
	"go/ast"
	"iter"
)

// Each yields every node of type N under root in ast.Inspect's pre-order,
// until the range body breaks. root may be any node (a file, a decl, an
// expression) or nil.
//
//	for se := range Each[*ast.SelectorExpr](file) {
//		// se is a *ast.SelectorExpr; no assertion needed
//	}
func Each[N ast.Node](root ast.Node) iter.Seq[N] {
	return func(yield func(N) bool) {
		if root == nil {
			return // ast.Walk panics on a nil node
		}
		var done bool
		ast.Inspect(root, func(n ast.Node) bool {
			if done {
				return false
			}
			x, ok := n.(N)
			if !ok {
				return true
			}
			if !yield(x) {
				done = true
				return false
			}
			return true
		})
	}
}
