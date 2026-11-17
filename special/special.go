// Package special defines the public types of minigo special forms:
// calls the interpreter hands unevaluated to host handlers registered
// with [github.com/podhmo/minigo.Engine.RegisterSpecial]. The types the
// handler itself needs that depend on runtime values (SpecialFunc,
// SpecialContext) stay in the runtime package — referencing them here
// would create an import cycle.
package special

import (
	"go/ast"

	"github.com/podhmo/minigo/syntax"
)

// SymbolID names a package-level declaration by the import path of the
// package that declares it, whatever alias the calling file imports it
// under. It is the key of a special form: see [SpecialFunc] and
// [SpecialContext.ResolveSymbol].
type SymbolID struct {
	PackagePath string
	Name        string
}

// QuotedCall is the call a [SpecialFunc] handles: Call is the call
// expression with its unevaluated arguments, in File. Evaluate parts of it
// on demand with [SpecialContext.Eval] and related methods; Locals and
// Upvals record the caller's variables they can see.
type QuotedCall struct {
	Call   *ast.CallExpr
	File   *syntax.File
	Locals map[string]int // visible local name -> caller slot index
	Upvals map[string]int // visible upvalue name -> caller upvalue index
}
