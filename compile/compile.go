// Package compile lowers per-function Go AST to stack-VM bytecode.
//
// The compiler is a total function: every Go program compiles. Any construct
// outside the supported subset emits OpTrap at its position instead of an
// error, so a single unsupported node never blocks the rest of the package.
//
// Scoping: only locals and upvalues are resolved statically. Anything that
// is not a local compiles to OpGlobal/OpSetGlobal and is resolved dynamically
// at run time (package globals, then file imports, then builtins) — this is
// what keeps imports lazy.
package compile

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"math"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// fscope is the static scope model of one function while compiling.
type fscope struct {
	parent    *fscope
	blocks    []map[string]int       // name -> local slot
	blockIDs  []int                  // unique id per open block, for goto scoping
	declPos   []map[string]token.Pos // name -> declaring position (goto scoping)
	nextID    int
	typeDecls map[string]bool // names bound by local `type` decls (not vars)
	nlocals   int
	upvals    []bytecode.UpvalDesc
	upmap     map[string]int
}

func newFScope(parent *fscope) *fscope {
	return &fscope{parent: parent, upmap: map[string]int{}, typeDecls: map[string]bool{}}
}

func (s *fscope) pushBlock() {
	s.nextID++
	s.blocks = append(s.blocks, map[string]int{})
	s.blockIDs = append(s.blockIDs, s.nextID)
	s.declPos = append(s.declPos, map[string]token.Pos{})
}
func (s *fscope) popBlock() {
	s.blocks = s.blocks[:len(s.blocks)-1]
	s.blockIDs = s.blockIDs[:len(s.blockIDs)-1]
	s.declPos = s.declPos[:len(s.declPos)-1]
}

// scopeSnapshot captures which blocks are open and which variables are
// visible — the two things Go's goto legality rules compare between a
// goto and its target label. A variable is identified by its declaring
// position, not its name: an inner `x :=` that shadows an outer one is a
// different variable, so jumping over it is illegal even though the name
// was already visible.
func (s *fscope) scopeSnapshot() (blocks map[int]bool, vars map[string]token.Pos) {
	blocks = map[int]bool{}
	for _, id := range s.blockIDs {
		blocks[id] = true
	}
	vars = map[string]token.Pos{}
	for i := len(s.declPos) - 1; i >= 0; i-- {
		for n, dpos := range s.declPos[i] {
			if s.typeDecls[n] || strings.HasPrefix(n, "$") {
				continue // type decls and internal slots are not variable decls
			}
			if _, seen := vars[n]; !seen {
				vars[n] = dpos
			}
		}
	}
	return blocks, vars
}

func (s *fscope) declare(name string, pos token.Pos) int {
	slot := s.nlocals
	s.nlocals++
	if len(s.blocks) == 0 {
		s.pushBlock()
	}
	s.blocks[len(s.blocks)-1][name] = slot
	s.declPos[len(s.declPos)-1][name] = pos
	return slot
}

func (s *fscope) lookupLocal(name string) (int, bool) {
	for i := len(s.blocks) - 1; i >= 0; i-- {
		if slot, ok := s.blocks[i][name]; ok {
			return slot, true
		}
	}
	return 0, false
}

// inCurrentBlock reports whether name is declared in the innermost block —
// `x := 1` on an existing same-block name is a redefinition (assignment),
// while an outer-block name is shadowed by a fresh cell.
func (s *fscope) inCurrentBlock(name string) bool {
	if len(s.blocks) == 0 {
		return false
	}
	_, ok := s.blocks[len(s.blocks)-1][name]
	return ok
}

// find reports how name is reachable inside this function's frame:
// a local slot (isUpval=false) or an upvalue index (isUpval=true).
func (s *fscope) find(name string) (isUpval bool, idx int, ok bool) {
	if slot, found := s.lookupLocal(name); found {
		return false, slot, true
	}
	if i, found := s.upmap[name]; found {
		return true, i, true
	}
	if s.capture(name) {
		return true, s.upmap[name], true
	}
	return false, 0, false
}

// capture pulls name from the enclosing frame into this function's upvalue
// table. Returns false when the name is not bound in any enclosing function
// (a global reference).
func (s *fscope) capture(name string) bool {
	if s.parent == nil {
		return false
	}
	if _, done := s.upmap[name]; done {
		return true
	}
	fromUpval, idx, ok := s.parent.find(name)
	if !ok {
		return false
	}
	s.upvals = append(s.upvals, bytecode.UpvalDesc{FromParentUpval: fromUpval, Index: idx})
	s.upmap[name] = len(s.upvals) - 1
	return true
}

// ctrlCtx is the patching context for breakable/continuable constructs
// (for, range, switch, select).
type ctrlCtx struct {
	isLoop     bool
	labels     []string // label names attached by enclosing LabeledStmt
	continueIP int      // -1 until post position is known
	continues  []int    // continue jumps to patch once continueIP is known
	breaks     []int    // instruction indices to patch to construct end
}

// labelInfo is a label statement's jump target (ip is its instruction
// position). `goto L` resolves against it; `break L`/`continue L` find the
// control construct it was attached to.
type labelInfo struct {
	name   string
	ip     int
	blocks map[int]bool         // blocks open at the label
	vars   map[string]token.Pos // variables in scope at the label
}

// pendingGoto is a goto emitted before its label was defined; resolved at
// the end of the function's compilation.
type pendingGoto struct {
	ins    int
	name   string
	pos    token.Pos
	blocks map[int]bool         // blocks open at the goto
	vars   map[string]token.Pos // variables in scope at the goto
}

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
}

func (c *compiler) emit(op bytecode.Op, a, b int, pos token.Pos) int {
	c.ch.Code = append(c.ch.Code, bytecode.Instruction{Op: op, A: int32(a), B: int32(b), C: -1, Pos: pos})
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

// ---- name resolution ----

func (c *compiler) getRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		// compile-time bindings from generic instantiation (type param -> TypeDef)
		if bv, bound := c.binds[name]; bound {
			c.emit(bytecode.OpConst, c.constIdx(bv), 0, pos)
			return
		}
		c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, pos)
	case !isUp:
		c.emit(bytecode.OpLocal, idx, 0, pos)
	default:
		c.emit(bytecode.OpUpval, idx, 0, pos)
	}
}

func (c *compiler) setRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		c.emit(bytecode.OpSetGlobal, c.nameIdx(name), 0, pos)
	case !isUp:
		c.emit(bytecode.OpSetLocal, idx, 0, pos)
	default:
		c.emit(bytecode.OpSetUpval, idx, 0, pos)
	}
}

func (c *compiler) refRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		c.emit(bytecode.OpGlobalRef, c.nameIdx(name), 0, pos)
	case !isUp:
		c.emit(bytecode.OpLocalRef, idx, 0, pos)
	default:
		c.trap(pos, "cannot take address of captured variable %s", name)
	}
}

// ---- public entry points ----

// Func compiles fn.Decl into fn.Chunk.
func Func(fn *runtime.Function) error {
	c := &compiler{pkg: fn.Pkg, file: fn.File, fs: newFScope(nil), ch: &bytecode.Chunk{Name: fn.Name}, labels: map[string]*labelInfo{}, binds: fn.Binds}
	c.fs.pushBlock()

	// Params (and the receiver for methods) are pre-bound by the VM into
	// slots 0..NParams-1; they are only declared here, not re-created.
	var coerces []paramCoerce // declared-type coercions for the prologue
	nparams := 0
	if fn.Decl.Recv != nil {
		recv := "$recv"
		var recvType ast.Expr
		if len(fn.Decl.Recv.List) > 0 {
			if len(fn.Decl.Recv.List[0].Names) > 0 {
				recv = fn.Decl.Recv.List[0].Names[0].Name
			}
			recvType = fn.Decl.Recv.List[0].Type
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
		}
	}
	if fn.Decl.Type.Params != nil {
		for _, field := range fn.Decl.Type.Params.List {
			names := field.Names
			if len(names) == 0 {
				names = []*ast.Ident{{Name: fmt.Sprintf("$arg%d", nparams)}}
			}
			for _, n := range names {
				slot := c.fs.declare(n.Name, n.Pos())
				coerces = append(coerces, paramCoerce{slot: slot, typ: field.Type})
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
				c.ch.NamedSlots = append(c.ch.NamedSlots, slot)
				c.emit(bytecode.OpNil, 0, 0, n.Pos())
				c.emit(bytecode.OpNewLocal, slot, 0, n.Pos())
				c.emitTypeCoerce(slot, field.Type, n.Pos())
			}
		}
	}
	c.ch.NResults = nresults
	c.results = resultTypes(fn.Decl.Type.Results)

	if fn.Decl.Body != nil {
		c.stmt(fn.Decl.Body)
		c.resolveGotos()
	} else {
		// a bodiless declaration (//go:linkname stubs, assembly decls)
		// compiles to a no-op returning its declared zero values.
		for _, rt := range c.results {
			c.emit(bytecode.OpNil, 0, 0, fn.Decl.End())
			c.typeExpr(rt)
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

// Expr compiles a bare AST expression into a chunk that pushes the
// expression's value and returns. It backs the OpEvalAST migration bridge:
// interpreter-visible fragments (e.g. future special-form bodies) are kept
// as AST and compiled on first execution. The expression resolves names
// like a function body does — locals don't exist, so free identifiers fall
// through to package globals, imports, and builtins at run time.
func Expr(pkg *runtime.Package, file *syntax.File, e ast.Expr) (*bytecode.Chunk, error) {
	c := &compiler{pkg: pkg, file: file, fs: newFScope(nil), ch: &bytecode.Chunk{Name: "<eval>"}}
	c.fs.pushBlock()
	c.expr(e)
	c.emit(bytecode.OpReturn, 1, 0, e.End())
	c.ch.NLocals = c.fs.nlocals
	return c.ch, nil
}

// ExprScoped compiles expr so free identifiers resolve against a caller
// frame: locals/upvals are name->index maps into the caller's locals and
// upvalue tables. It backs special-form Eval — the produced chunk reads
// and writes the caller's live cells.
func ExprScoped(pkg *runtime.Package, file *syntax.File, e ast.Expr, locals, upvals map[string]int) (*bytecode.Chunk, error) {
	c := &compiler{pkg: pkg, file: file, fs: newFScope(nil), ch: &bytecode.Chunk{Name: "<special-eval>"}, labels: map[string]*labelInfo{}}
	c.fs.pushBlock()
	max := -1
	for name, slot := range locals {
		c.fs.blocks[0][name] = slot
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

// paramCoerce is a slot + declared-type pair for the function prologue.
type paramCoerce struct {
	slot int
	typ  ast.Expr
}

// emitParamCoerces emits typeExpr+OpCoerce for each declared parameter.
// The coerce carries the parameter type's position so a failed coercion
// points at the signature, not nowhere.
func (c *compiler) emitParamCoerces(pcs []paramCoerce) {
	for _, pc := range pcs {
		c.emitTypeCoerce(pc.slot, pc.typ, pc.typ.Pos())
	}
}

// emitTypeCoerce emits typeExpr(t) + OpCoerce(slot). The type expr keeps
// the compiler total: an unresolvable type name traps only when reached.
func (c *compiler) emitTypeCoerce(slot int, t ast.Expr, pos token.Pos) {
	if t == nil {
		return
	}
	c.typeExpr(t)
	c.emit(bytecode.OpCoerce, slot, 0, pos)
}

// resultTypes flattens the declared result types, one per result value.
func resultTypes(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, f.Type)
		}
	}
	return out
}

func countResults(fl *ast.FieldList) int {
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// InitFunc compiles the synthetic package initializer: const/var declarations
// in file order, then init() calls.
func InitFunc(pkg *runtime.Package) (*bytecode.Chunk, error) {
	c := &compiler{pkg: pkg, fs: newFScope(nil), ch: &bytecode.Chunk{Name: pkg.Name + ".__init__"}}
	c.fs.pushBlock()

	// iota is a real identifier in const specs; bind it as a hidden local
	// (declared last wins — it shadows nothing here).
	iotaSlot := c.fs.declare("iota", token.NoPos)
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
		}
		c.valueSpec(d.Spec.(*ast.ValueSpec), d)
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

// valueSpec emits a whole var/const spec: all of its names are bound.
// Vars become package cells (OpNewGlobal); consts read-only cells
// (OpNewGlobal with B=1) so a later `k = v` store traps like Go.
func (c *compiler) valueSpec(vs *ast.ValueSpec, d *index.Decl) {
	isConst := d.Kind == index.ConstDecl
	bind := func(name *ast.Ident) {
		readonly := int32(0)
		if isConst {
			readonly = 1
		}
		c.emit(bytecode.OpNewGlobal, c.nameIdx(name.Name), int(readonly), name.Pos())
	}
	vals := vs.Values
	if isConst && len(vals) == 0 {
		vals = d.Inherited
	}
	// `var x T` at package level gets the same declared-type coerce as a
	// local: zero values materialize (var s Sq -> Struct), typed nils too.
	coerceVar := func(name *ast.Ident) {
		if isConst || vs.Type == nil {
			return
		}
		c.typeExpr(vs.Type)
		c.emit(bytecode.OpCoerceGlobal, c.nameIdx(name.Name), 0, name.Pos())
	}
	// A typed decl coerces its value on the stack before binding — consts
	// are stored as plain globals OpCoerceGlobal cannot reach, and a var
	// would otherwise materialize an untyped-constant value at bind time,
	// losing `var r MyRune = 'a'`-style direct conversion to the decl.
	coerceTop := func() {
		if vs.Type == nil {
			return
		}
		c.typeExpr(vs.Type)
		c.emit(bytecode.OpCoerceTop, 0, 0, vs.Pos())
	}
	switch {
	case len(vals) == 0:
		for _, name := range vs.Names {
			c.emit(bytecode.OpNil, 0, 0, name.Pos())
			bind(name)
			coerceVar(name)
		}
	case len(vals) == 1 && len(vs.Names) > 1:
		c.expr(vals[0])
		c.emit3(bytecode.OpUnpack, len(vs.Names), 0, 0, vs.Pos())
		for i := len(vs.Names) - 1; i >= 0; i-- {
			coerceTop()
			bind(vs.Names[i])
			coerceVar(vs.Names[i])
		}
	default:
		for i, name := range vs.Names {
			c.expr(vals[i])
			coerceTop()
			bind(name)
			coerceVar(name)
		}
	}
}

// ---- statements ----

func (c *compiler) stmt(s ast.Stmt) {
	switch st := s.(type) {
	case *ast.BlockStmt:
		c.fs.pushBlock()
		for _, x := range st.List {
			c.stmt(x)
		}
		c.fs.popBlock()
	case *ast.ExprStmt:
		c.expr(st.X)
		c.emit(bytecode.OpPop, 0, 0, st.Pos())
	case *ast.DeclStmt:
		gd := st.Decl.(*ast.GenDecl)
		for _, spec := range gd.Specs {
			switch gd.Tok {
			case token.VAR, token.CONST:
				vs := spec.(*ast.ValueSpec)
				isConst := gd.Tok == token.CONST
				// `var x T` binds a typed zero / typed nil via OpCoerce; typed
				// consts coerce the same way — locals are always cells.
				coerce := func(name *ast.Ident, slot int) {
					if vs.Type == nil || slot < 0 {
						return
					}
					c.emitTypeCoerce(slot, vs.Type, name.Pos())
				}
				// A declared type coerces the value on the stack before the
				// bind: an untyped constant converts straight to T (`var r
				// MyRune = 'a'`) instead of first materializing its default.
				coerceTop := func() {
					if vs.Type == nil {
						return
					}
					c.typeExpr(vs.Type)
					c.emit(bytecode.OpCoerceTop, 0, 0, vs.Pos())
				}
				if len(vs.Values) == 1 && len(vs.Names) > 1 {
					c.expr(vs.Values[0])
					c.emit3(bytecode.OpUnpack, len(vs.Names), 0, 0, vs.Pos())
					for i := len(vs.Names) - 1; i >= 0; i-- {
						coerceTop()
						coerce(vs.Names[i], c.bindLocal(vs.Names[i].Name, vs.Names[i].Pos(), isConst))
					}
					break
				}
				for i, name := range vs.Names {
					if len(vs.Values) == 0 {
						c.emit(bytecode.OpNil, 0, 0, name.Pos())
					} else {
						c.expr(vs.Values[i])
						coerceTop()
					}
					coerce(name, c.bindLocal(name.Name, name.Pos(), isConst))
				}
			case token.TYPE:
				// `type S struct{...}` inside a function binds the TypeDef as a
				// local value: S{...} literals, var x S, x.(S) all resolve it.
				c.localTypeDecl(spec.(*ast.TypeSpec))
			case token.IMPORT:
				c.trap(st.Pos(), "import inside function is not valid Go")
			}
		}
	case *ast.AssignStmt:
		c.assign(st)
	case *ast.IncDecStmt:
		op := bytecode.BinAdd
		if st.Tok == token.DEC {
			op = bytecode.BinSub
		}
		one := func() { c.emit(bytecode.OpConst, c.constIdx(int64(1)), 0, st.Pos()) }
		xe := st.X
		for {
			if p, ok := xe.(*ast.ParenExpr); ok {
				xe = p.X
				continue
			}
			break
		}
		switch t := xe.(type) {
		case *ast.Ident:
			c.getRef(t.Name, t.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.setRef(t.Name, t.Pos())
		case *ast.SelectorExpr:
			c.expr(t.X)
			c.emit(bytecode.OpDup, 0, 0, t.Pos())
			c.emit(bytecode.OpSelect, c.nameIdx(t.Sel.Name), 0, t.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetField, c.nameIdx(t.Sel.Name), 0, t.Pos())
		case *ast.IndexExpr:
			c.expr(t.X)
			c.expr(t.Index)
			c.emit(bytecode.OpDup2, 0, 0, t.Pos())
			c.emit(bytecode.OpIndex, 0, 0, t.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetIndex, 0, 0, st.Pos())
		case *ast.StarExpr:
			c.expr(t.X)
			c.emit(bytecode.OpDup, 0, 0, t.Pos())
			c.emit(bytecode.OpDeref, 0, 0, t.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetInd, 0, 0, st.Pos())
		default:
			c.trap(st.Pos(), "++/-- on %T is not supported", st.X)
		}
	case *ast.IfStmt:
		c.ifStmt(st)
	case *ast.ForStmt:
		c.forStmt(st)
	case *ast.RangeStmt:
		c.rangeStmt(st)
	case *ast.SwitchStmt:
		c.switchStmt(st)
	case *ast.ReturnStmt:
		c.returnStmt(st)
	case *ast.BranchStmt:
		c.branchStmt(st)
	case *ast.EmptyStmt:
	case *ast.DeferStmt:
		c.callStmt(st.Call, bytecode.OpDefer, st.Pos())
	case *ast.GoStmt:
		c.callStmt(st.Call, bytecode.OpGo, st.Pos())
	case *ast.SendStmt:
		c.expr(st.Chan)
		c.expr(st.Value)
		c.emit(bytecode.OpSend, 0, 0, st.Pos())
	case *ast.SelectStmt:
		c.selectStmt(st)
	case *ast.LabeledStmt:
		c.labeledStmt(st)
	case *ast.TypeSwitchStmt:
		c.typeSwitchStmt(st)
	case *ast.CaseClause, *ast.CommClause:
		c.trap(st.Pos(), "case clause outside switch")
	default:
		c.trap(s.Pos(), "unsupported statement %T", s)
	}
}

func (c *compiler) bindLocal(name string, pos token.Pos, isConst bool) int {
	if name == "_" {
		c.emit(bytecode.OpPop, 0, 0, pos)
		return -1
	}
	slot := c.fs.declare(name, pos)
	readonly := 0
	if isConst {
		readonly = 1
	}
	c.emit(bytecode.OpNewLocal, slot, readonly, pos)
	return slot
}

// localTypeDecl binds a `type` declaration inside a function body: the
// typedef is a compile-time const pushed as the local's value, so name
// resolution, composite literals, conversions and asserts all see it.
// Local types carry no methods (Go forbids methods on them anyway).
func (c *compiler) localTypeDecl(ts *ast.TypeSpec) {
	td := &runtime.TypeDef{
		Pkg: c.pkg, File: c.file, Name: ts.Name.Name,
		Spec: ts, Anon: ts.Type, Binds: c.binds,
	}
	if ts.TypeParams != nil {
		for _, tp := range ts.TypeParams.List {
			for _, n := range tp.Names {
				td.TParams = append(td.TParams, n.Name)
				td.TConstraints = append(td.TConstraints, tp.Type)
			}
		}
	}
	switch t := ts.Type.(type) {
	case *ast.StructType:
		td.Kind = runtime.KindStruct
		for _, fld := range t.Fields.List {
			tag := ""
			if fld.Tag != nil {
				tag, _ = strconv.Unquote(fld.Tag.Value)
			}
			if len(fld.Names) > 0 {
				for _, n := range fld.Names {
					td.Fields = append(td.Fields, n.Name)
					if tag != "" {
						if td.FTags == nil {
							td.FTags = map[string]string{}
						}
						td.FTags[n.Name] = tag
					}
				}
			} else {
				// anonymous field: embed by type name
				td.EmbedSpecs = append(td.EmbedSpecs, fld.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, embedFieldName(fld.Type))
			}
		}
	case *ast.InterfaceType:
		td.Kind = runtime.KindInterface
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				td.IEmbeds = append(td.IEmbeds, m.Type)
				continue
			}
			for _, n := range m.Names {
				td.MReqs = append(td.MReqs, n.Name)
			}
		}
	case *ast.ArrayType:
		td.Kind = runtime.KindSlice
	case *ast.MapType:
		td.Kind = runtime.KindMap
	case *ast.FuncType:
		td.Kind = runtime.KindFunc
	case *ast.ChanType:
		td.Kind = runtime.KindChan
	case *ast.StarExpr:
		td.Kind = runtime.KindPointer
	default:
		td.Kind = runtime.KindNamedBasic
	}
	if ts.Assign.IsValid() {
		td.Kind = runtime.KindAlias
	}
	c.fs.typeDecls[ts.Name.Name] = true
	c.emit(bytecode.OpConst, c.constIdx(td), 0, ts.Pos())
	slot := c.fs.declare(ts.Name.Name, ts.Pos())
	c.emit(bytecode.OpNewLocal, slot, 0, ts.Pos())
}

// callStmt compiles `defer f(x)` / `go f(x)`: callee and args are
// evaluated immediately; op (OpDefer/OpGo) decides when the call runs.
func (c *compiler) callStmt(call *ast.CallExpr, op bytecode.Op, pos token.Pos) {
	c.calleeExpr(call.Fun)
	for _, a := range call.Args {
		c.expr(a)
	}
	spread := 0
	if call.Ellipsis.IsValid() {
		spread = 1
	}
	c.emit(op, len(call.Args), spread, pos)
}

// assign handles =, :=, and compound ops.
func (c *compiler) assign(st *ast.AssignStmt) {
	isDefine := st.Tok == token.DEFINE
	simple := st.Tok == token.ASSIGN || isDefine

	if !simple {
		if len(st.Lhs) != 1 || len(st.Rhs) != 1 {
			c.trap(st.Pos(), "compound assignment requires single operands")
			return
		}
		op, ok := binOpOf(st.Tok)
		if !ok {
			c.trap(st.Pos(), "unsupported assign op %s", st.Tok)
			return
		}
		target := st.Lhs[0]
		for {
			if p, isParen := target.(*ast.ParenExpr); isParen {
				target = p.X
				continue
			}
			break
		}
		switch lhs := target.(type) {
		case *ast.Ident:
			c.getRef(lhs.Name, lhs.Pos())
			c.expr(st.Rhs[0])
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.setRef(lhs.Name, lhs.Pos())
		case *ast.SelectorExpr:
			c.expr(lhs.X)
			c.emit(bytecode.OpDup, 0, 0, lhs.Pos())
			c.emit(bytecode.OpSelect, c.nameIdx(lhs.Sel.Name), 0, lhs.Pos())
			c.expr(st.Rhs[0])
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetField, c.nameIdx(lhs.Sel.Name), 0, st.Pos())
		case *ast.IndexExpr:
			c.expr(lhs.X)
			c.expr(lhs.Index)
			c.emit(bytecode.OpDup2, 0, 0, lhs.Pos())
			c.emit(bytecode.OpIndex, 0, 0, lhs.Pos())
			c.expr(st.Rhs[0])
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetIndex, 0, 0, st.Pos())
		case *ast.StarExpr:
			c.expr(lhs.X)
			c.emit(bytecode.OpDup, 0, 0, lhs.Pos())
			c.emit(bytecode.OpDeref, 0, 0, lhs.Pos())
			c.expr(st.Rhs[0])
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetInd, 0, 0, st.Pos())
		default:
			c.trap(st.Pos(), "compound assignment on %T is not supported", lhs)
		}
		return
	}

	n := len(st.Lhs)
	if len(st.Rhs) == 1 && n > 1 {
		if n == 2 {
			switch x := st.Rhs[0].(type) {
			case *ast.UnaryExpr:
				// comma-ok receive: v, ok := <-ch
				if x.Op == token.ARROW {
					c.expr(x.X)
					c.emit(bytecode.OpRecvOK, 0, 0, x.Pos())
					c.emit3(bytecode.OpUnpack, 2, 0, 0, st.Pos())
					for i := n - 1; i >= 0; i-- {
						c.storeTarget(st.Lhs[i], isDefine)
					}
					return
				}
			case *ast.IndexExpr:
				// comma-ok map access: v, ok := m[k]
				c.expr(x.X)
				c.expr(x.Index)
				c.emit(bytecode.OpIndexOK, 0, 0, x.Pos())
				c.emit3(bytecode.OpUnpack, 2, 0, 0, st.Pos())
				for i := n - 1; i >= 0; i-- {
					c.storeTarget(st.Lhs[i], isDefine)
				}
				return
			case *ast.TypeAssertExpr:
				// comma-ok assert: v, ok := x.(T)
				c.expr(x.X)
				if x.Type == nil {
					c.trap(x.Pos(), ".(type) outside type switch")
					return
				}
				c.typeExpr(x.Type)
				c.emit(bytecode.OpAssertOK, 0, 0, x.Pos())
				c.emit3(bytecode.OpUnpack, 2, 0, 0, st.Pos())
				for i := n - 1; i >= 0; i-- {
					c.storeTarget(st.Lhs[i], isDefine)
				}
				return
			}
		}
		c.expr(st.Rhs[0])
		c.emit3(bytecode.OpUnpack, n, 0, 0, st.Pos())
	} else {
		for _, r := range st.Rhs {
			c.expr(r)
		}
	}
	// store in reverse order (stack top = last value)
	for i := n - 1; i >= 0; i-- {
		c.storeTarget(st.Lhs[i], isDefine)
	}
}

// storeTarget emits the store for one LHS expression; the value is on stack.
func (c *compiler) storeTarget(lhs ast.Expr, isDefine bool) {
	switch t := lhs.(type) {
	case *ast.Ident:
		if t.Name == "_" {
			c.emit(bytecode.OpPop, 0, 0, t.Pos())
			return
		}
		if isDefine && !c.fs.inCurrentBlock(t.Name) {
			slot := c.fs.declare(t.Name, t.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, t.Pos())
		} else {
			c.setRef(t.Name, t.Pos())
		}
	case *ast.SelectorExpr:
		// stack: [.. v]; eval base -> [v base]; swap -> [base v];
		// OpSetField pops v then base.
		c.expr(t.X)
		c.emit(bytecode.OpSwap, 0, 0, t.Pos())
		c.emit(bytecode.OpSetField, c.nameIdx(t.Sel.Name), 0, t.Pos())
	case *ast.IndexExpr:
		// stack: [v]; eval base,idx -> [v base idx]; rot3 -> [base idx v]
		c.expr(t.X)
		c.expr(t.Index)
		c.emit(bytecode.OpRot3, 0, 0, t.Pos())
		c.emit(bytecode.OpSetIndex, 0, 0, t.Pos())
	case *ast.StarExpr:
		c.expr(t.X)
		c.emit(bytecode.OpSwap, 0, 0, t.Pos())
		c.emit(bytecode.OpSetInd, 0, 0, t.Pos())
	default:
		c.trap(lhs.Pos(), "unsupported assignment target %T", lhs)
	}
}

func (c *compiler) ifStmt(st *ast.IfStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	c.expr(st.Cond)
	jElse := c.emit(bytecode.OpJumpFalse, 0, 0, st.Cond.Pos())
	c.stmt(st.Body)
	jEnd := c.emit(bytecode.OpJump, 0, 0, st.Pos())
	c.patchA(jElse, len(c.ch.Code))
	if st.Else != nil {
		c.stmt(st.Else)
	}
	c.patchA(jEnd, len(c.ch.Code))
	c.fs.popBlock()
}

func (c *compiler) forStmt(st *ast.ForStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	lc := &ctrlCtx{isLoop: true, continueIP: -1, labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, lc)
	condIP := len(c.ch.Code)
	var jEnd int
	if st.Cond != nil {
		c.expr(st.Cond)
		jEnd = c.emit(bytecode.OpJumpFalse, 0, 0, st.Cond.Pos())
	}
	// Go 1.22+ iteration semantics: names the init statement declares get a
	// fresh binding per iteration — rebind them inside the body so closures
	// (and goroutines) capture distinct cells, then copy the body's
	// (possibly mutated) value back so post and the next cond see it.
	shadows := loopInitNames(st.Init)
	c.fs.pushBlock()
	var shadowPairs [][2]int
	for _, name := range shadows {
		outer, _ := c.fs.lookupLocal(name.Name)
		slot := c.fs.declare(name.Name, name.Pos())
		shadowPairs = append(shadowPairs, [2]int{outer, slot})
		c.emit(bytecode.OpLocal, outer, 0, name.Pos())
		c.emit(bytecode.OpNewLocal, slot, 0, name.Pos())
	}
	for _, bs := range st.Body.List {
		c.stmt(bs)
	}
	c.fs.popBlock()
	lc.continueIP = len(c.ch.Code)
	for _, pr := range shadowPairs {
		c.emit(bytecode.OpLocal, pr[1], 0, st.Init.Pos())
		c.emit(bytecode.OpSetLocal, pr[0], 0, st.Init.Pos())
	}
	if st.Post != nil {
		c.stmt(st.Post)
	}
	// continues inside the body were emitted before post's position existed
	for _, ci := range lc.continues {
		c.patchA(ci, lc.continueIP)
	}
	c.emit(bytecode.OpJump, condIP, 0, st.Pos())
	end := len(c.ch.Code)
	if st.Cond != nil {
		c.patchA(jEnd, end)
	}
	for _, b := range lc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// loopInitNames lists the names a 3-clause for's init statement declares
// (`i := 0`): only :=-style inits get per-iteration bindings.
func loopInitNames(s ast.Stmt) []*ast.Ident {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE {
		return nil
	}
	var out []*ast.Ident
	for _, lhs := range as.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
			out = append(out, id)
		}
	}
	return out
}

func (c *compiler) rangeStmt(st *ast.RangeStmt) {
	c.fs.pushBlock()
	c.expr(st.X)
	c.emit(bytecode.OpIter, 0, 0, st.X.Pos())
	itSlot := c.fs.declare("$it", token.NoPos)
	c.emit(bytecode.OpNewLocal, itSlot, 0, st.X.Pos())

	nvars := 0
	if st.Key != nil {
		nvars++
	}
	if st.Value != nil {
		nvars++
	}
	lc := &ctrlCtx{isLoop: true, labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, lc)
	topIP := len(c.ch.Code)
	nextI := c.emit3(bytecode.OpRangeNext, 0, itSlot, nvars, st.Pos())
	lc.continueIP = topIP

	// OpRangeNext pushes nvars values (key, val); bind in reverse.
	if st.Value != nil {
		c.bindRangeVar(st.Value, st.Tok == token.DEFINE)
	}
	if st.Key != nil {
		c.bindRangeVar(st.Key, st.Tok == token.DEFINE)
	}
	c.stmt(st.Body)
	c.emit(bytecode.OpJump, topIP, 0, st.Pos())
	end := len(c.ch.Code)
	c.patchA(nextI, end)
	for _, b := range lc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

func (c *compiler) bindRangeVar(e ast.Expr, define bool) {
	if id, ok := e.(*ast.Ident); ok && id.Name == "_" {
		c.emit(bytecode.OpPop, 0, 0, e.Pos())
		return
	}
	c.storeTarget(e, define)
}

func (c *compiler) switchStmt(st *ast.SwitchStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	tagSlot := -1
	if st.Tag != nil {
		c.expr(st.Tag)
		tagSlot = c.fs.declare("$tag", token.NoPos)
		c.emit(bytecode.OpNewLocal, tagSlot, 0, st.Tag.Pos())
	}
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	var defaultBody []ast.Stmt
	var jumpOuts []int
	var pendingFalls []int // fallthrough sites in the previous clause body
	for _, s := range st.Body.List {
		clause := s.(*ast.CaseClause)
		if clause.List == nil {
			defaultBody = clause.Body
			continue
		}
		// tests: tag == e (or truthy e for tag-less switch); JumpTrue -> body
		bodyJumps := []int{}
		for _, e := range clause.List {
			if tagSlot >= 0 {
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.expr(e)
				c.emit(bytecode.OpBinary, int(bytecode.BinEql), 0, e.Pos())
			} else {
				c.expr(e)
			}
			bodyJumps = append(bodyJumps, c.emit(bytecode.OpJumpTrue, 0, 0, e.Pos()))
		}
		// no test matched: continue to next clause's tests (emitted after
		// this body)
		jNext := c.emit(bytecode.OpJump, 0, 0, clause.Pos())
		bodyStart := len(c.ch.Code)
		for _, bj := range bodyJumps {
			c.patchA(bj, bodyStart)
		}
		for _, fi := range pendingFalls {
			c.patchA(fi, bodyStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		var falls []int
		c.falls = &falls
		for _, bs := range clause.Body {
			c.stmt(bs)
		}
		c.falls = nil
		c.fs.popBlock()
		pendingFalls = falls
		jumpOuts = append(jumpOuts, c.emit(bytecode.OpJump, 0, 0, clause.Pos()))
		c.patchA(jNext, len(c.ch.Code))
	}
	if defaultBody != nil {
		defStart := len(c.ch.Code)
		for _, fi := range pendingFalls {
			c.patchA(fi, defStart)
		}
		pendingFalls = nil
		for _, bs := range defaultBody {
			c.stmt(bs)
		}
	}
	if len(pendingFalls) > 0 {
		ti := c.trap(st.Pos(), "fallthrough out of the final case clause")
		for _, fi := range pendingFalls {
			c.patchA(fi, ti)
		}
	}
	end := len(c.ch.Code)
	for _, j := range jumpOuts {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// selectStmt compiles a real blocking select. The Go spec evaluates every
// case's channel operand (and a send case's value) exactly once, in source
// order, on entry — so operands land in temp slots first, then OpSelArm
// builds a select descriptor per case and OpSelWait blocks on
// reflect.Select (random ready choice, blocking without a default). The
// wait dispatches through a jump table of OpJumps laid down immediately
// after it — case i's body is at table[i], the default at table[n].
func (c *compiler) selectStmt(st *ast.SelectStmt) {
	c.fs.pushBlock()
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	type selCase struct {
		body     []ast.Stmt
		pos      token.Pos
		chanSlot int
		valSlot  int // send cases only
		nrecv    int // receive arity: 0, 1, or 2
		lhs      []ast.Expr
		define   bool
		send     bool
	}
	var cases []selCase
	var defaultBody []ast.Stmt
	hasDefault := false
	tmp := 0
	for _, s := range st.Body.List {
		clause := s.(*ast.CommClause)
		if clause.Comm == nil {
			// a `default:` with an empty body still counts as a default —
			// without the arm the select blocks instead of falling through
			hasDefault = true
			defaultBody = clause.Body
			continue
		}
		sc := selCase{body: clause.Body, pos: clause.Pos(), chanSlot: -1, valSlot: -1}
		switch comm := clause.Comm.(type) {
		case *ast.SendStmt:
			sc.send = true
			sc.chanSlot = c.fs.declare(fmt.Sprintf("$sel%d", tmp), token.NoPos)
			tmp++
			sc.valSlot = c.fs.declare(fmt.Sprintf("$sel%d", tmp), token.NoPos)
			tmp++
			c.expr(comm.Chan)
			c.emit(bytecode.OpSetLocal, sc.chanSlot, 0, comm.Chan.Pos())
			c.expr(comm.Value)
			c.emit(bytecode.OpSetLocal, sc.valSlot, 0, comm.Value.Pos())
		default:
			recv, lhs, define := selectRecv(clause.Comm)
			if recv == nil {
				c.trap(clause.Comm.Pos(), "unsupported select case %T", clause.Comm)
				continue
			}
			sc.chanSlot = c.fs.declare(fmt.Sprintf("$sel%d", tmp), token.NoPos)
			tmp++
			c.expr(recv.X)
			c.emit(bytecode.OpSetLocal, sc.chanSlot, 0, recv.X.Pos())
			sc.nrecv = len(lhs)
			sc.lhs = lhs
			sc.define = define
		}
		cases = append(cases, sc)
	}

	// arms: one OpSelArm per case, pushed in source order for OpSelWait
	for _, sc := range cases {
		if sc.send {
			c.emit(bytecode.OpLocal, sc.chanSlot, 0, sc.pos)
			c.emit(bytecode.OpLocal, sc.valSlot, 0, sc.pos)
			c.emit(bytecode.OpSelArm, 0, 1, sc.pos)
		} else {
			c.emit(bytecode.OpLocal, sc.chanSlot, 0, sc.pos)
			c.emit(bytecode.OpSelArm, sc.nrecv, 0, sc.pos)
		}
	}
	bDefault := 0
	if hasDefault {
		bDefault = 1
	}
	c.emit(bytecode.OpSelWait, len(cases), bDefault, st.Pos())
	// jump table OpSelWait dispatches into: one OpJump per case, then the
	// default's OpJump as the last slot
	jmps := make([]int, 0, len(cases)+1)
	for _, sc := range cases {
		jmps = append(jmps, c.emit(bytecode.OpJump, 0, 0, sc.pos))
	}
	if hasDefault {
		jmps = append(jmps, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
	}

	var exits []int
	for i, sc := range cases {
		c.patchA(jmps[i], len(c.ch.Code))
		c.fs.pushBlock()
		if !sc.send {
			c.bindRecv(sc.lhs, sc.define)
		}
		for _, bs := range sc.body {
			c.stmt(bs)
		}
		c.fs.popBlock()
		exits = append(exits, c.emit(bytecode.OpJump, 0, 0, sc.pos))
	}
	if hasDefault {
		c.patchA(jmps[len(cases)], len(c.ch.Code))
		for _, bs := range defaultBody {
			c.stmt(bs)
		}
	}
	end := len(c.ch.Code)
	for _, j := range exits {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// selectRecv extracts a receive case from a CommClause's comm statement:
// `case <-ch`, `case v := <-ch`, `case v, ok := <-ch`, or assignments.
func selectRecv(comm ast.Stmt) (recv *ast.UnaryExpr, lhs []ast.Expr, define bool) {
	arrow := func(e ast.Expr) *ast.UnaryExpr {
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
			return u
		}
		return nil
	}
	switch s := comm.(type) {
	case *ast.ExprStmt:
		if u := arrow(s.X); u != nil {
			return u, nil, false
		}
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			if u := arrow(s.Rhs[0]); u != nil {
				return u, s.Lhs, s.Tok == token.DEFINE
			}
		}
	}
	return nil, nil, false
}

// bindRecv binds a select receive payload (already on the stack) to the
// case's LHS: a Tuple for two binds, a plain value for one, discarded else.
func (c *compiler) bindRecv(lhs []ast.Expr, define bool) {
	switch len(lhs) {
	case 0:
		c.emit(bytecode.OpPop, 0, 0, 0)
	case 1:
		c.storeTarget(lhs[0], define)
	default:
		c.emit3(bytecode.OpUnpack, len(lhs), 0, 0, 0)
		for i := len(lhs) - 1; i >= 0; i-- {
			c.storeTarget(lhs[i], define)
		}
	}
}

func (c *compiler) returnStmt(st *ast.ReturnStmt) {
	if len(st.Results) == 0 {
		// bare return: coerce named slots to their declared types so
		// `return` under `func f() (r *T)` yields a typed nil.
		for i, slot := range c.ch.NamedSlots {
			if i < len(c.results) && c.results[i] != nil {
				c.emitTypeCoerce(slot, c.results[i], st.Pos())
			}
		}
		c.emit(bytecode.OpReturn, -1, 0, st.Pos()) // -1: use named result slots
		return
	}
	if len(st.Results) == 1 && len(c.results) > 1 {
		// `return pair()`: one expr produces N results — push every
		// declared result type and coerce the tuple element-wise, so an
		// `any` slot still boxes a typed nil into an IfaceNil.
		c.expr(st.Results[0])
		for _, rt := range c.results {
			c.typeExpr(rt)
		}
		c.emit(bytecode.OpCoerceN, len(c.results), 0, st.Results[0].Pos())
	} else {
		for i, r := range st.Results {
			c.expr(r)
			// `return e` coerces e to the declared result type — `return nil`
			// under a *T result yields a typed nil, under any an IfaceNil.
			if i < len(c.results) && c.results[i] != nil {
				c.typeExpr(c.results[i])
				c.emit(bytecode.OpCoerceTop, 0, 0, r.Pos())
			}
		}
	}
	c.emit(bytecode.OpReturn, len(st.Results), 0, st.Pos())
}

func (c *compiler) branchStmt(st *ast.BranchStmt) {
	if st.Label != nil {
		switch st.Tok {
		case token.BREAK:
			for i := len(c.ctrl) - 1; i >= 0; i-- {
				cc := c.ctrl[i]
				for _, name := range cc.labels {
					if name == st.Label.Name {
						cc.breaks = append(cc.breaks, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
						return
					}
				}
			}
			c.trap(st.Pos(), "break label %s is not defined", st.Label.Name)
		case token.CONTINUE:
			for i := len(c.ctrl) - 1; i >= 0; i-- {
				cc := c.ctrl[i]
				if !cc.isLoop {
					continue
				}
				for _, name := range cc.labels {
					if name == st.Label.Name {
						if cc.continueIP >= 0 {
							c.emit(bytecode.OpJump, cc.continueIP, 0, st.Pos())
						} else {
							cc.continues = append(cc.continues, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
						}
						return
					}
				}
			}
			c.trap(st.Pos(), "continue label %s is not defined", st.Label.Name)
		case token.GOTO:
			gb, gv := c.fs.scopeSnapshot()
			if li, ok := c.labels[st.Label.Name]; ok {
				if why := gotoViolation(li, gb, gv); why != "" {
					c.trap(st.Pos(), "%s", why)
					return
				}
				c.emit(bytecode.OpJump, li.ip, 0, st.Pos())
				return
			}
			c.pendingGotos = append(c.pendingGotos, pendingGoto{
				ins:    c.emit(bytecode.OpJump, 0, 0, st.Pos()),
				name:   st.Label.Name,
				pos:    st.Pos(),
				blocks: gb,
				vars:   gv,
			})
		case token.FALLTHROUGH:
			c.trap(st.Pos(), "fallthrough cannot have a label")
		}
		return
	}
	switch st.Tok {
	case token.BREAK:
		if len(c.ctrl) > 0 {
			cc := c.ctrl[len(c.ctrl)-1]
			cc.breaks = append(cc.breaks, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			return
		}
		c.trap(st.Pos(), "break outside loop/switch")
	case token.CONTINUE:
		for i := len(c.ctrl) - 1; i >= 0; i-- {
			cc := c.ctrl[i]
			if !cc.isLoop {
				continue
			}
			if cc.continueIP >= 0 {
				c.emit(bytecode.OpJump, cc.continueIP, 0, st.Pos())
			} else {
				cc.continues = append(cc.continues, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			}
			return
		}
		c.trap(st.Pos(), "continue outside loop")
	case token.FALLTHROUGH:
		if c.falls != nil {
			*c.falls = append(*c.falls, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			return
		}
		c.trap(st.Pos(), "fallthrough outside switch case")
	}
}

// labeledStmt registers a label and compiles its statement. A label
// directly wrapping a control construct (for/range/switch/select/type
// switch) is claimed by that construct so `break L`/`continue L` work.
func (c *compiler) labeledStmt(st *ast.LabeledStmt) {
	lb, lv := c.fs.scopeSnapshot()
	li := &labelInfo{name: st.Label.Name, ip: len(c.ch.Code), blocks: lb, vars: lv}
	if _, dup := c.labels[st.Label.Name]; dup {
		c.trap(st.Pos(), "label %s redeclared", st.Label.Name)
	}
	c.labels[st.Label.Name] = li
	switch st.Stmt.(type) {
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.SelectStmt, *ast.TypeSwitchStmt:
		c.pendingLabels = append(c.pendingLabels, li)
		c.stmt(st.Stmt)
		c.pendingLabels = nil
	default:
		c.stmt(st.Stmt)
	}
}

// takeLabels hands any pending labels to a newly created ctrlCtx and
// returns their names.
func (c *compiler) takeLabels() []string {
	if len(c.pendingLabels) == 0 {
		return nil
	}
	names := make([]string, len(c.pendingLabels))
	for i, li := range c.pendingLabels {
		names[i] = li.name
	}
	c.pendingLabels = nil
	return names
}

// resolveGotos patches forward gotos to their labels; a goto with no
// matching label in the function becomes a run-time trap.
func (c *compiler) resolveGotos() {
	for _, pg := range c.pendingGotos {
		if li, ok := c.labels[pg.name]; ok {
			if why := gotoViolation(li, pg.blocks, pg.vars); why != "" {
				ti := c.trap(pg.pos, "%s", why)
				c.patchA(pg.ins, ti)
				continue
			}
			c.patchA(pg.ins, li.ip)
			continue
		}
		ti := c.trap(pg.pos, "goto %s: label not defined", pg.name)
		c.patchA(pg.ins, ti)
	}
	c.pendingGotos = nil
}

// gotoViolation reports why `goto L` is illegal given the scope snapshots
// taken at the goto and at the label. Go forbids jumping INTO a block
// (a label's open blocks must all be open at the goto) and jumping OVER a
// variable declaration (every var visible at the label must already be
// visible at the goto). Returns "" when the jump is legal.
func gotoViolation(li *labelInfo, gotoBlocks map[int]bool, gotoVars map[string]token.Pos) string {
	for id := range li.blocks {
		if !gotoBlocks[id] {
			return fmt.Sprintf("goto %s jumps into a block", li.name)
		}
	}
	for n, lp := range li.vars {
		// the declaration the name resolves to at the label must be the
		// same one visible at the goto — a different decl (or none) means
		// the jump skips that variable's declaration
		if gp, ok := gotoVars[n]; !ok || gp != lp {
			return fmt.Sprintf("goto %s jumps over declaration of %s", li.name, n)
		}
	}
	return ""
}

// typeSwitchStmt compiles `switch v := x.(type) { case T: ... }`. The
// subject is evaluated once into a hidden slot; each case emits OpAssertOK
// keeping the asserted value on stack for the case body to bind (narrowed
// v) or discard.
func (c *compiler) typeSwitchStmt(st *ast.TypeSwitchStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	var subj ast.Expr
	varName := ""
	switch a := st.Assign.(type) {
	case *ast.ExprStmt:
		if ta, ok := a.X.(*ast.TypeAssertExpr); ok {
			subj = ta.X
		}
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			if ta, ok := a.Rhs[0].(*ast.TypeAssertExpr); ok {
				subj = ta.X
			}
		}
		if len(a.Lhs) > 0 {
			if id, ok := a.Lhs[0].(*ast.Ident); ok {
				varName = id.Name
			}
		}
	}
	if subj == nil {
		c.trap(st.Pos(), "type switch without a type assertion")
		c.fs.popBlock()
		return
	}
	c.expr(subj)
	tagSlot := c.fs.declare("$tsubj", token.NoPos)
	c.emit(bytecode.OpNewLocal, tagSlot, 0, subj.Pos())
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	var defaultBody []ast.Stmt
	var jumpOuts []int
	var pendingFalls []int
	for _, s := range st.Body.List {
		clause := s.(*ast.CaseClause)
		if clause.List == nil {
			defaultBody = clause.Body
			continue
		}
		bodyJumps := []int{}
		// each test leaves one value on the stack for the body to bind
		// (the assert result, or the subject itself for `case nil:`);
		// JumpTrue pops the flag, the false path pops the leftover value.
		for _, e := range clause.List {
			if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.emit(bytecode.OpNil, 0, 0, e.Pos())
				c.emit(bytecode.OpBinary, int(bytecode.BinEql), 0, e.Pos())
			} else {
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.typeExpr(e)
				c.emit(bytecode.OpAssertOK, 0, 0, e.Pos())
				c.emit3(bytecode.OpUnpack, 2, 0, 0, e.Pos())
			}
			bodyJumps = append(bodyJumps, c.emit(bytecode.OpJumpTrue, 0, 0, e.Pos()))
			c.emit(bytecode.OpPop, 0, 0, e.Pos())
		}
		jNext := c.emit(bytecode.OpJump, 0, 0, clause.Pos())
		bodyStart := len(c.ch.Code)
		for _, bj := range bodyJumps {
			c.patchA(bj, bodyStart)
		}
		for _, fi := range pendingFalls {
			c.patchA(fi, bodyStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		if varName != "" && varName != "_" {
			slot := c.fs.declare(varName, clause.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, clause.Pos())
		} else {
			c.emit(bytecode.OpPop, 0, 0, clause.Pos())
		}
		var falls []int
		c.falls = &falls
		for _, bs := range clause.Body {
			c.stmt(bs)
		}
		c.falls = nil
		c.fs.popBlock()
		pendingFalls = falls
		jumpOuts = append(jumpOuts, c.emit(bytecode.OpJump, 0, 0, clause.Pos()))
		c.patchA(jNext, len(c.ch.Code))
	}
	if defaultBody != nil {
		defStart := len(c.ch.Code)
		for _, fi := range pendingFalls {
			c.patchA(fi, defStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		if varName != "" && varName != "_" {
			// default binds the un-narrowed subject value
			slot := c.fs.declare(varName, st.Pos())
			c.emit(bytecode.OpLocal, tagSlot, 0, st.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, st.Pos())
		}
		for _, bs := range defaultBody {
			c.stmt(bs)
		}
		c.fs.popBlock()
	}
	if len(pendingFalls) > 0 {
		ti := c.trap(st.Pos(), "fallthrough out of the final case clause")
		for _, fi := range pendingFalls {
			c.patchA(fi, ti)
		}
	}
	end := len(c.ch.Code)
	for _, j := range jumpOuts {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// ---- expressions ----

func (c *compiler) expr(e ast.Expr) {
	switch x := e.(type) {
	case *ast.BasicLit:
		v, err := literalValue(x)
		if err != nil {
			c.trap(x.Pos(), "bad literal: %s", err)
			return
		}
		c.emit(bytecode.OpConst, c.constIdx(v), 0, x.Pos())
	case *ast.Ident:
		switch x.Name {
		case "nil":
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
		case "true":
			c.emit(bytecode.OpConst, c.constIdx(true), 0, x.Pos())
		case "false":
			c.emit(bytecode.OpConst, c.constIdx(false), 0, x.Pos())
		default:
			c.getRef(x.Name, x.Pos())
		}
	case *ast.SelectorExpr:
		c.expr(x.X)
		c.emit(bytecode.OpSelect, c.nameIdx(x.Sel.Name), 0, x.Pos())
	case *ast.IndexExpr:
		// OpInstantiate doubles as indexing: non-generic bases fall back to
		// an index lookup, so `a[i]` and `F[T]` share one encoding. A type
		// form arg (F[[]int]) compiles to a typedef; anything else stays a
		// value expr (ident type args resolve through getRef too).
		c.expr(x.X)
		if isTypeForm(x.Index) {
			c.typeExpr(x.Index)
		} else {
			c.expr(x.Index)
		}
		c.emit(bytecode.OpInstantiate, 1, 0, x.Pos())
	case *ast.SliceExpr:
		c.expr(x.X)
		if x.Low != nil {
			c.expr(x.Low)
		} else {
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
		}
		if x.High != nil {
			c.expr(x.High)
		} else {
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
		}
		if x.Slice3 {
			if x.Max != nil {
				c.expr(x.Max)
			} else {
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
			}
			c.emit(bytecode.OpSlice, 0, 1, x.Pos())
			return
		}
		c.emit(bytecode.OpSlice, 0, 0, x.Pos())
	case *ast.StarExpr:
		c.expr(x.X)
		c.emit(bytecode.OpDeref, 0, 0, x.Pos())
	case *ast.ParenExpr:
		c.expr(x.X)
	case *ast.UnaryExpr:
		c.unary(x)
	case *ast.BinaryExpr:
		c.binary(x)
	case *ast.CallExpr:
		c.call(x)
	case *ast.CompositeLit:
		c.compositeLit(x)
	case *ast.FuncLit:
		c.funcLit(x)
	case *ast.TypeAssertExpr:
		c.expr(x.X)
		if x.Type == nil {
			c.trap(x.Pos(), ".(type) outside type switch")
			return
		}
		c.staticTyp(x.X)
		c.typeExpr(x.Type)
		c.emit(bytecode.OpAssert, 0, 1, x.Pos())
	case *ast.IndexListExpr:
		// multi-index is only legal as generic instantiation F[T, U]
		c.expr(x.X)
		for _, i := range x.Indices {
			c.typeExpr(i)
		}
		c.emit(bytecode.OpInstantiate, len(x.Indices), 0, x.Pos())
	case *ast.Ellipsis:
		c.trap(x.Pos(), "bare ellipsis is not supported")
	case *ast.KeyValueExpr:
		c.trap(x.Pos(), "key:value outside composite literal")
	default:
		c.trap(e.Pos(), "unsupported expression %T", e)
	}
}

// staticTyp pushes the declared type the operand of an assertion is
// bound under — Go's "main.I" in "interface conversion: main.I is main.T,
// not io.Writer". Only a bare identifier carries a static type into the
// runtime; other expressions (or untyped cells) yield NIL and the panic
// message falls back to "interface {}".
func (c *compiler) staticTyp(e ast.Expr) {
	id, ok := e.(*ast.Ident)
	if !ok {
		c.emit(bytecode.OpNil, 0, 0, e.Pos())
		return
	}
	isUp, idx, found := c.fs.find(id.Name)
	switch {
	case !found:
		if _, bound := c.binds[id.Name]; bound {
			c.emit(bytecode.OpNil, 0, 0, id.Pos())
			return
		}
		c.emit(bytecode.OpGlobalTyp, c.nameIdx(id.Name), 0, id.Pos())
	case !isUp:
		c.emit(bytecode.OpLocalTyp, idx, 0, id.Pos())
	default:
		c.emit(bytecode.OpUpvalTyp, idx, 0, id.Pos())
	}
}

func (c *compiler) unary(x *ast.UnaryExpr) {
	switch x.Op {
	case token.AND: // &x
		switch t := x.X.(type) {
		case *ast.Ident:
			c.refRef(t.Name, t.Pos())
		case *ast.CompositeLit:
			c.compositeLit(t)
			c.emit(bytecode.OpBox, 0, 0, x.Pos())
		case *ast.SelectorExpr:
			c.expr(t.X)
			c.emit(bytecode.OpFieldRef, c.nameIdx(t.Sel.Name), 0, t.Pos())
		case *ast.IndexExpr:
			c.expr(t.X)
			c.expr(t.Index)
			c.emit(bytecode.OpIndexRef, 0, 0, t.Pos())
		default:
			c.trap(x.Pos(), "address-of %T is not supported", x.X)
		}
	case token.ADD:
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnPos), 0, x.Pos())
	case token.SUB:
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnNeg), 0, x.Pos())
	case token.NOT:
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnNot), 0, x.Pos())
	case token.XOR:
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnXor), 0, x.Pos())
	case token.ARROW:
		c.expr(x.X)
		c.emit(bytecode.OpRecv, 0, 0, x.Pos())
	default:
		c.trap(x.Pos(), "unsupported unary %s", x.Op)
	}
}

func (c *compiler) binary(x *ast.BinaryExpr) {
	switch x.Op {
	case token.LAND:
		c.expr(x.X)
		c.emit(bytecode.OpDup, 0, 0, x.Pos())
		j := c.emit(bytecode.OpJumpFalse, 0, 0, x.Pos())
		c.emit(bytecode.OpPop, 0, 0, x.Pos())
		c.expr(x.Y)
		c.patchA(j, len(c.ch.Code))
	case token.LOR:
		c.expr(x.X)
		c.emit(bytecode.OpDup, 0, 0, x.Pos())
		j := c.emit(bytecode.OpJumpTrue, 0, 0, x.Pos())
		c.emit(bytecode.OpPop, 0, 0, x.Pos())
		c.expr(x.Y)
		c.patchA(j, len(c.ch.Code))
	default:
		if c.foldConst(x) {
			return
		}
		op, ok := binOpOf(x.Op)
		if !ok {
			c.trap(x.Pos(), "unsupported binary %s", x.Op)
			return
		}
		c.expr(x.X)
		c.expr(x.Y)
		c.emit(bytecode.OpBinary, int(op), 0, x.Pos())
	}
}

// foldConst evaluates a constant-only binary expression in go/constant's
// arbitrary-precision domain and emits one OpConst. Go computes constant
// arithmetic exactly — 1<<100>>50 is 2^50 — where evaluating the same
// expression at runtime in int64 would silently wrap to 0. An expression
// whose constant result minigo cannot represent traps at compile time
// (the same operation Go rejects). Returns false when any operand is not
// a constant, so the caller emits the usual binary ops.
func (c *compiler) foldConst(x *ast.BinaryExpr) bool {
	// division by zero is a compile error in Go when the whole
	// expression is constant — trap it like the compiler. With a
	// non-constant dividend the op divides at runtime instead: floats
	// yield ±Inf or NaN and ints panic (a recoverable *Panic, not a
	// compile-time *Trap).
	if x.Op == token.QUO || x.Op == token.REM {
		if rv, ok := constValue(x.Y); ok && (rv.Kind() == constant.Int || rv.Kind() == constant.Float) && constant.Sign(rv) == 0 {
			if _, ok := constValue(x.X); ok {
				c.trap(x.Pos(), "constant division by zero")
				return true
			}
		}
	}
	cv, ok := constValue(x)
	if !ok {
		return false
	}
	var v any
	switch cv.Kind() {
	case constant.Bool:
		v = constant.BoolVal(cv)
	case constant.String:
		v = constant.StringVal(cv)
	case constant.Float:
		f, _ := constant.Float64Val(cv)
		if math.IsInf(f, 0) {
			// an overflowing constant float is still a valid untyped
			// constant (Go compiles `const F = 1e500`); failing is the
			// materialization boundary's job.
			v = &runtime.UConst{V: cv}
		} else {
			v = f
		}
	case constant.Complex:
		v = &runtime.UConst{V: cv}
	case constant.Int:
		if hasCharLit(x.X) || hasCharLit(x.Y) {
			// a rune-kind constant expr defaults to rune, not int:
			// `var y = 'a' + 1` types y as int32 in Go.
			v = &runtime.UConst{V: cv, Rune: true}
		} else if i, ok := constant.Int64Val(cv); ok {
			v = i
		} else if u, ok := constant.Uint64Val(cv); ok {
			if u == 1<<63 {
				v = int64(u)
			} else {
				// same boxing as literalValue: formatting reads the box.
				v = &runtime.GoValue{V: u}
			}
		} else {
			// beyond uint64: lazy untyped constant — `const B = 1<<100`
			// is legal and only materializing it can overflow.
			v = &runtime.UConst{V: cv}
		}
	default:
		// Unknown kind: the operation is undefined for these operand
		// types ("a" + 1), which Go rejects at compile time.
		c.trap(x.Pos(), "invalid constant expression: %s %s %s", x.X, x.Op, x.Y)
		return true
	}
	c.emit(bytecode.OpConst, c.constIdx(v), 0, x.Pos())
	return true
}

// hasCharLit reports whether the expression contains a rune literal —
// constant folding uses it to keep the rune kind of the result.
func hasCharLit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.CHAR {
			found = true
		}
		return !found
	})
	return found
}

// constValue evaluates an expression made only of literal constants and
// returns its go/constant value. ok is false when any operand is not a
// constant (identifiers, calls, index expressions), letting the caller
// fall back to normal codegen.
func constValue(e ast.Expr) (cv constant.Value, ok bool) {
	defer func() {
		// go/constant panics on unsound inputs (shift by a negative
		// count, integer divide by zero); those keep runtime semantics.
		if recover() != nil {
			cv, ok = nil, false
		}
	}()
	switch x := e.(type) {
	case *ast.ParenExpr:
		return constValue(x.X)
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT, token.FLOAT, token.CHAR, token.STRING:
			return constant.MakeFromLiteral(x.Value, x.Kind, 0), true
		}
	case *ast.UnaryExpr:
		xv, ok := constValue(x.X)
		if !ok {
			return nil, false
		}
		switch x.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return constant.UnaryOp(x.Op, xv, 0), true
		}
	case *ast.BinaryExpr:
		lv, ok := constValue(x.X)
		if !ok {
			return nil, false
		}
		rv, ok := constValue(x.Y)
		if !ok {
			return nil, false
		}
		switch x.Op {
		case token.SHL, token.SHR:
			s, ok := constant.Uint64Val(rv)
			if !ok {
				return nil, false
			}
			return constant.Shift(lv, x.Op, uint(s)), true
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return constant.MakeBool(constant.Compare(lv, x.Op, rv)), true
		case token.LAND, token.LOR:
			// keep short-circuit semantics at runtime; && and || on
			// constants are rare enough not to fold here.
			return nil, false
		case token.QUO:
			// a quotient of two integer constants is an integer constant
			// (7/2 is 3): QUO computes the exact rational, QUO_ASSIGN is
			// go/constant's spelling for truncating integer division.
			if lv.Kind() == constant.Int && rv.Kind() == constant.Int {
				return constant.BinaryOp(lv, token.QUO_ASSIGN, rv), true
			}
			return constant.BinaryOp(lv, x.Op, rv), true
		default:
			return constant.BinaryOp(lv, x.Op, rv), true
		}
	}
	return nil, false
}

func binOpOf(tok token.Token) (bytecode.BinOp, bool) {
	switch tok {
	case token.ADD:
		return bytecode.BinAdd, true
	case token.SUB:
		return bytecode.BinSub, true
	case token.MUL:
		return bytecode.BinMul, true
	case token.QUO:
		return bytecode.BinQuo, true
	case token.REM:
		return bytecode.BinRem, true
	case token.AND:
		return bytecode.BinAnd, true
	case token.OR:
		return bytecode.BinOr, true
	case token.XOR:
		return bytecode.BinXor, true
	case token.AND_NOT:
		return bytecode.BinAndNot, true
	case token.SHL:
		return bytecode.BinShl, true
	case token.SHR:
		return bytecode.BinShr, true
	case token.LAND:
		return bytecode.BinLAnd, true
	case token.LOR:
		return bytecode.BinLOr, true
	case token.EQL:
		return bytecode.BinEql, true
	case token.NEQ:
		return bytecode.BinNeq, true
	case token.LSS:
		return bytecode.BinLss, true
	case token.LEQ:
		return bytecode.BinLeq, true
	case token.GTR:
		return bytecode.BinGtr, true
	case token.GEQ:
		return bytecode.BinGeq, true
	case token.ADD_ASSIGN:
		return bytecode.BinAdd, true
	case token.SUB_ASSIGN:
		return bytecode.BinSub, true
	case token.MUL_ASSIGN:
		return bytecode.BinMul, true
	case token.QUO_ASSIGN:
		return bytecode.BinQuo, true
	case token.REM_ASSIGN:
		return bytecode.BinRem, true
	case token.AND_ASSIGN:
		return bytecode.BinAnd, true
	case token.OR_ASSIGN:
		return bytecode.BinOr, true
	case token.XOR_ASSIGN:
		return bytecode.BinXor, true
	case token.SHL_ASSIGN:
		return bytecode.BinShl, true
	case token.SHR_ASSIGN:
		return bytecode.BinShr, true
	case token.AND_NOT_ASSIGN:
		return bytecode.BinAndNot, true
	}
	return 0, false
}

func (c *compiler) call(x *ast.CallExpr) {
	// F[...] is explicit generic instantiation — unwrap to reach a
	// special form like define.Convert[Dst, Src](...); the type args
	// carry no meaning for the quoted handler.
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
	if sel, ok := fun.(*ast.SelectorExpr); ok && c.trySpecial(x, sel) {
		return
	}
	c.calleeExpr(x.Fun)
	newCall := isNewCall(x)
	for i, a := range x.Args {
		// make(T, ...) takes a type as first argument; new(T) also
		// accepts an arbitrary expression (Go 1.26) — only a syntactic
		// type form compiles as a type, anything else evaluates to a
		// value and the builtin distinguishes a typedef argument from
		// a value at run time.
		if i == 0 && (isTypePositionCall(x) || (newCall && isTypeForm(a))) {
			c.typeExpr(a)
			continue
		}
		c.expr(a)
	}
	spread := 0
	if x.Ellipsis.IsValid() {
		spread = 1
	}
	c.emit(bytecode.OpCall, len(x.Args), spread, x.Pos())
}

// trySpecial emits OpSpecialCall when the call's callee resolves to a
// registered special form (importIdent.Name matching a canonical symbol
// in the engine's registry). The arguments stay quoted for the handler.
func (c *compiler) trySpecial(x *ast.CallExpr, sel *ast.SelectorExpr) bool {
	if c.pkg == nil || len(c.pkg.Specials) == 0 || c.file == nil {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	scope := c.pkg.Scopes[c.file]
	if scope == nil {
		return false
	}
	ref, ok := scope[id.Name]
	if !ok {
		return false
	}
	sym := runtime.SymbolID{PackagePath: ref.Path, Name: sel.Sel.Name}
	if _, ok := c.pkg.Specials[sym]; !ok {
		return false
	}
	q := &runtime.QuotedCall{Call: x, File: c.file, Locals: map[string]int{}, Upvals: map[string]int{}}
	// snapshot every visible name: outer blocks first, inner shadows win
	for _, block := range c.fs.blocks {
		for name, slot := range block {
			q.Locals[name] = slot
		}
	}
	for name, i := range c.fs.upmap {
		q.Upvals[name] = i
	}
	c.emit3(bytecode.OpSpecialCall, c.constIdx(sym), c.constIdx(q), 0, x.Pos())
	return true
}

// calleeExpr compiles the called expression; a syntactic type form means a
// conversion call T(x).
func (c *compiler) calleeExpr(fun ast.Expr) {
	if isTypeForm(fun) {
		c.typeExpr(fun)
		return
	}
	c.expr(fun)
}

// isTypePositionCall reports whether the call's first argument is a type
// (the make builtin).
func isTypePositionCall(x *ast.CallExpr) bool {
	id, ok := x.Fun.(*ast.Ident)
	return ok && id.Name == "make"
}

// isNewCall reports whether the call names the new builtin — its first
// argument is a type OR an expression since Go 1.26.
func isNewCall(x *ast.CallExpr) bool {
	id, ok := x.Fun.(*ast.Ident)
	return ok && id.Name == "new"
}

// isTypeForm reports whether e is syntactically a type expression (and thus
// a conversion when used as a call callee).
func isTypeForm(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.ParenExpr:
		return isTypeForm(t.X) // (*T)(x) is a conversion, not a deref call
	case *ast.ArrayType, *ast.MapType, *ast.StructType, *ast.FuncType,
		*ast.InterfaceType, *ast.StarExpr, *ast.ChanType:
		return true
	}
	return false
}

func (c *compiler) compositeLit(x *ast.CompositeLit) {
	c.compileLit(x, x.Type, 0)
}

// compileLit emits a composite literal. baseType is the AST the literal's
// typedef is derived from; depth counts OpElemType peels applied to it —
// element literals may omit their type (`{{1,2}}` inside `[][]int`),
// inheriting the enclosing literal's element type via run-time resolution.
func (c *compiler) compileLit(x *ast.CompositeLit, baseType ast.Expr, depth int) {
	c.typeExpr(baseType)
	for i := 0; i < depth; i++ {
		c.emit(bytecode.OpElemType, 0, 0, x.Pos())
	}
	kv := false
	for _, el := range x.Elts {
		if _, ok := el.(*ast.KeyValueExpr); ok {
			kv = true
			break
		}
	}
	emitVal := func(val ast.Expr) {
		if lit, ok := val.(*ast.CompositeLit); ok && lit.Type == nil {
			c.compileLit(lit, baseType, depth+1)
			return
		}
		c.expr(val)
	}
	// Whether an identifier key is a field name or a real expression is
	// decidable when the peeled element type is syntactically a map or an
	// array — `map[int]int{K: 1}` and `[]int{K: 1}` evaluate K.
	keysAreExprs := literalKeysAreExprs(baseType, depth)
	for _, el := range x.Elts {
		if kvel, isKV := el.(*ast.KeyValueExpr); kv && !isKV {
			// mixed keyed/positional elements — arrays allow it: the
			// positional element takes the running index, resolved at
			// run time since keys may be named constants.
			c.emit(bytecode.OpConst, c.constIdx(&runtime.ImplicitIndex{}), 0, el.Pos())
			emitVal(el)
		} else if kv {
			if lit, ok := kvel.Key.(*ast.CompositeLit); ok && lit.Type == nil {
				// an elided key literal (map[K]V{{...}: v}) inherits
				// the map's declared key type when it is syntactic.
				if mt, ok := peelLitType(baseType, depth).(*ast.MapType); ok {
					c.compileLit(lit, mt.Key, 0)
				} else {
					c.expr(kvel.Key)
				}
			} else if id, ok := kvel.Key.(*ast.Ident); ok && !keysAreExprs {
				// In struct literals the key is a field name, not an
				// expression; the typedef confirms the map/index case.
				c.emit(bytecode.OpConst, c.constIdx(id.Name), 0, id.Pos())
			} else {
				c.expr(kvel.Key)
			}
			emitVal(kvel.Value)
		} else {
			emitVal(el)
		}
	}
	b := 0
	if kv {
		b = 1
	}
	c.emit(bytecode.OpMakeComposite, len(x.Elts), b, x.Pos())
}

// literalKeysAreExprs peels a composite literal's declared element type
// `depth` levels (array elt / map value / pointer / ellipsis) and reports
// whether the resulting type is syntactically a map or array — where a
// key is a real expression, not a field name. Named types (idents,
// selector, instantiations) keep the struct-style name heuristic since
// the underlying shape is not visible to the compiler.
func literalKeysAreExprs(baseType ast.Expr, depth int) bool {
	switch peelLitType(baseType, depth).(type) {
	case *ast.MapType, *ast.ArrayType:
		return true
	}
	return false
}

// peelLitType peels a composite literal's declared type `depth` levels
// (array elt / map value / pointer / ellipsis) — the element type a
// nested literal's shape comes from.
func peelLitType(t ast.Expr, depth int) ast.Expr {
	for i := 0; t != nil && i < depth; i++ {
		switch tt := t.(type) {
		case *ast.ArrayType:
			t = tt.Elt
		case *ast.MapType:
			t = tt.Value
		case *ast.StarExpr:
			t = tt.X
		case *ast.Ellipsis:
			t = tt.Elt
		case *ast.ParenExpr:
			t = tt.X
			i-- // parens don't count as a peel level
		default:
			t = nil
		}
	}
	return t
}

// typeExpr emits a push of *runtime.TypeDef for a type expression.
func (c *compiler) typeExpr(e ast.Expr) {
	switch t := e.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		// named type: resolves through globals/imports at run time
		c.expr(t)
	case *ast.ArrayType:
		// Anon/Pkg/File let OpElemType resolve the element typedef later;
		// Binds carries the generic instantiation so `[]T` resolves T.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	case *ast.MapType:
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	case *ast.StarExpr:
		// *T is a real typedef now: `var p *int` yields a TypedNil,
		// `x.(*T)` asserts on pointer identity, `[]*T{{...}}` auto-takes &.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindPointer, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	case *ast.StructType:
		td := &runtime.TypeDef{Kind: runtime.KindStruct, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}
		td.FTags = runtime.StructFieldTags(t)
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				td.EmbedSpecs = append(td.EmbedSpecs, f.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, embedFieldName(f.Type))
				continue
			}
			for _, n := range f.Names {
				td.Fields = append(td.Fields, n.Name)
			}
		}
		c.emit(bytecode.OpConst, c.constIdx(td), 0, e.Pos())
	case *ast.FuncType:
		// the signature AST rides on the typedef so generalized inference
		// (Go 1.27) can unify it against a generic function's parameters.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Kind: runtime.KindInterface, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				td.IEmbeds = append(td.IEmbeds, m.Type)
				continue
			}
			for _, n := range m.Names {
				td.MReqs = append(td.MReqs, n.Name)
			}
		}
		c.emit(bytecode.OpConst, c.constIdx(td), 0, e.Pos())
	case *ast.ParenExpr:
		c.typeExpr(t.X)
	case *ast.IndexExpr:
		// generic instantiation T[Args]: args in type position
		c.expr(t.X)
		c.typeExpr(t.Index)
		c.emit(bytecode.OpInstantiate, 1, 0, e.Pos())
	case *ast.IndexListExpr:
		c.expr(t.X)
		for _, i := range t.Indices {
			c.typeExpr(i)
		}
		c.emit(bytecode.OpInstantiate, len(t.Indices), 0, e.Pos())
	case *ast.Ellipsis:
		// ...T binds as []T: a variadic param's declared type IS a slice,
		// so a missing rest coerces to TypedNil{slice}, not the elem zero
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: t.Pos(), Elt: t.Elt}, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	case *ast.ChanType:
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: c.pkg, File: c.file, Binds: c.binds}), 0, e.Pos())
	default:
		c.trap(e.Pos(), "unsupported type expression %T", e)
	}
}

// funcLit compiles a function literal into a separate chunk and emits a
// closure creation.
func (c *compiler) funcLit(x *ast.FuncLit) {
	// a synthetic Decl carries the signature so inference can unify a
	// funclit argument against `func(E) R`-shaped parameters.
	inner := &runtime.Function{Pkg: c.pkg, File: c.file, Name: "<funclit>", Decl: &ast.FuncDecl{Type: x.Type, Body: x.Body}}
	ic := &compiler{pkg: c.pkg, file: c.file, fs: newFScope(c.fs), ch: &bytecode.Chunk{Name: "<funclit>"}, labels: map[string]*labelInfo{}, binds: c.binds}
	ic.fs.pushBlock()
	nparams := 0
	var coerces []paramCoerce
	if x.Type.Params != nil {
		for _, field := range x.Type.Params.List {
			names := field.Names
			if len(names) == 0 {
				names = []*ast.Ident{{Name: fmt.Sprintf("$arg%d", nparams)}}
			}
			for _, n := range names {
				slot := ic.fs.declare(n.Name, n.Pos())
				coerces = append(coerces, paramCoerce{slot: slot, typ: field.Type})
				nparams++
			}
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				ic.ch.IsVararg = true
			}
		}
	}
	ic.ch.NParams = nparams
	ic.emitParamCoerces(coerces)
	if x.Type.Results != nil {
		for _, field := range x.Type.Results.List {
			for _, n := range field.Names {
				slot := ic.fs.declare(n.Name, n.Pos())
				ic.ch.NamedSlots = append(ic.ch.NamedSlots, slot)
				ic.emit(bytecode.OpNil, 0, 0, n.Pos())
				ic.emit(bytecode.OpNewLocal, slot, 0, n.Pos())
				ic.emitTypeCoerce(slot, field.Type, n.Pos())
			}
		}
		ic.ch.NResults = countResults(x.Type.Results)
	}
	ic.results = resultTypes(x.Type.Results)
	ic.stmt(x.Body)
	ic.resolveGotos()
	ic.emit(bytecode.OpReturn, ic.ch.NResults, 0, x.End())
	ic.ch.NLocals = ic.fs.nlocals
	ic.ch.Upvals = ic.fs.upvals
	inner.Chunk = ic.ch

	c.emit(bytecode.OpMakeClosure, c.constIdx(inner), 0, x.Pos())
}

// embedFieldName derives the field name of an anonymous (embedded) struct
// field: the base type name, ignoring pointers, packages and type args.
func embedFieldName(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embedFieldName(t.X)
	case *ast.IndexListExpr:
		return embedFieldName(t.X)
	}
	return ""
}

// literalValue converts a BasicLit to a runtime value.
func literalValue(l *ast.BasicLit) (any, error) {
	switch l.Kind {
	case token.INT:
		v := constant.MakeFromLiteral(l.Value, token.INT, 0)
		if i, ok := constant.Int64Val(v); ok {
			return i, nil
		}
		// the one uint64-only literal Go source can spell is
		// 9223372036854775808 — MinInt64's magnitude, spelled under a
		// unary minus.
		if u, ok := constant.Uint64Val(v); ok {
			if u == 1<<63 {
				return int64(u), nil
			}
			// wider uint64 literals stay boxed: arithmetic unwraps them
			// to int64 (same bits mod 2^64) and formatting reads the box.
			return &runtime.GoValue{V: u}, nil
		}
		// beyond uint64 the literal stays an untyped constant: it compiles
		// (Go does too) and only materializing it as a value can fail.
		return &runtime.UConst{V: v}, nil
	case token.FLOAT:
		v := constant.MakeFromLiteral(l.Value, token.FLOAT, 0)
		f, _ := constant.Float64Val(v)
		if math.IsInf(f, 0) {
			// 1e500 is a legal untyped constant; it fails only when it
			// has to fit a float64 (`var f = 1e500` is a compile error).
			return &runtime.UConst{V: v}, nil
		}
		return f, nil
	case token.IMAG:
		// imaginary literals exist only in the constant domain until
		// materialized into a complex64/128 value.
		return &runtime.UConst{V: constant.MakeFromLiteral(l.Value, token.IMAG, 0)}, nil
	case token.STRING:
		return strconv.Unquote(l.Value)
	case token.CHAR:
		// rune literals stay untyped so a bare 'a' defaults to rune
		// (int32) while still converting into any numeric target.
		return &runtime.UConst{V: constant.MakeFromLiteral(l.Value, token.CHAR, 0), Rune: true}, nil
	}
	return nil, fmt.Errorf("unsupported literal kind %s", l.Kind)
}

// orderSpecs sorts var/const specs in dependency order (Go spec: package-level
// initialization proceeds in dependency order, with source order as the
// tie-breaker). A spec depends on every package-level name free in its value
// expressions — including names reached transitively through function bodies
// (`var x = f()` depends on every package-level var f reads, and on what
// functions f calls read, recursively). Cyclic leftovers keep source order.
func orderSpecs(ix *index.Index, reps []*index.Decl) []*index.Decl {
	declared := map[string]bool{}
	for n := range ix.Vars {
		declared[n] = true
	}
	for n := range ix.Consts {
		declared[n] = true
	}

	// idents referenced by an AST (also covers nested func literals)
	refs := func(n ast.Node, out map[string]bool) {
		ast.Inspect(n, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok {
				out[id.Name] = true
			}
			return true
		})
	}

	// funcRefs(name) = package-level names reachable from the function's
	// body, transitively through other functions/methods it references.
	funcRefsCache := map[string]map[string]bool{}
	var funcRefs func(name string, depth int) map[string]bool
	funcRefs = func(name string, depth int) map[string]bool {
		if depth > 16 {
			return nil // recursion budget: cycles/deep chains keep source order
		}
		if r, ok := funcRefsCache[name]; ok {
			return r
		}
		d := ix.Funcs[name]
		if d == nil {
			for _, td := range ix.Types {
				if m := td.Methods[name]; m != nil {
					d = m
					break
				}
			}
		}
		if d == nil {
			return nil
		}
		r := map[string]bool{}
		refs(d.Func, r)
		// follow function-valued references one level further
		for n := range r {
			if declared[n] {
				continue
			}
			for m := range funcRefs(n, depth+1) {
				r[m] = true
			}
		}
		funcRefsCache[name] = r
		return r
	}

	// spec -> specs it depends on (a spec provides its names)
	providedBy := map[string]*ast.ValueSpec{}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		for _, n := range vs.Names {
			providedBy[n.Name] = vs
		}
	}

	deps := map[*ast.ValueSpec]map[*ast.ValueSpec]bool{}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		vals := vs.Values
		if len(vals) == 0 {
			vals = d.Inherited
		}
		ds := map[*ast.ValueSpec]bool{}
		names := map[string]bool{}
		for _, e := range vals {
			refs(e, names)
		}
		// widen direct references through function bodies
		for n := range names {
			if declared[n] {
				continue
			}
			for m := range funcRefs(n, 0) {
				names[m] = true
			}
		}
		for n := range names {
			if !declared[n] {
				continue
			}
			if dep := providedBy[n]; dep != nil && dep != vs {
				ds[dep] = true
			}
		}
		deps[vs] = ds
	}

	// dependency-driven DFS: walk decls in source order; before emitting a
	// spec, emit each spec it depends on (Go initializes a variable's
	// dependencies at its point in the declaration order, not globally).
	bySpec := map[*ast.ValueSpec]*index.Decl{}
	for _, d := range reps {
		bySpec[d.Spec.(*ast.ValueSpec)] = d
	}
	var out []*index.Decl
	done := map[*ast.ValueSpec]bool{}
	visiting := map[*ast.ValueSpec]bool{} // cycle guard
	var visit func(d *index.Decl)
	visit = func(d *index.Decl) {
		vs := d.Spec.(*ast.ValueSpec)
		if done[vs] || visiting[vs] {
			return
		}
		visiting[vs] = true
		for dep := range deps[vs] {
			if dd, ok := bySpec[dep]; ok {
				visit(dd)
			}
		}
		delete(visiting, vs)
		done[vs] = true
		out = append(out, d)
	}
	for _, d := range reps {
		visit(d)
	}
	return out
}
