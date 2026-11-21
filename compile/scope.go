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
	"go/constant"
	"go/token"
	"strings"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/spike"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// binding is everything the compiler knows about one declared name:
// its slot and declaring position plus, for local `type` decls, the
// type metadata the decl carries. One record per name keeps the
// name->slot map and the type metadata on the same block lifetime —
// a popped block drops its types entirely and a shadowing decl
// replaces the record whole.
type binding struct {
	slot     int              // local slot the name occupies
	pos      token.Pos        // declaring position (goto scoping)
	typeDecl bool             // bound by a local `type` decl, not a var
	tspec    *ast.TypeSpec    // the `type` decl's spec (shape checks)
	tdef     *runtime.TypeDef // the `type` decl's typedef (local embed resolution)
	ifaceT   bool             // the `type` decl's spec is an interface
	ifaceVar bool             // a var declared interface-typed
	// declTyp is the var/param's statically-known type expression when
	// one is written or syntactically inferred (`var x T`, `x := T(v)`)
	// — the compile-time operand-assignability gate reads it; nil means
	// the name's type is unknown statically.
	declTyp ast.Expr
	// declTypFile is the file declTyp's spelling belongs to when it
	// re-derived from a package decl — its names resolve in that
	// package scope, not the use site's.
	declTypFile *syntax.File
	// declDefer holds the initializer value expression whose member
	// type is a deferred fact (`var y = p.C` when the index couldn't
	// answer p.C): reads of the name report deferred so OpTypGate
	// asks the question at run time.
	declDefer ast.Expr
	// untypedConst marks a `const` bound without a declared type: the
	// operand adopts the other side's type, like a literal does.
	untypedConst bool
	// constDecl marks a `const`-bound name — typed or not — so the
	// constant-expression walk can tell it from a var. An untyped const
	// carries untypedConst instead of a declTyp.
	constDecl bool
	// constVal is a const-bound name's folded value, computed where the
	// name binds — the const evaluator folds references at their use
	// sites without re-resolving the initializer in the use scope.
	constVal constant.Value
	// dte is an untyped const's default type expression — the type it
	// binds at `y := c` (`const c = 5` makes y an int like gc).
	dte ast.Expr
}

// fscope is the static scope model of one function while compiling.
type fscope struct {
	parent   *fscope
	blocks   []map[string]*binding // name -> binding
	blockIDs []int                 // unique id per open block, for goto scoping
	nextID   int
	iota     int // slot backing the `iota` builtin in local const specs; -1 until declared
	nlocals  int
	upvals   []bytecode.UpvalDesc
	upmap    map[string]int
	// boxed is the P4-2 slot classification: the slots whose cell can
	// outlive the frame (address taken, captured, shared with a special
	// form, or bound through a pointer-receiver select) — everything
	// else stays a plain local.
	boxed map[int]bool
}

func newFScope(parent *fscope) *fscope {
	return &fscope{parent: parent, iota: -1, upmap: map[string]int{}}
}

// iotaSlot lazily declares the hidden local backing the `iota` builtin
// inside this function's local const specs — a user `const iota = ...`
// shadows it like any other declared name. The slot lives for the whole
// function but its name binding lives in the declaring block, so a const
// in a sibling block re-binds it: `{const A = iota}; {const B = iota}`
// resolves iota in both blocks while a user-declared iota keeps winning.
func (s *fscope) iotaSlot() int {
	if s.iota < 0 {
		s.iota = s.declare("iota", token.NoPos)
		return s.iota
	}
	if _, found := s.lookupLocal("iota"); !found {
		if len(s.blocks) == 0 {
			s.pushBlock()
		}
		s.blocks[len(s.blocks)-1]["iota"] = &binding{slot: s.iota, untypedConst: true}
	}
	return s.iota
}

func (s *fscope) pushBlock() {
	s.nextID++
	s.blocks = append(s.blocks, map[string]*binding{})
	s.blockIDs = append(s.blockIDs, s.nextID)
}

func (s *fscope) popBlock() {
	s.blocks = s.blocks[:len(s.blocks)-1]
	s.blockIDs = s.blockIDs[:len(s.blockIDs)-1]
}

// recordType marks a just-declared name as a local `type` decl,
// attaching the spec/typedef/interface metadata the decl carries.
func (s *fscope) recordType(name string, ts *ast.TypeSpec, td *runtime.TypeDef, iface bool) {
	if len(s.blocks) == 0 {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.typeDecl = true
		b.tspec = ts
		b.tdef = td
		b.ifaceT = iface
	}
}

// markIface records a just-declared name as interface-typed (its decl's
// type annotation or initializer was an interface type/expression).
// Interface-tag switches use it to pick pair-strict case equality.
func (s *fscope) markIface(name string) {
	if len(s.blocks) == 0 {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.ifaceVar = true
	}
}

// noteConstDecl marks a just-declared `const` name — typed or untyped
// — so constant-expression checks can distinguish it from a var.
func (s *fscope) noteConstDecl(name string) {
	if len(s.blocks) == 0 {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.constDecl = true
	}
}

// noteConstVal records a just-declared `const` name's folded value, so
// the scope-aware const evaluator folds references to it.
func (s *fscope) noteConstVal(name string, cv constant.Value) {
	if len(s.blocks) == 0 || cv == nil {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.constVal = cv
	}
}

// noteUntypedConst marks a just-declared `const` name as untyped (no
// declared type and no typed initializer): it adopts the other
// operand's type in a comparison. dte is the const's default type —
// what a `y := c` bind gets.
func (s *fscope) noteUntypedConst(name string, dte ast.Expr) {
	if len(s.blocks) == 0 {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.untypedConst = true
		b.dte = dte
	}
}

// localConsts maps every in-scope local declaration to whether it is
// const-bound — true for a `const` name, false for vars, params and
// type decls (a shadowing non-const must still answer for its name so
// a package const of the same name does not leak through). Package
// members never appear here.
func (s *fscope) localConsts() map[string]bool {
	seen := map[string]bool{}
	var out map[string]bool
	for cur := s; cur != nil; cur = cur.parent {
		for i := len(cur.blocks) - 1; i >= 0; i-- {
			for name, b := range cur.blocks[i] {
				if seen[name] {
					continue
				}
				seen[name] = true
				if out == nil {
					out = map[string]bool{}
				}
				out[name] = b.constDecl
			}
		}
	}
	return out
}

// lookupBinding walks the scope chain for the innermost declaration of
// name and returns its binding record.
func (s *fscope) lookupBinding(name string) *binding {
	for cur := s; cur != nil; cur = cur.parent {
		for i := len(cur.blocks) - 1; i >= 0; i-- {
			if b, declared := cur.blocks[i][name]; declared {
				return b
			}
		}
	}
	return nil
}

// isIfaceVar reports whether the innermost declaration of name was
// interface-typed; an unmarked shadow decl clears an outer mark.
func (s *fscope) isIfaceVar(name string) bool {
	if b := s.lookupBinding(name); b != nil {
		return b.ifaceVar
	}
	return false
}

// localTypeDefs collects the typedefs of every local `type` decl
// visible from this scope — inner declarations shadow outer ones.
func (s *fscope) localTypeDefs() map[string]*runtime.TypeDef {
	seen := map[string]bool{}
	var out map[string]*runtime.TypeDef
	for cur := s; cur != nil; cur = cur.parent {
		for i := len(cur.blocks) - 1; i >= 0; i-- {
			for name, b := range cur.blocks[i] {
				if seen[name] {
					continue
				}
				seen[name] = true
				if b.tdef != nil {
					if out == nil {
						out = map[string]*runtime.TypeDef{}
					}
					out[name] = b.tdef
				}
			}
		}
	}
	return out
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
	for i := len(s.blocks) - 1; i >= 0; i-- {
		for n, b := range s.blocks[i] {
			if s.isTypeDeclName(n) || strings.HasPrefix(n, "$") {
				continue // type decls and internal slots are not variable decls
			}
			if _, seen := vars[n]; !seen {
				vars[n] = b.pos
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
	s.blocks[len(s.blocks)-1][name] = &binding{slot: slot, pos: pos}
	return slot
}

func (s *fscope) lookupLocal(name string) (int, bool) {
	for i := len(s.blocks) - 1; i >= 0; i-- {
		if b, ok := s.blocks[i][name]; ok {
			return b.slot, true
		}
	}
	return 0, false
}

// box marks a local slot of this function as escaping its frame; why
// attributes the mark for spike measurement ("ref", "fieldref",
// "select", "capture", "special").
func (s *fscope) box(slot int, why string) {
	if s.boxed == nil {
		s.boxed = map[int]bool{}
	}
	if !s.boxed[slot] {
		spike.NoteBoxReason(why)
	}
	s.boxed[slot] = true
}

// boxName marks the slot name currently resolves to — in whichever
// enclosing function scope declared it — as escaping its frame. A name
// that resolves to no local binding (a global, a builtin) marks
// nothing; a name the inner scopes only reach through upvals still
// finds its declaring scope's binding here, so an `&x` on an upvalue
// boxes the owner's slot directly.
func (s *fscope) boxName(name, why string) {
	for cur := s; cur != nil; cur = cur.parent {
		for i := len(cur.blocks) - 1; i >= 0; i-- {
			if b, ok := cur.blocks[i][name]; ok {
				cur.box(b.slot, why)
				return
			}
		}
	}
}

// boxedSlice renders the classification as a per-slot flag slice sized
// to nlocals — always non-nil so a compiled chunk reports a computed
// (empty or not) answer rather than "unclassified".
func (s *fscope) boxedSlice() []bool {
	boxed := make([]bool, s.nlocals)
	for slot := range s.boxed {
		if slot < len(boxed) {
			boxed[slot] = true
		}
	}
	return boxed
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
	if !fromUpval {
		// the captured var's cell moves into this closure's upvalue
		// table — it outlives the parent's frame.
		s.parent.box(idx, "capture")
	}
	s.upvals = append(s.upvals, bytecode.UpvalDesc{FromParentUpval: fromUpval, Index: idx})
	s.upmap[name] = len(s.upvals) - 1
	return true
}

// nameInfo is what an identifier resolves to: whether it names a
// type, plus the type details callers inspect (its decl spec, its
// typedef, and whether the resolved type is an interface).
type nameInfo struct {
	isType  bool
	tspec   *ast.TypeSpec    // decl spec when syntactically known (local `type` or package index)
	td      *runtime.TypeDef // typedef when the resolution carries one (index, binds)
	iface   bool             // the resolved type is an interface type
	predecl bool             // resolved by the universe block — nothing user-declared answered
}

// resolveName reports what an identifier resolves to, walking the one
// scope ladder every name classifier shares: innermost local/upval
// binding, generic instantiation binds, package index decls, then
// predeclared types. A nearer declaration always wins — a local var
// shadows a package `type` of the same name, which the hand-duplicated
// ladders used to miss.
func (c *compiler) resolveName(name string) (nameInfo, bool) {
	// lookupBinding is a pure scope walk — find() would capture an
	// enclosing name into this function's upvals as a side effect,
	// and a classifier must never emit. Under pkgScopeTyp the name
	// was spelled in a package decl — the use site's locals and
	// upvalues cannot shadow it (the same rule typeIdent applies at
	// emit).
	if c.pkgScopeTyp == nil {
		if b := c.fs.lookupBinding(name); b != nil {
			if b.typeDecl {
				return nameInfo{isType: true, tspec: b.tspec, td: b.tdef, iface: b.ifaceT}, true
			}
			return nameInfo{}, true // a var/const binding — never a type
		}
		if _, isUp := c.fs.upmap[name]; isUp {
			return nameInfo{}, true // an already-captured var
		}
	}
	if bv, bound := c.binds[name]; bound {
		info := nameInfo{isType: true} // generic instantiation binds name a type
		if td, ok := bv.(*runtime.TypeDef); ok && td != nil {
			info.td = td
			info.iface = td.Kind == runtime.KindInterface
		}
		return info, true
	}
	if c.pkg != nil && c.pkg.Index != nil {
		idx := c.pkg.Index
		if tdi := idx.Types[name]; tdi != nil {
			info := nameInfo{isType: true}
			// a methods-only index entry (a `func (x T) M` decl whose T
			// lives in another file/package, e.g. a REPL graft) has no
			// Decl — the name still resolves to a type.
			if tdi.Decl != nil {
				if ts, ok := tdi.Decl.Spec.(*ast.TypeSpec); ok {
					info.tspec = ts
					_, info.iface = ts.Type.(*ast.InterfaceType)
				}
			}
			return info, true
		}
		if idx.Vars[name] != nil || idx.Funcs[name] != nil || idx.Consts[name] != nil {
			return nameInfo{}, true
		}
	}
	if predeclaredTypeNames[name] {
		return nameInfo{isType: true, iface: name == "any" || name == "error", predecl: true}, true
	}
	return nameInfo{}, false
}

// declTypCanon rewrites an ident type expression that resolves, in the
// declaration's own scope, to a predeclared type into a position-less
// marker: `var x int` binds builtin int, and an inner block's later
// `type int uint8` must not retype x when a gate resolves the name in
// the use site's scope. A decl-scope shadow keeps the real ident —
// `var x int` inside a `type int` block IS the named type.
func (c *compiler) declTypCanon(te ast.Expr) ast.Expr {
	id, ok := ast.Unparen(te).(*ast.Ident)
	if !ok || id.NamePos == token.NoPos || !predeclaredTypeNames[id.Name] {
		return te
	}
	if info, found := c.resolveName(id.Name); found && info.predecl && !info.iface {
		return &ast.Ident{Name: id.Name}
	}
	return te
}

// typeIdent emits the typedef an identifier names in type position —
// `var x T`, `f() T`, `x.(T)`. A type-position name prefers the nearest
// TYPE declaration: a shadowing var (`case item := <-ch` inside a
// function returning `item`) must not push the variable where
// OpCoerce* expects a typedef.
func (c *compiler) typeIdent(e ast.Expr, name string) {
	if id, ok := e.(*ast.Ident); ok && id.NamePos == token.NoPos && predeclaredTypeNames[name] && runtime.BasicTypedef(name) != nil {
		// a synthesized predeclared name — `int` for a range index,
		// `rune` for a string element, `bool` for comma-ok — names
		// the builtin type itself: no shadow can rename it.
		c.emit(bytecode.OpConst, c.constIdx(runtime.BasicTypedef(name)), 0, e.Pos())
		return
	}
	if c.pkgScopeTyp != nil {
		// a re-derived package decl's type resolves in package
		// scope — the use site's locals and bound type params
		// cannot rename it.
		if c.pkg != nil && c.pkg.Index != nil && c.pkg.Index.Types[name] != nil {
			c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, e.Pos())
			return
		}
		if predeclaredTypeNames[name] {
			c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, e.Pos())
			return
		}
	}
	if b := c.fs.lookupBinding(name); b != nil {
		if b.typeDecl {
			if b.tdef != nil {
				c.emit(bytecode.OpConst, c.constIdx(b.tdef), 0, e.Pos())
				return
			}
			c.expr(e)
			return
		}
		// a var/const shadows the name here — resolve the type it hides.
		if bv, bound := c.binds[name]; bound {
			if td, ok := bv.(*runtime.TypeDef); ok {
				c.emit(bytecode.OpConst, c.constIdx(td), 0, e.Pos())
				return
			}
		}
		if c.pkg != nil && c.pkg.Index != nil && c.pkg.Index.Types[name] != nil {
			c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, e.Pos())
			return
		}
		if predeclaredTypeNames[name] {
			c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, e.Pos())
			return
		}
	}
	c.expr(e)
}

// recvTypeExpr rewrites blank type arguments in a method receiver's
// instantiation — `func (f *Foo[_, _])` binds the generic's own type
// parameters positionally, so `_` at argument i names the base type's
// declared TParams[i], not a fresh name. Resolving it that way makes the
// receiver's coerce target the same specialization the argument carries
// (`*Foo[string,int]` binds `*Foo[_, _]`). The shared AST is never
// mutated — each rewritten node is a shallow copy.
func (c *compiler) recvTypeExpr(e ast.Expr) ast.Expr {
	var args []ast.Expr
	var base ast.Expr
	star := false
	t := e
	if s, ok := t.(*ast.StarExpr); ok {
		star = true
		t = s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		base = x.X
		args = []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		base = x.X
		args = x.Indices
	default:
		return e
	}
	names := c.recvTParamNames(base)
	if names == nil {
		return e
	}
	var out []ast.Expr
	for i, a := range args {
		if id, ok := a.(*ast.Ident); ok && id.Name == "_" && i < len(names) {
			if out == nil {
				out = append([]ast.Expr(nil), args...)
			}
			out[i] = &ast.Ident{NamePos: id.NamePos, Name: names[i]}
		}
	}
	if out == nil {
		return e
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = &ast.IndexExpr{X: x.X, Lbrack: x.Lbrack, Index: out[0], Rbrack: x.Rbrack}
	case *ast.IndexListExpr:
		t = &ast.IndexListExpr{X: x.X, Lbrack: x.Lbrack, Indices: out, Rbrack: x.Rbrack}
	}
	if star {
		s := e.(*ast.StarExpr)
		return &ast.StarExpr{Star: s.Star, X: t}
	}
	return t
}

// recvTParamNames resolves the base of a receiver instantiation to the
// type-parameter names its type declaration declares.
func (c *compiler) recvTParamNames(x ast.Expr) []string {
	id, ok := x.(*ast.Ident)
	if !ok || c.pkg == nil || c.pkg.Index == nil {
		return nil
	}
	td, ok := c.pkg.Index.Types[id.Name]
	if !ok || td.Decl == nil {
		return nil
	}
	spec, ok := td.Decl.Spec.(*ast.TypeSpec)
	if !ok || spec.TypeParams == nil {
		return nil
	}
	var names []string
	for _, f := range spec.TypeParams.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// universeIdent reports whether a bare identifier in a foreign
// package's value expression resolves identically in every scope —
// the universe block's values and builtins, plus predeclared types a
// conversion can name. Anything else must resolve through the
// declaring package's own decls or imports.
func universeIdent(name string) bool {
	return syntax.IsPredeclaredValue(name) || predeclaredTypeNames[name]
}

// universeFuncs are the predeclared functions certainlyNonConst leaves
// undecided.
var universeFuncs = map[string]struct{}{
	"len": {}, "cap": {}, "min": {}, "max": {}, "real": {}, "imag": {}, "complex": {},
}

// intTypeNames is the predeclared integer spellings (canonical form —
// byte and rune resolve through CanonicalBasicName).
var intTypeNames = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

var predeclaredTypeNames = map[string]bool{
	"bool": true, "byte": true, "rune": true, "string": true,
	"error": true, "any": true, "complex64": true, "complex128": true,
	"float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true,
}

// isTypeDeclName reports whether the innermost local/upval declaration of
// name is a `type` decl rather than a variable (walking enclosing scopes).
func (s *fscope) isTypeDeclName(name string) bool {
	if b := s.lookupBinding(name); b != nil {
		return b.typeDecl
	}
	return false
}
