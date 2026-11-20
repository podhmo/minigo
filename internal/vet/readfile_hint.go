// readfile_hint.go is vet's first performance hint: `b, err :=
// os.ReadFile(p)` materializes the file one runtime.Value per byte,
// which is fine only when the script actually inspects the bytes. When
// every use of the result reads it as text — `string(b)` (or `len(b)`)
// — the bytes crossed the host boundary for nothing: `io.Copy` into a
// strings.Builder keeps the data on the host (README "Performance
// notes").

package vet

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/pkg/astwalk"
)

// scanReadFileHints flags byte vars bound by os.ReadFile whose only
// reads are string(b) or len(b). One disqualifying use — slicing,
// writing, passing to another call, indexing — keeps the var off the
// report; a second `:=`/var redeclaration of the same name does too.
func scanReadFileHints(decl ast.Decl, imports map[string]string, fset func(token.Pos) token.Position, out *[]Result) {
	for as := range astwalk.Each[*ast.AssignStmt](decl) {
		if as.Tok != token.DEFINE || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ReadFile" {
			continue
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || imports[x.Name] != "os" {
			continue
		}
		// the byte result is the first LHS ident; `_, err :=` has none.
		if len(as.Lhs) == 0 {
			continue
		}
		bound, ok := as.Lhs[0].(*ast.Ident)
		if !ok || bound.Name == "_" {
			continue
		}
		if !textOnly(decl, bound) {
			continue
		}
		*out = append(*out, Result{
			Kind:     KindHint,
			Pos:      fset(as.TokPos),
			Call:     "os.ReadFile",
			Resolved: fmt.Sprintf("%s from os.ReadFile is only read as text — io.Copy(&strings.Builder{}, r) keeps the bytes on the host", bound.Name),
		})
	}
}

// textOnly reports whether every mention of bound inside root is an
// argument to string(b)/len(b): at least one such use, no other use,
// and no shadowing redeclaration.
func textOnly(root ast.Node, bound *ast.Ident) bool {
	sawUse := false
	ok := true
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if !ok {
			return false
		}
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		id, isID := n.(*ast.Ident)
		if !isID || id.Name != bound.Name || id == bound {
			return true
		}
		if p, isCall := stack[len(stack)-2].(*ast.CallExpr); isCall {
			if fn, _ := p.Fun.(*ast.Ident); fn != nil && (fn.Name == "string" || fn.Name == "len") {
				for _, a := range p.Args {
					if a == id {
						sawUse = true
						return true
					}
				}
			}
		}
		ok = false // index/slice/call arg, assignment, shadowing decl, …
		return false
	})
	return ok && sawUse
}
