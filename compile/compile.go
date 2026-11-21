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
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/spike"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// compiler holds the state for one chunk under construction.
type compiler struct {
	pkg  *runtime.Package // may be nil
	file *syntax.File
	fs   *fscope
	ch   *bytecode.Chunk
	ctrl []*ctrlCtx

	binds         map[string]runtime.Value // generic instantiation: type-param name -> *TypeDef
	results       []ast.Expr               // declared result types, parallel to NamedSlots order
	labels        map[string]*labelInfo
	pendingGotos  []pendingGoto
	pendingLabels []*labelInfo // labels waiting to be claimed by a construct
	falls         *[]int       // fallthrough jump sites in the current case body

	// symName is the enclosing function's Go-style symbol name
	// ("main.main"); litCount numbers its func literals so a literal
	// spells "main.main.func1" like the toolchain names it.
	symName  string
	litCount int

	// strictLazy makes every indexed-fact query run under
	// semantics.IndexedOnly: what is not already indexed comes back
	// Deferred — compilation never triggers an indexing build.
	strictLazy bool

	// opTypDepth bounds staticOpTyp's recursion through package-level
	// var/const initializers (a named cycle would not terminate).
	opTypDepth int

	// deferCallTemps maps a hoist scratch name to its undecidable member
	// call — `p.T(x)` under IndexedOnly, where conversion-vs-call is a
	// run-time question: the ident emits the member-shape branch
	// instead of a plain local load.
	deferCallTemps map[string]*deferredCallTemp
	// sawDeferred records that the last fact check (isConstExpr,
	// certainlyNonConst) hit a deferred package member — the caller
	// emits the CallSite variant instead of guessing. Cleared before
	// each check.
	sawDeferred bool

	// pkgScopeFold marks a const fold as resolving in package scope:
	// a package-level var's initializer re-derives its literal's shape
	// at the Sizeof call site, whose local bindings must not shadow
	// the names the decl's keys mean.
	pkgScopeFold bool

	// pkgScopeTyp, when non-nil, makes typeExpr resolve the emitted
	// type in package scope — under the file a package decl spelled
	// it in — because the typedef re-derived from a package var spec
	// at a use site whose locals (and local type decls) must not
	// shadow the decl's names.
	pkgScopeTyp *syntax.File

	// synthImports binds qualifier identifiers minted by
	// synthImportName to their import refs: an operand or type
	// expression re-scoped through a foreign package's file refers to
	// that package's own imports, which the caller may not import at
	// all — caller-import rewriting cannot express that, so the minted
	// qualifier carries the ref itself. ".imp:" can never collide
	// with a declared identifier.
	synthImports map[string]*runtime.ImportRef

	// iotaVal is the const spec index while compiling a const spec's
	// values; -1 elsewhere. `iota` reads emit the constant — the hidden
	// local backing it materializes to int64 in storage, which would
	// drag a float-constant expression into the float64 domain.
	iotaVal int

	// synthSeq numbers every compiler-synthesized local name. Hoisted
	// call-argument scratch slots, invented param/receiver names and
	// iterator/tag/select scratch slots all mint through c.fresh, so a
	// $-name can never collide with one still live in the same scope —
	// the class of bug where a nested call's $argN overwrote the outer
	// call's (#118).
	synthSeq int
}

// fresh mints a synthetic local name unique across the whole
// compilation: c.fresh("$arg") -> "$arg3". Numbering all $-names from
// one sequence is the simplest way to keep every synthesized slot
// distinct regardless of which construct produced it.
func (c *compiler) fresh(base string) string {
	name := fmt.Sprintf("%s%d", base, c.synthSeq)
	c.synthSeq++
	return name
}

func (c *compiler) emit(op bytecode.Op, a, b int, pos token.Pos) int {
	ins := bytecode.Instruction{Op: op, A: int32(a), B: int32(b), C: -1, Pos: pos}
	if op == bytecode.OpGlobal {
		// C names the site's resolution cache (bytecode.GlobalSite).
		ins.C = int32(len(c.ch.Sites))
		c.ch.Sites = append(c.ch.Sites, &bytecode.GlobalSite{})
	}
	c.ch.Code = append(c.ch.Code, ins)
	return len(c.ch.Code) - 1
}

func (c *compiler) emit3(op bytecode.Op, a, b, cc int, pos token.Pos) int {
	i := c.emit(op, a, b, pos)
	c.ch.Code[i].C = int32(cc)
	return i
}

func (c *compiler) patchA(i, target int) { c.ch.Code[i].A = int32(target) }

func (c *compiler) trap(pos token.Pos, format string, args ...any) int {
	return c.emit(bytecode.OpTrap, c.constIdx(fmt.Sprintf(format, args...)), 0, pos)
}

func (c *compiler) constIdx(v any) int {
	c.ch.Consts = append(c.ch.Consts, v)
	return len(c.ch.Consts) - 1
}

func (c *compiler) nameIdx(s string) int { return c.constIdx(s) }

// Func compiles fn.Decl into fn.Chunk.
func Func(fn *runtime.Function) error { return funcStrict(fn, false) }

// FuncStrict is [Func] under strict lazy compilation ([WithStrictLazyCompile]
// on the engine): every indexed-fact query runs IndexedOnly, so a package
// not yet indexed defers instead of building at compile time.
func FuncStrict(fn *runtime.Function) error { return funcStrict(fn, true) }

func funcStrict(fn *runtime.Function, strict bool) error {
	if len(fn.Binds) == 0 || fn.Pkg == nil {
		return compileFuncStrict(fn, strict)
	}
	// a generic instantiation compiles once per distinct binds: every
	// call of an inferred generic mints a fresh Function
	key, ok := runtime.InstKey(fn.Decl, fn.File, fn.Name, fn.Binds)
	if ok {
		if ch, hit := fn.Pkg.InstChunk(key); hit {
			fn.Chunk = ch
			return nil
		}
	}
	if err := compileFuncStrict(fn, strict); err != nil {
		return err
	}
	if ok {
		fn.Pkg.SetInstChunk(key, fn.Decl, fn.Binds, fn.Chunk)
	}
	return nil
}

func compileFuncStrict(fn *runtime.Function, strict bool) error {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	c := &compiler{pkg: fn.Pkg, file: fn.File, fs: newFScope(nil), ch: &bytecode.Chunk{Name: fn.Name}, labels: map[string]*labelInfo{}, binds: fn.Binds, symName: fn.Name, iotaVal: -1, strictLazy: strict}
	if fn.Pkg != nil {
		c.symName = fn.Pkg.Name + "." + fn.Name
	}
	c.fs.pushBlock()

	// Params (and the receiver for methods) are pre-bound by the VM into
	// slots 0..NParams-1; they are only declared here, not re-created.
	var coerces []paramCoerce // declared-type coercions for the prologue
	nparams := 0
	if fn.Decl.Recv != nil {
		recv := c.fresh("$recv")
		var recvType ast.Expr
		if len(fn.Decl.Recv.List) > 0 {
			if len(fn.Decl.Recv.List[0].Names) > 0 {
				recv = fn.Decl.Recv.List[0].Names[0].Name
			}
			recvType = c.recvTypeExpr(fn.Decl.Recv.List[0].Type)
		}
		rpos := token.NoPos
		if len(fn.Decl.Recv.List[0].Names) > 0 {
			rpos = fn.Decl.Recv.List[0].Names[0].Pos()
		} else if recvType != nil {
			rpos = recvType.Pos()
		}
		slot := c.fs.declare(recv, rpos)
		nparams++
		if recvType != nil {
			coerces = append(coerces, paramCoerce{slot: slot, typ: recvType})
			c.fs.noteDeclTyp(recv, c.declTypCanon(recvType), nil)
		}
	}
	if fn.Decl.Type.Params != nil {
		for _, field := range fn.Decl.Type.Params.List {
			names := field.Names
			if len(names) == 0 {
				names = []*ast.Ident{{Name: c.fresh("$arg")}}
			}
			for _, n := range names {
				slot := c.fs.declare(n.Name, n.Pos())
				if c.isIfaceTypeExpr(field.Type) {
					c.fs.markIface(n.Name)
				}
				coerces = append(coerces, paramCoerce{slot: slot, typ: field.Type})
				c.fs.noteDeclTyp(n.Name, c.declTypCanon(field.Type), nil)
				nparams++
			}
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				c.ch.IsVararg = true
			}
		}
	}
	c.ch.NParams = nparams
	// declared param types coerce the bound args (e.g. a typed nil arg
	// crossing into an interface-typed param boxes it, like Go).
	c.emitParamCoerces(coerces)

	// Named results are local cells initialized to their declared zero
	// (var-style coerce) so a bare `return` under `func f() (r *T)`
	// yields a typed nil, not untyped NIL.
	nresults := 0
	if fn.Decl.Type.Results != nil {
		nresults = countResults(fn.Decl.Type.Results)
		for _, field := range fn.Decl.Type.Results.List {
			for _, n := range field.Names {
				slot := c.fs.declare(n.Name, n.Pos())
				if c.isIfaceTypeExpr(field.Type) {
					c.fs.markIface(n.Name)
				}
				c.fs.noteDeclTyp(n.Name, c.declTypCanon(field.Type), nil)
				c.ch.NamedSlots = append(c.ch.NamedSlots, slot)
				c.emit(bytecode.OpNil, 0, 0, n.Pos())
				c.emit(bytecode.OpNewLocal, slot, 0, n.Pos())
				c.emitSigTypeCoerce(slot, field.Type, n.Pos())
			}
		}
	}
	c.ch.NResults = nresults
	c.results = syntax.FieldTypes(fn.Decl.Type.Results)

	if fn.Decl.Body != nil {
		// the body's outermost scope is the signature's block — params
		// and named results live beside top-level `:=` names, so
		// `a, b, s := f()` with s a result name assigns, not shadows.
		for _, s := range fn.Decl.Body.List {
			c.stmt(s)
		}
		c.resolveGotos()
	} else {
		// a bodiless declaration (//go:linkname stubs, assembly decls)
		// compiles to a no-op returning its declared zero values.
		for _, rt := range c.results {
			c.emit(bytecode.OpNil, 0, 0, fn.Decl.End())
			c.sigTypeExpr(rt)
			c.emit(bytecode.OpCoerceTop, 0, 0, fn.Decl.End())
		}
	}
	// implicit return
	c.emit(bytecode.OpReturn, nresults, 0, fn.Decl.End())
	c.ch.NLocals = c.fs.nlocals
	c.ch.Upvals = c.fs.upvals
	fn.Chunk = c.ch
	return nil
}

// Expr compiles e into a chunk that returns the expression's value. There
// are no locals: free identifiers resolve at run time to imports, package
// globals and builtins. Use [ExprScoped] to resolve names against a running
// function's frame instead.
func Expr(pkg *runtime.Package, file *syntax.File, e ast.Expr) (*bytecode.Chunk, error) {
	return ExprStrict(pkg, file, e, false)
}

// ExprStrict is [Expr] under strict lazy compilation.
func ExprStrict(pkg *runtime.Package, file *syntax.File, e ast.Expr, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	c := &compiler{pkg: pkg, file: file, fs: newFScope(nil), ch: &bytecode.Chunk{Name: "<eval>"}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()
	c.expr(e)
	c.emit(bytecode.OpReturn, 1, 0, e.End())
	c.ch.NLocals = c.fs.nlocals
	return c.ch, nil
}

// ExprScoped is [Expr] with free identifiers resolved against a caller's
// frame, as special forms need: locals and upvals map names to indexes in
// that frame's locals and upvalue tables. The chunk reads and writes the
// caller's live cells.
func ExprScoped(pkg *runtime.Package, file *syntax.File, e ast.Expr, locals, upvals map[string]int) (*bytecode.Chunk, error) {
	return ExprScopedStrict(pkg, file, e, locals, upvals, false)
}

// ExprScopedStrict is [ExprScoped] under strict lazy compilation.
func ExprScopedStrict(pkg *runtime.Package, file *syntax.File, e ast.Expr, locals, upvals map[string]int, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	c := &compiler{pkg: pkg, file: file, fs: newFScope(nil), ch: &bytecode.Chunk{Name: "<special-eval>"}, labels: map[string]*labelInfo{}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()
	max := -1
	for name, slot := range locals {
		c.fs.blocks[0][name] = &binding{slot: slot}
		if slot > max {
			max = slot
		}
	}
	c.fs.nlocals = max + 1
	// Pre-seeding upmap both resolves the name and pins the caller's index —
	// find() returns it without consulting the (nil) parent scope.
	for name, i := range upvals {
		c.fs.upmap[name] = i
	}
	c.expr(e)
	c.emit(bytecode.OpReturn, 1, 0, e.End())
	c.ch.NLocals = c.fs.nlocals
	return c.ch, nil
}

// InitFunc compiles the package initializer: const and var declarations in
// dependency order, as Go initializes them, then the init functions. Use
// [ConstInitFunc] to bind only the constants, without running var
// initializers or init functions.
func InitFunc(pkg *runtime.Package) (*bytecode.Chunk, error) {
	return InitFuncStrict(pkg, false)
}

// InitFuncStrict is [InitFunc] under strict lazy compilation.
func InitFuncStrict(pkg *runtime.Package, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	c := &compiler{pkg: pkg, fs: newFScope(nil), ch: &bytecode.Chunk{Name: pkg.Name + ".__init__"}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()

	// iota is a real identifier in const specs; bind it as a hidden local
	// (declared last wins — it shadows nothing here).
	iotaSlot := c.fs.declare("iota", token.NoPos)
	c.fs.iota = iotaSlot
	c.emit(bytecode.OpConst, c.constIdx(int64(0)), 0, 0)
	c.emit(bytecode.OpNewLocal, iotaSlot, 0, 0)

	// One representative decl per spec, in source order.
	var specReps []*index.Decl
	seen := map[*ast.ValueSpec]bool{}
	for _, d := range pkg.Index.Decls {
		if d.Kind != index.ConstDecl && d.Kind != index.VarDecl {
			continue
		}
		vs := d.Spec.(*ast.ValueSpec)
		if seen[vs] {
			continue
		}
		seen[vs] = true
		specReps = append(specReps, d)
	}
	// Go initializes vars/consts in dependency order, not textual order.
	for _, d := range orderSpecs(pkg.Index, specReps) {
		c.file = d.File // per-file: import aliases and scope lookups
		if d.Kind == index.ConstDecl {
			c.emit(bytecode.OpConst, c.constIdx(int64(d.Idx)), 0, d.Pos)
			c.emit(bytecode.OpSetLocal, iotaSlot, 0, d.Pos)
			c.iotaVal = d.Idx
		}
		c.valueSpec(d.Spec.(*ast.ValueSpec), d)
		c.iotaVal = -1
	}
	for _, d := range pkg.Index.Inits {
		c.file = d.File
		fv := &runtime.Function{Pkg: pkg, File: d.File, Decl: d.Func, Name: "init"}
		c.emit(bytecode.OpConst, c.constIdx(fv), 0, d.Pos)
		c.emit(bytecode.OpCall, 0, 0, d.Pos)
		c.emit(bytecode.OpPop, 0, 0, d.Pos)
	}
	c.emit(bytecode.OpReturn, 0, 0, 0)
	c.ch.NLocals = c.fs.nlocals
	c.ch.Upvals = c.fs.upvals
	return c.ch, nil
}

// ConstInitFunc compiles an initializer for the package's constants only,
// in dependency order, without var declarations or init functions, so
// constants bind without the package's initialization side effects. It
// returns a nil chunk and nil error when a const initializer references a
// package-level var or function; use [InitFunc] then.
func ConstInitFunc(pkg *runtime.Package) (*bytecode.Chunk, error) {
	return ConstInitFuncStrict(pkg, false)
}

// ConstInitFuncStrict is [ConstInitFunc] under strict lazy compilation.
func ConstInitFuncStrict(pkg *runtime.Package, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	var specReps []*index.Decl
	seen := map[*ast.ValueSpec]bool{}
	for _, d := range pkg.Index.Decls {
		if d.Kind != index.ConstDecl {
			continue
		}
		vs := d.Spec.(*ast.ValueSpec)
		if seen[vs] {
			continue
		}
		seen[vs] = true
		specReps = append(specReps, d)
	}
	if constRefsVarOrFunc(pkg.Index, specReps) {
		// such a program needs the full initializer: either the code is
		// not really constant, or a builtin operand like len/Sizeof still
		// evaluates the var.
		return nil, nil
	}

	c := &compiler{pkg: pkg, fs: newFScope(nil), ch: &bytecode.Chunk{Name: pkg.Name + ".__consts__"}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()

	// same iota plumbing as InitFunc: a hidden local backs the `iota`
	// builtin inside const specs.
	iotaSlot := c.fs.declare("iota", token.NoPos)
	c.fs.iota = iotaSlot
	c.emit(bytecode.OpConst, c.constIdx(int64(0)), 0, 0)
	c.emit(bytecode.OpNewLocal, iotaSlot, 0, 0)

	for _, d := range orderSpecs(pkg.Index, specReps) {
		c.file = d.File
		c.emit(bytecode.OpConst, c.constIdx(int64(d.Idx)), 0, d.Pos)
		c.emit(bytecode.OpSetLocal, iotaSlot, 0, d.Pos)
		c.iotaVal = d.Idx
		c.valueSpec(d.Spec.(*ast.ValueSpec), d)
		c.iotaVal = -1
	}
	c.emit(bytecode.OpReturn, 0, 0, 0)
	c.ch.NLocals = c.fs.nlocals
	c.ch.Upvals = c.fs.upvals
	return c.ch, nil
}
