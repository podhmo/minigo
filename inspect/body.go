// body.go — the function-body view (docs/sketch/experiment-inspect-body-dataflow.md).
//
// Ops(decl) answers the TODO question "how does a script touch a function's
// body": the decl's body is compiled by the real minigo compiler into its
// stack-VM bytecode, then lifted here into a flat op list with explicit
// value ids — the "special VM code" that lets a script track call
// arguments and return values without interpreting the code.
//
// Each Op defines a value id (Out) and consumes value ids (Ins), so the
// list is a dataflow graph: a `call` op names its callee register (Fun)
// and argument registers (Args, also the tail of Ins), `param` entries
// bind caller arguments to callee parameters when descending into helper
// functions, and `ret` ops expose what the body returns. Value-producing
// ops whose inputs carry a resolved symbol propagate it so a call's Sym
// names the real callee (e.g. strconv.Atoi -> {strconv, Atoi}) — a script
// descends by resolving Sym through Symbol/PackageOf, and bound or stdlib
// packages simply yield no source decl, which is what keeps the analysis
// shallow: the lifter never interprets callee bodies the script doesn't
// ask for.
//
// The emulation is a single linear pass — it ignores jump targets, so it
// is a may-analysis: every instruction contributes its value edges
// regardless of the branch that reaches it. That over-approximation is
// what pattern detectors (linters, parameter inference) want.
package inspect

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"sort"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/compile"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Op is one instruction of a lifted function body.
//
// Kind groups the ~60 bytecode opcodes into analysis-friendly classes:
//
//	param    function parameter (value is bound on entry; see Body.Params)
//	lit      constant literal (Lit carries the rendered value)
//	global   package-scope name load (Name + Sym — imports resolve to {pkg,""})
//	ref      local/upvalue read (Slot; Ins = values currently bound there)
//	cellref  address-of read (&x / &s.f / &s[i]) — Slot/Name says which cell
//	bind     declaration-time store (Slot or Name; Tok: const|renew)
//	set      assignment store (Slot/Name; Ins includes base for field/index)
//	sel      member selection .Name (Ins=[base])
//	index    index/slice (Ins=[base,key] or [base,lo,hi]; Tok: ok|slice)
//	comp     composite literal
//	call     f(args) — Fun is the callee value id, Args the arguments
//	defer    call scheduled for frame teardown (Fun/Args like call)
//	go       call run as a goroutine (Fun/Args like call)
//	func     closure value (Body is the lifted literal body)
//	pack     multi-value pack; unpack = tuple explode (NOut>1)
//	inst     generic instantiate OR index (shared OpInstantiate encoding)
//	iter     range iterator creation; range = per-iteration yields (NOut)
//	coerce   declared-type coercion (Typ = the declared type; Slot or top)
//	assert   type assertion (Tok "ok" for comma-ok)
//	type     generic instantiation / element-type query
//	ctrl     jump/branch (Ins = the condition value)
//	ret      return (Ins = the returned value ids)
//	panic / trap
//	special  quoted special-form call (Sym = registered symbol)
//	eval     lazy AST fragment evaluation
//	chan     channel op (Name: send|recv|recvok|selsend|selrecv)
//	move     stack shuffling (dup/swap/rot)
//	expr     arithmetic/other value expression (Name = operator)
//	nop
type Op struct {
	I    int              // instruction index in the compiled chunk
	Code string           // raw bytecode opcode name (e.g. "OpCall")
	Kind string           // lifted class — see the comment above
	Out  int              // first value id defined, -1 when the op defines none
	NOut int              // number of value ids defined (usually 0 or 1)
	Ins  *runtime.Slice   // value ids consumed, in stack order ([]int64)
	Slot int              // local/upvalue slot involved (-1 = n/a)
	Name string           // member/symbol/operator name
	Sym  runtime.SymbolID // resolved callee for call/defer/go, resolved global ref, special symbol
	Typ  *TypeExpr        // declared type for coerce ops (nil otherwise)
	Lit  string           // literal value for Kind "lit"
	Tok  string           // secondary tag: spread|const|renew|upval|ok|slice|top|n|bare|implicit
	Fun  int              // call-like kinds: callee value id (-1 otherwise)
	Args *runtime.Slice   // call-like kinds: argument value ids ([]int64)
	Pos  string           // file:line:col of the originating expression
	Text string           // rendered source text of the originating expr (best-effort)
	Body *Body            // nested body for Kind "func" (nil otherwise)

	expr ast.Expr // originating expression — engine-only
}

// Expr exposes the originating AST expression — engine-only.
func (o *Op) Expr() ast.Expr { return o.expr }

// Param is one function parameter (or receiver) with the value id it is
// bound to on entry — caller Args[i] maps to Params[i].Val when
// descending into a callee.
type Param struct {
	Val  int // value id this parameter holds on entry
	Slot int // local slot index
	Name string
	Type *TypeExpr // declared type (nil when uninferrable)
	Recv bool      // receiver slot (methods only)
}

// Body is the lifted view of one function body.
type Body struct {
	Ops      *runtime.Slice // []*Op
	Params   *runtime.Slice // []*Param
	NResults int
	Name     string

	pkg  *runtime.Package
	file *syntax.File
	fset *token.FileSet
}

// OpSlice unwraps the boxed op list — engine-only.
func (b *Body) OpSlice() []*Op {
	if b == nil || b.Ops == nil {
		return nil
	}
	out := make([]*Op, 0, len(b.Ops.Elems))
	for _, v := range b.Ops.Elems {
		if gv, ok := v.(*runtime.GoValue); ok {
			if o, ok := gv.V.(*Op); ok {
				out = append(out, o)
			}
		}
	}
	return out
}

// ParamSlice unwraps the boxed param list — engine-only.
func (b *Body) ParamSlice() []*Param {
	if b == nil || b.Params == nil {
		return nil
	}
	out := make([]*Param, 0, len(b.Params.Elems))
	for _, v := range b.Params.Elems {
		if gv, ok := v.(*runtime.GoValue); ok {
			if p, ok := gv.V.(*Param); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// OpsOf compiles a function/method decl and lifts its body to the op
// dataflow view. Non-func decls (vars, consts, types) and host pseudo-decls
// return nil — the script treats that as "no body to descend into", which
// is what bounds the analysis to source code.
func OpsOf(d *Decl) (*Body, error) {
	if d == nil || d.decl == nil || d.decl.Func == nil {
		return nil, nil
	}
	pkg, file := d.Package, d.file
	ctx := &liftCtx{
		pkg:   pkg,
		file:  file,
		exprs: indexExprs(d.decl.Func),
	}
	if pkg != nil {
		ctx.fset = pkg.Fset
		if file != nil {
			ctx.scope = pkg.Scopes[file]
		}
	}
	fn := &runtime.Function{Pkg: pkg, File: file, Decl: d.decl.Func, Name: d.Name}
	if err := compile.Func(fn); err != nil {
		return nil, fmt.Errorf("inspect.Ops: compile %s: %w", d.Name, err)
	}
	return ctx.liftFunc(d.decl.Func, fn.Chunk, d.Name), nil
}

// liftCtx is the resolution context shared across a body and its nested
// function literals (which live in the same file/scope).
type liftCtx struct {
	pkg   *runtime.Package
	file  *syntax.File
	fset  *token.FileSet
	scope map[string]*runtime.ImportRef
	exprs map[token.Pos][]ast.Expr // position -> exprs at that position
	depth int
}

// lifter is the per-body symbolic-emulation state.
type lifter struct {
	*liftCtx
	ch    *bytecode.Chunk          // the chunk being lifted
	stack []int                    // symbolic stack: value ids
	slots map[int]map[int]bool     // local slot -> value ids bound (may-set)
	uv    map[int]map[int]bool     // upvalue index -> value ids
	sym   map[int]runtime.SymbolID // value id -> resolved symbol
	nextV int
	ops   []*Op
}

// builtinFuncs are the names resolveGlobal finds after imports and
// package scope — they resolve as ":builtin:" symbols so scripts don't
// confuse them for package members or descendable calls.
var builtinFuncs = map[string]bool{
	"len": true, "cap": true, "append": true, "copy": true, "delete": true,
	"make": true, "new": true, "close": true, "panic": true, "recover": true,
	"print": true, "println": true, "complex": true, "real": true, "imag": true,
	"min": true, "max": true, "clear": true,
}

func (l *lifter) pop() int {
	if len(l.stack) == 0 {
		return -1 // underflow: dead-code path — keep going, value id -1 = unknown
	}
	v := l.stack[len(l.stack)-1]
	l.stack = l.stack[:len(l.stack)-1]
	return v
}

func (l *lifter) popN(n int) []int {
	xs := make([]int, 0, n)
	for i := 0; i < n; i++ {
		xs = append(xs, l.pop())
	}
	// popped ids come back in reverse stack order — flip them
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
	return xs
}

func (l *lifter) push() int {
	v := l.nextV
	l.nextV++
	l.stack = append(l.stack, v)
	return v
}

func (l *lifter) pushExisting(v int) {
	l.stack = append(l.stack, v)
}

// setvals reads a slot->values map as a sorted slice.
func setvals(m map[int]bool) []int {
	xs := make([]int, 0, len(m))
	for v := range m {
		xs = append(xs, v)
	}
	sort.Ints(xs)
	return xs
}

func (l *lifter) slotIns(slot int) []int { return setvals(l.slots[slot]) }
func (l *lifter) upvalIns(idx int) []int { return setvals(l.uv[idx]) }

// bindSlot records a stored value into a slot's may-set (union semantics).
func (l *lifter) bindSlot(slot int, vals ...int) {
	if l.slots[slot] == nil {
		l.slots[slot] = map[int]bool{}
	}
	for _, v := range vals {
		if v >= 0 {
			l.slots[slot][v] = true
		}
	}
}

func (l *lifter) bindUpval(idx int, vals ...int) {
	if l.uv[idx] == nil {
		l.uv[idx] = map[int]bool{}
	}
	for _, v := range vals {
		if v >= 0 {
			l.uv[idx][v] = true
		}
	}
}

// resolveGlobal names a package-scope identifier: an import-local name
// yields the package symbol {path, ""}, a builtin yields {:builtin:, name},
// anything else is a member of the current package.
func (l *lifter) resolveGlobal(name string) runtime.SymbolID {
	if l.scope != nil {
		if ref, ok := l.scope[name]; ok {
			return runtime.SymbolID{PackagePath: ref.Path, Name: ""}
		}
	}
	if builtinFuncs[name] || predeclared[name] {
		return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: name}
	}
	path := ""
	if l.pkg != nil {
		path = l.pkg.Path
	}
	return runtime.SymbolID{PackagePath: path, Name: name}
}

// propagateSym carries a value's resolved symbol through value-identity
// ops (single-source chains like ref/deref). Selections extend only a
// package-level symbol ({path,""} -> {path, member}).
func (l *lifter) propagateSym(out int, ins []int) {
	found := runtime.SymbolID{}
	n := 0
	for _, v := range ins {
		if s, ok := l.sym[v]; ok {
			found = s
			n++
		}
	}
	if n == 1 {
		l.sym[out] = found
	}
}

// typOf recovers the declared type an op's typedef input describes:
// named types arrive as a global/sel chain (Sym/expr attached), anonymous
// composites as a *runtime.TypeDef const (Anon is the source AST).
func (l *lifter) typOf(v int) *TypeExpr {
	if v < 0 || v >= len(l.ops) {
		return nil
	}
	var src *Op
	for i := len(l.ops) - 1; i >= 0; i-- {
		if l.ops[i].Out >= 0 && l.ops[i].Out <= v && v < l.ops[i].Out+l.ops[i].NOut {
			src = l.ops[i]
			break
		}
	}
	if src == nil {
		return nil
	}
	if src.Typ != nil {
		return src.Typ
	}
	if src.expr != nil {
		return NewTypeExpr(src.expr, l.file, l.pkg)
	}
	return nil
}

// pickExpr chooses the originating AST node at an instruction's position.
// All expressions in a chain share the head position (e.g. every node of
// `r.URL.Query().Get` starts at `r`), so the kind/name narrows it: a call
// op wants the CallExpr (there may be several nested at the same pos —
// the outermost ends last), a sel op the SelectorExpr naming the member.
func pickExpr(cands []ast.Expr, kind string, name string) ast.Expr {
	var best ast.Expr
	bestEnd := token.Pos(0)
	consider := func(e ast.Expr) {
		if e.End() > bestEnd {
			best, bestEnd = e, e.End()
		}
	}
	for _, e := range cands {
		switch kind {
		case "call", "defer", "go", "special":
			if ce, ok := e.(*ast.CallExpr); ok {
				consider(ce)
			}
		case "func":
			if fe, ok := e.(*ast.FuncLit); ok {
				consider(fe)
			}
		case "sel":
			if se, ok := e.(*ast.SelectorExpr); ok && (name == "" || se.Sel.Name == name) {
				consider(se)
			}
		case "index":
			switch e.(type) {
			case *ast.IndexExpr, *ast.SliceExpr:
				consider(e)
			}
		case "comp":
			if ce, ok := e.(*ast.CompositeLit); ok {
				consider(ce)
			}
		case "global", "ref":
			if id, ok := e.(*ast.Ident); ok && (name == "" || id.Name == name) {
				consider(id)
			}
		default:
			consider(e)
		}
	}
	return best
}

// indexExprs maps each expression's Pos to the expressions starting
// there, for Text/Expr recovery after lifting.
func indexExprs(root ast.Node) map[token.Pos][]ast.Expr {
	m := map[token.Pos][]ast.Expr{}
	ast.Inspect(root, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			m[e.Pos()] = append(m[e.Pos()], e)
		}
		return true
	})
	return m
}

// render prints an expr compactly for Op.Text.
func (l *lifter) render(e ast.Expr) string {
	if e == nil {
		return ""
	}
	var buf bytes.Buffer
	fset := l.fset
	if fset == nil {
		fset = token.NewFileSet()
	}
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return ""
	}
	s := buf.String()
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func (l *lifter) constAt(a int32) any {
	if a < 0 || int(a) >= len(l.ch.Consts) {
		return nil
	}
	return l.ch.Consts[a]
}

func (l *lifter) nameAt(a int32) string {
	if s, ok := l.constAt(a).(string); ok {
		return s
	}
	return ""
}

// liftFunc lifts one compiled function (a decl's body, or a funclit's
// synthesized decl) to a Body. `ch` is nil-checked by callers.
func (ctx *liftCtx) liftFunc(fd *ast.FuncDecl, ch *bytecode.Chunk, name string) *Body {
	if ch == nil {
		return nil
	}
	b := &Body{Name: name, NResults: ch.NResults, pkg: ctx.pkg, file: ctx.file, fset: ctx.fset}

	l := &lifter{
		liftCtx: ctx,
		ch:      ch,
		slots:   map[int]map[int]bool{},
		uv:      map[int]map[int]bool{},
		sym:     map[int]runtime.SymbolID{},
	}
	l.stack = []int{}

	// parameters occupy slots 0..NParams-1 — seeded value ids
	var params []*Param
	pi := 0
	addParam := func(name string, typ ast.Expr, recv bool) {
		p := &Param{Val: l.nextV, Slot: pi, Name: name, Recv: recv}
		l.nextV++
		if typ != nil {
			p.Type = NewTypeExpr(typ, ctx.file, ctx.pkg)
		}
		l.slots[pi] = map[int]bool{p.Val: true}
		params = append(params, p)
		pi++
	}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		rf := fd.Recv.List[0]
		if len(rf.Names) == 0 {
			addParam("$recv", rf.Type, true)
		} else {
			for _, n := range rf.Names {
				addParam(n.Name, rf.Type, true)
			}
		}
	}
	if fd.Type != nil && fd.Type.Params != nil {
		for _, f := range fd.Type.Params.List {
			if len(f.Names) == 0 {
				addParam(fmt.Sprintf("$arg%d", pi), f.Type, false)
				continue
			}
			for _, n := range f.Names {
				addParam(n.Name, f.Type, false)
			}
		}
	}
	// named result slots are declared after params — the emitted
	// OpNil/OpNewLocal stream binds them as ordinary locals.

	for i, ins := range ch.Code {
		l.step(i, ins)
	}
	b.Ops = boxAny(l.ops)
	b.Params = boxAny(params)
	return b
}

// step emulates one instruction on the symbolic stack and records its Op.
func (l *lifter) step(i int, ins bytecode.Instruction) {
	op := &Op{I: i, Code: opName(ins.Op), Out: -1, Slot: -1, Fun: -1}
	if l.fset != nil {
		op.Pos = l.fset.Position(ins.Pos).String()
	}
	finish := func(kind string, inIds []int, out int, nout int) {
		op.Kind = kind
		op.Ins = intSlice(inIds)
		op.Out = out
		op.NOut = nout
		if op.expr == nil {
			op.expr = pickExpr(l.exprs[ins.Pos], kind, op.Name)
		}
		op.Text = l.render(op.expr)
		l.ops = append(l.ops, op)
	}

	push := func() int { return l.push() }
	var x []int

	switch ins.Op {
	case bytecode.OpNop:
		finish("nop", nil, -1, 0)

	case bytecode.OpConst:
		out := push()
		c := l.constAt(ins.A)
		op.Lit = litText(c)
		kind := "lit"
		switch td := c.(type) {
		case *runtime.TypeDef:
			kind = "type"
			op.Tok = "typedef"
			op.Lit = ""
			if td.Anon != nil {
				op.Typ = NewTypeExpr(td.Anon, l.file, l.pkg)
			}
		case *runtime.Function:
			kind = "funcproto"
		}
		finish(kind, nil, out, 1)
		if kind == "type" && op.Typ == nil && op.expr != nil {
			op.Typ = NewTypeExpr(op.expr, l.file, l.pkg)
		}

	case bytecode.OpNil:
		op.Lit = "nil"
		finish("lit", nil, push(), 1)

	case bytecode.OpDup:
		t := l.pop()
		l.pushExisting(t)
		l.pushExisting(t)
		finish("move", []int{t}, -1, 0)

	case bytecode.OpSwap:
		b, a := l.pop(), l.pop()
		l.pushExisting(b)
		l.pushExisting(a)
		finish("move", []int{a, b}, -1, 0)

	case bytecode.OpRot3:
		c, b, a := l.pop(), l.pop(), l.pop()
		l.pushExisting(b)
		l.pushExisting(c)
		l.pushExisting(a)
		finish("move", []int{a, b, c}, -1, 0)

	case bytecode.OpPop:
		finish("pop", []int{l.pop()}, -1, 0)

	case bytecode.OpNewLocal:
		v := l.pop()
		l.bindSlot(int(ins.A), v)
		op.Slot = int(ins.A)
		if ins.B != 0 {
			op.Tok = "const"
		}
		finish("bind", []int{v}, -1, 0)

	case bytecode.OpRenewVar:
		op.Slot = int(ins.A)
		op.Tok = "renew"
		finish("bind", l.slotIns(int(ins.A)), -1, 0)

	case bytecode.OpLocal:
		op.Slot = int(ins.A)
		x = l.slotIns(int(ins.A))
		out := push()
		l.propagateSym(out, x)
		finish("ref", x, out, 1)

	case bytecode.OpSetLocal:
		v := l.pop()
		l.bindSlot(int(ins.A), v)
		op.Slot = int(ins.A)
		finish("set", []int{v}, -1, 0)

	case bytecode.OpLocalRef:
		op.Slot = int(ins.A)
		finish("cellref", l.slotIns(int(ins.A)), push(), 1)

	case bytecode.OpUpval:
		op.Slot = int(ins.A)
		op.Tok = "upval"
		x = l.upvalIns(int(ins.A))
		out := push()
		l.propagateSym(out, x)
		finish("ref", x, out, 1)

	case bytecode.OpSetUpval:
		v := l.pop()
		l.bindUpval(int(ins.A), v)
		op.Slot = int(ins.A)
		op.Tok = "upval"
		finish("set", []int{v}, -1, 0)

	case bytecode.OpGlobal:
		op.Name = l.nameAt(ins.A)
		op.Sym = l.resolveGlobal(op.Name)
		out := push()
		l.sym[out] = op.Sym
		finish("global", nil, out, 1)

	case bytecode.OpNewGlobal:
		v := l.pop()
		op.Name = l.nameAt(ins.A)
		if ins.B != 0 {
			op.Tok = "const"
		}
		finish("bind", []int{v}, -1, 0)

	case bytecode.OpSetGlobal:
		v := l.pop()
		op.Name = l.nameAt(ins.A)
		finish("set", []int{v}, -1, 0)

	case bytecode.OpGlobalRef:
		op.Name = l.nameAt(ins.A)
		finish("cellref", nil, push(), 1)

	case bytecode.OpSelect:
		b := l.pop()
		op.Name = l.nameAt(ins.A)
		out := push()
		if s, ok := l.sym[b]; ok && s.Name == "" {
			op.Sym = runtime.SymbolID{PackagePath: s.PackagePath, Name: op.Name}
			l.sym[out] = op.Sym
		} else {
			l.propagateSym(out, []int{b})
		}
		finish("sel", []int{b}, out, 1)

	case bytecode.OpSetField:
		v, b := l.pop(), l.pop()
		op.Name = l.nameAt(ins.A)
		finish("set", []int{b, v}, -1, 0)

	case bytecode.OpIndex:
		k, b := l.pop(), l.pop()
		finish("index", []int{b, k}, push(), 1)

	case bytecode.OpIndexOK:
		k, b := l.pop(), l.pop()
		op.Tok = "ok"
		finish("index", []int{b, k}, push(), 1)

	case bytecode.OpSetIndex:
		v, k, b := l.pop(), l.pop(), l.pop()
		finish("set", []int{b, k, v}, -1, 0)

	case bytecode.OpSlice:
		hi, lo, b := l.pop(), l.pop(), l.pop()
		op.Tok = "slice"
		finish("index", []int{b, lo, hi}, push(), 1)

	case bytecode.OpDeref:
		xx := l.pop()
		out := push()
		l.propagateSym(out, []int{xx})
		finish("deref", []int{xx}, out, 1)

	case bytecode.OpSetInd:
		v, c := l.pop(), l.pop()
		finish("set", []int{c, v}, -1, 0)

	case bytecode.OpBox:
		finish("cellref", []int{l.pop()}, push(), 1)

	case bytecode.OpCall, bytecode.OpDefer, bytecode.OpGo:
		n := int(ins.A)
		x = l.popN(n + 1) // [callee, arg0..argN-1]
		op.Fun = x[0]
		op.Args = intSlice(x[1:])
		if s, ok := l.sym[x[0]]; ok {
			op.Sym = s
		}
		if ins.B == 1 {
			op.Tok = "spread"
		}
		kind := "call"
		out := -1
		nout := 0
		switch ins.Op {
		case bytecode.OpCall:
			out = push()
			nout = 1
		case bytecode.OpDefer:
			kind = "defer"
		case bytecode.OpGo:
			kind = "go"
		}
		finish(kind, x, out, nout)

	case bytecode.OpPack:
		finish("pack", l.popN(int(ins.A)), push(), 1)

	case bytecode.OpUnpack:
		t := l.pop()
		n := int(ins.A)
		first := -1
		for k := 0; k < n; k++ {
			v := push()
			if k == 0 {
				first = v
			}
		}
		finish("unpack", []int{t}, first, n)

	case bytecode.OpMakeComposite:
		n := int(ins.A)
		if ins.B == 1 {
			n *= 2
		}
		x = l.popN(n)
		td := l.pop()
		finish("comp", append([]int{td}, x...), push(), 1)

	case bytecode.OpMakeClosure:
		out := push()
		if fn, ok := l.constAt(ins.A).(*runtime.Function); ok && fn.Chunk != nil && l.depth < 8 {
			sub := &liftCtx{
				pkg: l.pkg, file: l.file, fset: l.fset, scope: l.scope,
				exprs: l.exprs, depth: l.depth + 1,
			}
			if fn.Decl != nil {
				op.Body = sub.liftFunc(fn.Decl, fn.Chunk, fn.Name)
			}
		}
		finish("func", nil, out, 1)

	case bytecode.OpEvalAST:
		if frag, ok := l.constAt(ins.A).(*bytecode.ASTFragment); ok {
			op.expr = frag.Expr
		}
		finish("eval", nil, push(), 1)

	case bytecode.OpBinary:
		b, a := l.pop(), l.pop()
		op.Name = bytecode.BinOp(ins.A).String()
		finish("expr", []int{a, b}, push(), 1)

	case bytecode.OpUnary:
		a := l.pop()
		op.Name = bytecode.UnOp(ins.A).String()
		finish("expr", []int{a}, push(), 1)

	case bytecode.OpJump:
		op.Name = "jump"
		finish("ctrl", nil, -1, 0)

	case bytecode.OpJumpFalse:
		op.Name = "iffalse"
		finish("ctrl", []int{l.pop()}, -1, 0)

	case bytecode.OpJumpTrue:
		op.Name = "iftrue"
		finish("ctrl", []int{l.pop()}, -1, 0)

	case bytecode.OpIter:
		finish("iter", []int{l.pop()}, push(), 1)

	case bytecode.OpRangeNext:
		op.Slot = int(ins.B)
		x = l.slotIns(int(ins.B))
		n := int(ins.C)
		first := -1
		for k := 0; k < n; k++ {
			v := push()
			if k == 0 {
				first = v
			}
		}
		finish("range", x, first, n)

	case bytecode.OpSend:
		v, c := l.pop(), l.pop()
		op.Name = "send"
		finish("chan", []int{c, v}, -1, 0)

	case bytecode.OpRecv:
		c := l.pop()
		op.Name = "recv"
		finish("chan", []int{c}, push(), 1)

	case bytecode.OpRecvOK:
		c := l.pop()
		op.Name = "recvok"
		finish("chan", []int{c}, push(), 1)

	case bytecode.OpSelSend:
		v, c := l.pop(), l.pop()
		op.Name = "selsend"
		finish("chan", []int{c, v}, push(), 1)

	case bytecode.OpSelRecv:
		c := l.pop()
		op.Name = "selrecv"
		first := push()
		push()
		finish("chan", []int{c}, first, 2)

	case bytecode.OpPanic:
		finish("panic", []int{l.pop()}, -1, 0)

	case bytecode.OpTrap:
		op.Lit = litText(l.constAt(ins.A))
		finish("trap", nil, -1, 0)

	case bytecode.OpReturn:
		n := int(ins.A)
		if n < 0 {
			// bare return: results are the current values of the named slots
			var srcs []int
			for _, s := range l.ch.NamedSlots {
				srcs = append(srcs, l.slotIns(s)...)
			}
			op.Tok = "bare"
			finish("ret", srcs, -1, 0)
		} else {
			if i == len(l.ch.Code)-1 {
				op.Tok = "implicit"
			}
			finish("ret", l.popN(n), -1, 0)
		}

	case bytecode.OpDup2:
		b, a := l.pop(), l.pop()
		l.pushExisting(a)
		l.pushExisting(b)
		l.pushExisting(a)
		l.pushExisting(b)
		finish("move", []int{a, b}, -1, 0)

	case bytecode.OpFieldRef:
		b := l.pop()
		op.Name = l.nameAt(ins.A)
		finish("cellref", []int{b}, push(), 1)

	case bytecode.OpIndexRef:
		k, b := l.pop(), l.pop()
		finish("cellref", []int{b, k}, push(), 1)

	case bytecode.OpAssert:
		td, xx := l.pop(), l.pop()
		finish("assert", []int{xx, td}, push(), 1)

	case bytecode.OpAssertOK:
		td, xx := l.pop(), l.pop()
		op.Tok = "ok"
		finish("assert", []int{xx, td}, push(), 1)

	case bytecode.OpInstantiate:
		n := int(ins.A)
		x = l.popN(n)
		base := l.pop()
		finish("inst", append([]int{base}, x...), push(), 1)

	case bytecode.OpElemType:
		finish("type", []int{l.pop()}, push(), 1)

	case bytecode.OpCoerce:
		td := l.pop()
		op.Slot = int(ins.A)
		op.Typ = l.typOf(td)
		finish("coerce", []int{td}, -1, 0)

	case bytecode.OpCoerceTop:
		td, t := l.pop(), l.pop()
		out := push()
		l.propagateSym(out, []int{t})
		op.Tok = "top"
		op.Typ = l.typOf(td)
		finish("coerce", []int{t, td}, out, 1)

	case bytecode.OpCoerceN:
		x = l.popN(int(ins.A))
		t := l.pop()
		out := push()
		op.Tok = "n"
		if len(x) > 0 {
			op.Typ = l.typOf(x[0])
		}
		finish("coerce", append([]int{t}, x...), out, 1)

	case bytecode.OpCoerceGlobal:
		td := l.pop()
		op.Name = l.nameAt(ins.A)
		op.Typ = l.typOf(td)
		finish("coerce", []int{td}, -1, 0)

	case bytecode.OpSpecialCall:
		if sid, ok := l.constAt(ins.A).(runtime.SymbolID); ok {
			op.Sym = sid
		}
		if q, ok := l.constAt(ins.B).(*runtime.QuotedCall); ok {
			op.expr = q.Call
		}
		finish("special", nil, push(), 1)

	default:
		finish("expr", nil, -1, 0)
	}
}

func litText(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func intSlice(xs []int) *runtime.Slice {
	vs := make([]runtime.Value, len(xs))
	for i, v := range xs {
		vs[i] = int64(v)
	}
	return &runtime.Slice{Elems: vs}
}

func boxAny[T any](xs []T) *runtime.Slice {
	vs := make([]runtime.Value, len(xs))
	for i, x := range xs {
		vs[i] = &runtime.GoValue{V: x}
	}
	return &runtime.Slice{Elems: vs}
}

// opName renders a bytecode.Op for Op.Code.
func opName(o bytecode.Op) string {
	if int(o) < len(opNames) && opNames[o] != "" {
		return opNames[o]
	}
	return fmt.Sprintf("Op(%d)", int(o))
}

var opNames = map[bytecode.Op]string{
	bytecode.OpNop: "OpNop", bytecode.OpConst: "OpConst", bytecode.OpNil: "OpNil",
	bytecode.OpDup: "OpDup", bytecode.OpSwap: "OpSwap", bytecode.OpRot3: "OpRot3",
	bytecode.OpPop: "OpPop", bytecode.OpNewLocal: "OpNewLocal", bytecode.OpRenewVar: "OpRenewVar",
	bytecode.OpLocal: "OpLocal", bytecode.OpSetLocal: "OpSetLocal", bytecode.OpLocalRef: "OpLocalRef",
	bytecode.OpUpval: "OpUpval", bytecode.OpSetUpval: "OpSetUpval",
	bytecode.OpGlobal: "OpGlobal", bytecode.OpNewGlobal: "OpNewGlobal",
	bytecode.OpSetGlobal: "OpSetGlobal", bytecode.OpGlobalRef: "OpGlobalRef",
	bytecode.OpSelect: "OpSelect", bytecode.OpSetField: "OpSetField",
	bytecode.OpIndex: "OpIndex", bytecode.OpIndexOK: "OpIndexOK",
	bytecode.OpSetIndex: "OpSetIndex", bytecode.OpSlice: "OpSlice",
	bytecode.OpDeref: "OpDeref", bytecode.OpSetInd: "OpSetInd", bytecode.OpBox: "OpBox",
	bytecode.OpCall: "OpCall", bytecode.OpDefer: "OpDefer", bytecode.OpGo: "OpGo",
	bytecode.OpPack: "OpPack", bytecode.OpUnpack: "OpUnpack",
	bytecode.OpMakeComposite: "OpMakeComposite", bytecode.OpMakeClosure: "OpMakeClosure",
	bytecode.OpEvalAST: "OpEvalAST", bytecode.OpBinary: "OpBinary", bytecode.OpUnary: "OpUnary",
	bytecode.OpJump: "OpJump", bytecode.OpJumpFalse: "OpJumpFalse", bytecode.OpJumpTrue: "OpJumpTrue",
	bytecode.OpIter: "OpIter", bytecode.OpRangeNext: "OpRangeNext",
	bytecode.OpSend: "OpSend", bytecode.OpRecv: "OpRecv", bytecode.OpRecvOK: "OpRecvOK",
	bytecode.OpSelSend: "OpSelSend", bytecode.OpSelRecv: "OpSelRecv",
	bytecode.OpPanic: "OpPanic", bytecode.OpTrap: "OpTrap", bytecode.OpReturn: "OpReturn",
	bytecode.OpDup2: "OpDup2", bytecode.OpFieldRef: "OpFieldRef", bytecode.OpIndexRef: "OpIndexRef",
	bytecode.OpAssert: "OpAssert", bytecode.OpAssertOK: "OpAssertOK",
	bytecode.OpInstantiate: "OpInstantiate", bytecode.OpElemType: "OpElemType",
	bytecode.OpCoerce: "OpCoerce", bytecode.OpCoerceTop: "OpCoerceTop",
	bytecode.OpCoerceN: "OpCoerceN", bytecode.OpCoerceGlobal: "OpCoerceGlobal",
	bytecode.OpSpecialCall: "OpSpecialCall",
}
