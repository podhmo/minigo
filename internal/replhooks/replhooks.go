// Package replhooks carries the *minigo.Engine internals the REPL
// front-end (github.com/podhmo/minigo/repl) drives. One Hooks value is a
// session: the fields point at the session engine's state, and the funcs
// are that engine's methods. The surface changes with the interpreter.
package replhooks

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
	"github.com/podhmo/minigo/vm"
)

// Hooks is the slice of engine state a REPL session needs. It is built
// by (*minigo.Engine).ReplHooks and consumed by repl.New; fields are set
// once at session creation and treated read-only afterwards.
type Hooks struct {
	// Session state.
	Fset     *token.FileSet
	Builtins *runtime.Env
	Specials map[runtime.SymbolID]runtime.SpecialFunc
	Binds    map[string]*runtime.Package
	Cfg      resolve.BuildConfig
	Resolver resolve.Resolver
	Cwd      string // the engine's working directory (WorkingDir)

	// Fresh returns a new session's hooks — REPL.Reset builds its
	// replacement session through it.
	Fresh func() *Hooks

	// Evaluation.
	NewVM     func() *vm.VM
	Call      func(ctx context.Context, pkg *runtime.Package, name string, args ...runtime.Value) (runtime.Value, error)
	EvalExpr  func(ctx context.Context, pkg *runtime.Package, file *syntax.File, expr ast.Expr) (runtime.Value, error)
	Bootstrap func(p *runtime.Package, run func(*runtime.Function) error) error

	// Package loading and lookup.
	LoadPath        func(ctx context.Context, path string) (*runtime.Package, error)
	LoadPathFrom    func(ctx context.Context, fromDir, path string) (*runtime.Package, error)
	LoadDir         func(ctx context.Context, dir string) (*runtime.Package, error)
	PackageNameFrom func(ctx context.Context, fromDir, path string) (string, error)
	BoundPackages   func() []string
	LookupDeclIn    func(p *runtime.Package, name string) (*index.Decl, error)
	Materialize     func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)

	// Type machinery — the same helpers member dispatch uses.
	MethodFunc     func(pkg *runtime.Package, recv string, md *index.Decl) *runtime.Function
	MethodFuncs    func(td *runtime.TypeDef, ptr bool, seen map[*runtime.TypeDef]bool) map[string]*runtime.Function
	FieldTypes     func(td *runtime.TypeDef) ([]*runtime.TypeDef, error)
	ElemOf         func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	PeelAliasTd    func(td *runtime.TypeDef) *runtime.TypeDef
	ResolveTypeRef func(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error)

	// BindCompiles attaches the compile hook to *runtime.Function
	// constants of a chunk built for p.
	BindCompiles func(p *runtime.Package, ch *bytecode.Chunk)
	// FormatValue renders a script value the way script fmt does — the
	// REPL's :dump/%#v spelling, where nil composites show their type.
	FormatValue func(c runtime.VMCaller, x runtime.Value) fmt.Formatter
}
