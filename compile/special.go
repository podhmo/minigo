// Package compile lowers Go functions, package initializers and
// expressions to [bytecode.Chunk] values for the minigo stack VM.
//
// Every Go program compiles. A construct outside the supported subset
// compiles to a [bytecode.OpTrap] that fails at run time, with its
// position, only if execution reaches it.
//
// Only locals and upvalues are resolved at compile time. Any other name
// compiles to [bytecode.OpGlobal] or [bytecode.OpSetGlobal] and is looked
// up at run time (file imports, then package globals, then builtins),
// which keeps imports lazy.
package compile

import (
	"go/ast"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// isSpecialCall reports whether x calls a registered special form —
// importIdent.Name matching a canonical symbol in the engine's
// registry. Its dispatch is decided by the registry, not the package
// member, and its arguments stay quoted for the handler — the
// undecidable-call and eager-hoist walks must never rewrite them.
func (c *compiler) isSpecialCall(x *ast.CallExpr) bool {
	if c.pkg == nil || c.pkg.Specials.Len() == 0 {
		return false
	}
	// unwrap F[...] like call() does — explicit instantiation carries
	// no meaning for the quoted handler.
	fun := x.Fun
	for {
		switch ix := fun.(type) {
		case *ast.IndexExpr:
			fun = ix.X
		case *ast.IndexListExpr:
			fun = ix.X
		case *ast.ParenExpr:
			fun = ix.X
		default:
			goto unwrapped
		}
	}
unwrapped:
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	ref, ok := c.importRef(id.Name)
	if !ok {
		return false
	}
	return c.pkg.Specials.Has(runtime.SymbolID{PackagePath: ref.Path, Name: sel.Sel.Name})
}

// trySpecial emits OpSpecialCall when the call's callee resolves to a
// registered special form (importIdent.Name matching a canonical symbol
// in the engine's registry). The arguments stay quoted for the handler.
func (c *compiler) trySpecial(x *ast.CallExpr, sel *ast.SelectorExpr) bool {
	if c.file == nil || !c.isSpecialCall(x) {
		return false
	}
	id := sel.X.(*ast.Ident)
	ref, _ := c.importRef(id.Name)
	sym := runtime.SymbolID{PackagePath: ref.Path, Name: sel.Sel.Name}
	q := &runtime.QuotedCall{Call: x, File: c.file, Locals: map[string]int{}, Upvals: map[string]int{}}
	// snapshot every visible name: outer blocks first, inner shadows win
	for _, block := range c.fs.blocks {
		for name, b := range block {
			q.Locals[name] = b.slot
			// specialCtx.Eval lends the frame's cells to the eval — every
			// snapshotted slot escapes.
			c.fs.box(b.slot)
		}
	}
	for name, i := range c.fs.upmap {
		q.Upvals[name] = i
	}
	c.emit3(bytecode.OpSpecialCall, c.constIdx(sym), c.constIdx(q), 0, x.Pos())
	return true
}
