// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// exec runs a frame to completion: the instruction loop first, then the
// frame's defers during unwind — mirroring Go, where deferred calls run on
// normal return and during panic unwinding alike. A script *Panic is
// catchable by recover() inside a deferred function; a *Trap bypasses
// recover and re-panics after defers drain. A raw host panic (a Go runtime
// error or a panic inside a builtin) is lifted into a script *Panic so it
// records frames and can be recovered, like Go's own runtime errors.
func (v *VM) exec(f *frame) {
	// profiler safepoints bracket every push and pop, so the code run
	// between two checks always has the same innermost frame: the caller
	// is charged up to the call, the callee up to its return.
	if v.H.Prof != nil {
		v.profCheck()
	}
	v.frames = append(v.frames, f)
	if v.H.Prof != nil {
		// the callee's setup allocations (its frame's cells, argbuf)
		// belong to its stack, not the caller's call line.
		v.profAllocFlush()
	}
	defer func() {
		r := recover()
		if v.H.Prof != nil {
			v.profCheck()
		}
		v.frames = v.frames[:len(v.frames)-1]
		if p, ok := asScriptPanic(r).(*runtime.Panic); ok && p != nil {
			// keep the panicking frame visible to Callers while the
			// unwind propagates — Go's traceback lists it until the
			// panic dies or a defer recovers it.
			v.unwinding = append(v.unwinding, unwoundFrame{f: f, pn: p})
		}
		v.unwind(f, asScriptPanic(r))
	}()
	v.loop(f)
}

func (v *VM) loop(f *frame) {
	code := f.ch.Code
	consts := f.ch.Consts
	for f.ip < len(code) && (f.boundHi == 0 || (f.ip > f.boundLo && f.ip < f.boundHi)) {
		// a nested call may have grown the arena into a new backing —
		// re-slice the view so bare f.stack reads/writes this
		// instruction see the live array. cap(v.vals) moves exactly
		// when the backing does, so a stale view is one compare.
		if f.valsCap != cap(v.vals) {
			f.refresh()
		}
		ins := code[f.ip]
		f.ip++
		switch ins.Op {
		case bytecode.OpNop:
		case bytecode.OpConst:
			f.push(consts[ins.A])
		case bytecode.OpNil:
			f.push(runtime.NIL)
		case bytecode.OpDup:
			f.push(f.stack[f.sp-1])
		case bytecode.OpDup2:
			n := f.sp
			a, b := f.stack[n-2], f.stack[n-1]
			f.push(a)
			f.push(b)
		case bytecode.OpFieldRef:
			base := f.pop()
			if ir, isImport := base.(*runtime.ImportRef); isImport && ins.B == 0 {
				// &pkg.V is the imported global's own storage cell,
				// like &v for a same-package global (OpGlobalRef).
				if c := v.importGlobalCell(f, ir, consts[ins.A].(string)); c != nil {
					f.push(c)
					break
				}
			}
			if ins.B == 0 || ins.B&2 != 0 {
				// Pinned operand (address-of target B=0, or a
				// multi-assign store B&2): a ref holding a struct is
				// the variable's storage (its address is stable), but
				// a pointer operand evaluates to its pointee —
				// snapshot it, so `fp := &p.f` and
				// `p, p.f = new(T), v` don't follow a reseated p. Any
				// ref kind pins the same way: `s.P, s.P.X` and
				// `a[0], a[0].X` resolve the stored pointer now.
				if gv, ok := runtime.Deref(base); ok {
					e := gv
					for {
						if n, isNamed := e.(*runtime.Named); isNamed {
							e = n.V
							continue
						}
						break
					}
					if _, isStruct := e.(*runtime.Struct); !isStruct {
						base = gv
					}
				}
			}
			if ins.B == 0 {
				// the nil check fires now; B=1 marks a store target, where
				// Go checks at store time so the RHS evaluates first.
				v.checkAddrBase(base, nil)
			}
			path, def := v.declaredFieldPath(f, base, consts[ins.A].(string))
			f.push(&runtime.FieldRef{Base: base, Name: consts[ins.A].(string), Def: def, Path: path})
		case bytecode.OpIndexRef:
			key := f.pop()
			base := f.pop()
			// the ref stores the resolved key: a still-untyped constant
			// must materialize like v.index's operand does (`mss["a"][0]`
			// keys a UConst 0 otherwise). A named key keeps its tag —
			// `type T pkg.S` unwraps to a struct whose typedef is pkg.S,
			// which the store's declared key check would reject.
			key = v.materialize(f, key)
			if ins.B == 2 {
				// a[i][:] — a ref keeps the slice bound to element
				// storage; a base with no slice elements (map
				// elements are unaddressable in Go, strings, call
				// results) yields the index value, which the slice
				// op copies like a plain element read.
				probe := &runtime.IndexRef{Base: base, Key: key}
				if probe.Slice() != nil {
					f.push(probe)
				} else {
					f.push(v.index(f, base, key))
				}
				break
			}
			if ins.B == 0 {
				v.checkAddrBase(base, key)
				v.checkIndexRefBounds(f, base, key)
			}
			if _, isMap := runtime.Unwrap(base).(*runtime.Map); isMap && ins.B == 0 {
				// Go rejects &m[k] at compile time: map elements are
				// not addressable — B=1 marks a multi-assign store
				// target, where the ref is legal (m[k] = v stores).
				f.trap("cannot take the address of map element")
			}
			f.push(&runtime.IndexRef{Base: base, Key: key})
		case bytecode.OpDerefRef:
			f.push(&runtime.DerefRef{Ptr: f.pop()})
		case bytecode.OpNilPtrCheck:
			base := f.pop()
			v.checkAddrBase(base, nil)
			f.push(base)
		case bytecode.OpSwap:
			n := f.sp
			f.stack[n-1], f.stack[n-2] = f.stack[n-2], f.stack[n-1]
		case bytecode.OpRot3:
			n := f.sp
			f.stack[n-3], f.stack[n-2], f.stack[n-1] = f.stack[n-2], f.stack[n-1], f.stack[n-3]
		case bytecode.OpPop:
			f.pop()
		case bytecode.OpNewLocal:
			x := f.pop()
			if ins.B == 0 {
				// a var bind fixes the value's type: an untyped constant
				// materializes its default here (`x := 'a'` is a rune, and
				// `x := 1<<100` fails like a Go compile error). Const cells
				// keep it lazy — an unused `const B = 1<<100` is legal.
				x = v.materialize(f, x)
			}
			if ins.B != 0 {
				// a const binding always needs the ReadOnly flag — it
				// keeps a cell even on a plain slot.
				f.setLocalCell(int(ins.A), &runtime.Cell{Elem: valueCopy(x), ReadOnly: true})
			} else if c, boxed := f.boxedCell(int(ins.A)); boxed {
				// a fresh cell per bind: an earlier captured ref keeps
				// pointing at the previous value, as Go's variable
				// identity requires. An unpublished cell has no such
				// observer — overwrite it in place.
				if f.cellsLeaked || f.cellsShared {
					f.setLocalCell(int(ins.A), &runtime.Cell{Elem: valueCopy(x)})
				} else {
					*c = runtime.Cell{Elem: valueCopy(x)}
				}
			} else {
				f.locals[ins.A] = valueCopy(x)
			}
		case bytecode.OpRenewVar:
			if c, boxed := f.boxedCell(int(ins.A)); boxed {
				// a renewed binding rebinds the variable identity — a
				// captured ref must keep the old cell. Unpublished
				// cells have no observer and renew in place.
				if f.cellsLeaked || f.cellsShared {
					f.setLocalCell(int(ins.A), &runtime.Cell{Elem: c.Elem})
				}
			}
			f.ltyps[ins.A] = nil
		case bytecode.OpLocal:
			f.push(f.localVal(int(ins.A)))
		case bytecode.OpLocalTyp:
			if td := f.localTyp(int(ins.A)); td != nil {
				f.push(td)
			} else {
				f.push(runtime.NIL)
			}
		case bytecode.OpSetLocal:
			v.assignLocal(f, int(ins.A), f.pop())
		case bytecode.OpLocalRef:
			if ins.B == 0 {
				// a store-target ref is consumed transiently by the
				// store — its promoted cell cannot be published, so a
				// pooled block cell serves when the chunk cannot leak.
				f.push(f.localCellTransient(int(ins.A)))
			} else {
				f.push(f.localCell(int(ins.A)))
			}
		case bytecode.OpUpval:
			f.push(f.upvals[ins.A].Elem)
		case bytecode.OpUpvalTyp:
			if td := f.upvals[ins.A].Typ; td != nil {
				f.push(td)
			} else {
				f.push(runtime.NIL)
			}
		case bytecode.OpSetUpval:
			v.assignCell(f, f.upvals[ins.A], f.pop())
		case bytecode.OpUpvalRef:
			f.push(f.upvals[ins.A])
		case bytecode.OpGlobal:
			if ins.C >= 0 && int(ins.C) < len(f.ch.Sites) {
				site := f.ch.Sites[ins.C]
				if s, _ := site.Cache.Load().(*globalSlot); s != nil &&
					s.env == f.fn.Pkg.Globals && s.gen == s.env.Gen() {
					if s.cell != nil {
						f.push(s.cell.Elem)
					} else {
						f.push(s.val)
					}
					break
				}
				f.push(v.resolveGlobalSite(f, consts[ins.A].(string), site))
				break
			}
			f.push(v.resolveGlobal(f, consts[ins.A].(string)))
		case bytecode.OpGlobalTyp:
			if old, ok := f.fn.Pkg.Globals.Get(consts[ins.A].(string)); ok {
				if c, isCell := old.(*runtime.Cell); isCell && c.Typ != nil {
					f.push(c.Typ)
					break
				}
			}
			f.push(runtime.NIL)
		case bytecode.OpNewGlobal:
			x := f.pop()
			if ins.B == 0 {
				x = v.materialize(f, x)
			}
			c := &runtime.Cell{Elem: valueCopy(x), ReadOnly: ins.B != 0}
			f.fn.Pkg.Globals.Set(consts[ins.A].(string), c)
		case bytecode.OpSetGlobal:
			name := consts[ins.A].(string)
			val := f.pop()
			if old, ok := f.fn.Pkg.Globals.Get(name); ok {
				if c, isCell := old.(*runtime.Cell); isCell {
					if c.ReadOnly {
						f.trap("cannot assign to %s", name)
					}
					v.assignCell(f, c, val)
					break
				}
			}
			f.fn.Pkg.Globals.Set(name, valueCopy(val))
		case bytecode.OpGlobalRef:
			name := consts[ins.A].(string)
			if old, ok := f.fn.Pkg.Globals.Get(name); ok {
				if c, isCell := old.(*runtime.Cell); isCell {
					f.push(c)
					break
				}
			}
			f.trap("cannot take address of %s", name)
		case bytecode.OpSelect:
			base := f.pop()
			f.push(v.selectMember(f, base, consts[ins.A].(string), v.selSite(f, ins)))
		case bytecode.OpSelectCall:
			base := f.pop()
			res := v.selectMember(f, base, consts[ins.A].(string), v.selSite(f, ins))
			if bm, ok := res.(*runtime.BoundMethod); ok {
				bm.Direct = true
			}
			f.push(res)
		case bytecode.OpSetField:
			val := f.pop()
			base := f.pop()
			v.setField(f, base, consts[ins.A].(string), val)
		case bytecode.OpIndex:
			idx := f.pop()
			base := f.pop()
			if ins.B == 1 {
				f.push(v.indexAlias(f, base, idx))
			} else {
				f.push(v.index(f, base, idx))
			}
		case bytecode.OpIndexOK:
			idx := f.pop()
			base := f.pop()
			f.push(v.indexOK(f, base, idx))
		case bytecode.OpSetIndex:
			val := f.pop()
			idx := f.pop()
			base := f.pop()
			v.setIndex(f, base, idx, val)
		case bytecode.OpSlice:
			var max runtime.Value = runtime.NIL
			if ins.B == 1 {
				max = f.pop()
			}
			hi := f.pop()
			lo := f.pop()
			base := f.pop()
			f.push(v.slice(f, base, lo, hi, max))
		case bytecode.OpDeref:
			x := f.pop()
			if ir, isIR := x.(*runtime.IndexRef); isIR {
				// a ref's element read goes through the full index path —
				// map bases, nil-map zero values, and live cell bases all
				// resolve there (IndexRef.Get only knows slices).
				f.push(v.index(f, ir.Base, ir.Key))
			} else if fr, isFR := x.(*runtime.FieldRef); isFR {
				// reading a field ref is the member select on its base —
				// a nil pointer still panics and host/promoted fields
				// resolve like a plain `x.f` read (FieldRef.Get only
				// knows plain struct storage).
				f.push(v.selectMember(f, fr.Base, fr.Name, v.selSite(f, ins)))
			} else if td, ok := x.(*runtime.TypeDef); ok {
				// `(*T)` in a method-expression position evaluates to the
				// pointer typedef so selectMember can bind pointer methods.
				// Elem keeps the dereferenced typedef — an instantiation
				// (`(*G[string])`) needs its bound methods and its exact
				// pointee for the receiver coerce, which a `*G` spelling
				// alone loses.
				f.push(&runtime.TypeDef{Kind: runtime.KindPointer, Anon: &ast.StarExpr{X: instExprFor(td)}, Elem: td, Pkg: td.Pkg, File: td.File})
			} else if dv, ok := runtime.DerefValue(x); ok {
				// a pointer value typed *Declared dereferences to the
				// declared pointee type — `*s` on `(*T)(p)` reads as T,
				// so method calls and `:=`-inferred vars keep the tag.
				if n, isN := x.(*runtime.Named); isN && n.Typ != nil && n.Typ.Kind == runtime.KindPointer {
					if v.unsafePtrTd(n.Typ) {
						// gc rejects `*u` ("cannot indirect u") —
						// converting back to *T is the only dereference.
						f.trap("cannot indirect %s", tdName(n.Typ))
					}
					if et := v.elemTypedef(f, n.Typ); et != nil {
						// A converted pointer re-types the storage on
						// read: `(*uval)(&u)` reads the uint as uval.
						// The conversion already proved the pointee
						// shapes identical, so re-tag rather than coerce
						// — assignability would reject two named types.
						if dtd := v.typeOfValue(dv); dtd != nil && tagIsNamed(et) && v.tdShapeEq(dtd, et) {
							dv = runtime.Tag(et, dv) // Tag unwraps internally
						} else {
							dv = v.coerce(f, dv, et)
						}
					}
				}
				f.push(dv)
			} else if tn, ok := asTypedNil(x); ok {
				// `*u` on a nil unsafe.Pointer is still "cannot
				// indirect" — gc rejects the indirection before any
				// nil check.
				if v.unsafePtrTd(tn.Typ) {
					f.trap("cannot indirect %s", tdName(tn.Typ))
				}
				// *p on a nil pointer panics; on other nilables it's invalid
				if tn.Typ.Kind == runtime.KindPointer {
					panic(runtime.NilDerefPanic())
				}
				f.trap("deref of non-pointer %T", x)
			} else if dr, isDR := x.(*runtime.DerefRef); isDR {
				// `*p` through a ref on a nil pointer panics like the
				// plain operand read — Get just reports failure.
				if pv, ok := runtime.Deref(dr.Ptr); ok {
					if tn, isTN := asTypedNil(pv); isTN {
						if v.unsafePtrTd(tn.Typ) {
							f.trap("cannot indirect %s", tdName(tn.Typ))
						}
						if tn.Typ.Kind == runtime.KindPointer {
							panic(runtime.NilDerefPanic())
						}
					}
					// `*p += 1` after `p := &ip[1]` on a boxed host
					// slice: the element ref only resolves through the
					// VM's own reads, as the plain IndexRef read above.
					if ev, ok := v.readElemRef(f, pv); ok {
						f.push(ev)
						break
					}
				}
				f.trap("deref of non-pointer %T", x)
			} else {
				if gv, isGV := x.(*runtime.GoValue); isGV {
					// a nil host pointer panics like a nil script one.
					if rv := hostbridge.RVOf(gv.V); rv.IsValid() && rv.Kind() == hostbridge.KindPtr {
						panic(runtime.NilDerefPanic())
					}
				}
				f.trap("deref of non-pointer %T", x)
			}
		case bytecode.OpSetInd:
			val := f.pop()
			ref := f.pop()
			v.setIndirect(f, ref, val)
		case bytecode.OpSetRefs:
			// multi-assign phase 2: every LHS ref was resolved before the
			// RHS evaluated (Go spec); stores land left-to-right.
			n := int(ins.A)
			vals := make([]runtime.Value, n)
			refs := make([]runtime.Value, n)
			v.profAllocs += 2
			if ins.B == 1 {
				// range-assign order: the iter pair is already pushed,
				// the LHS refs (evaluated with pre-iteration operands)
				// sit on top.
				for i := n - 1; i >= 0; i-- {
					refs[i] = f.pop()
				}
				for i := n - 1; i >= 0; i-- {
					vals[i] = f.pop()
				}
			} else {
				for i := n - 1; i >= 0; i-- {
					vals[i] = f.pop()
				}
				for i := n - 1; i >= 0; i-- {
					refs[i] = f.pop()
				}
			}
			for i := 0; i < n; i++ {
				v.assignRef(f, refs[i], vals[i])
			}
		case bytecode.OpAssert:
			tdv := f.pop()
			var static runtime.Value = runtime.NIL
			if ins.B == 1 {
				static = f.pop()
			}
			x := f.pop()
			f.push(v.typeAssert(f, x, tdv, static, ins.Pos))
		case bytecode.OpAssertOK:
			tdv := f.pop()
			x := f.pop()
			f.push(v.typeAssertOK(f, x, tdv))
		case bytecode.OpInstantiate:
			ntargs := int(ins.A)
			targs := make([]runtime.Value, ntargs)
			v.profAllocs++
			for i := ntargs - 1; i >= 0; i-- {
				targs[i] = f.pop()
			}
			base := f.pop()
			f.push(v.instantiate(f, base, targs, ins.Pos, v.selSite(f, ins)))
		case bytecode.OpLocalType:
			// A function-local type decl runs per call; inside an
			// instantiated generic function — including a literal that
			// closes over one — the enclosing type args belong to the
			// declared type's identity (`type X int` in F[T] differs per
			// instantiation). specializeType computes the same outer
			// args for a local generic's T[args]. The same wrap runs
			// after every compiled type expression (typeExpr), giving
			// any typedef OuterSpell — the display context embedded
			// local-type names spell with (`func(main.U[int;int]·3)`).
			// Resolved binds and OuterArgs stay Local-only: named
			// identity compares Binds, so folding them onto a
			// package-level typedef would invent a different type.
			top := f.pop()
			if td, ok := top.(*runtime.TypeDef); ok {
				top = v.outerTypedef(f, td)
			}
			f.push(top)
		case bytecode.OpFoldArrayLen:
			// [td, len] on the stack: evaluate-at-use constants like
			// `[n]int`/`[len(a)]*T` fold so type identity spells the
			// concrete `[3]*T` like Go. A is the const index of the
			// *ast.ArrayType whose length this value is — the compiler
			// emitted the eval for exactly that node. The fold is
			// published beside the AST (runtime.FoldArrayLen), never
			// written into it: the AST is shared by every goroutine.
			lv := f.pop()
			td := f.pop().(*runtime.TypeDef)
			if n, ok := lenConstInt(lv); ok {
				if at, ok := consts[ins.A].(*ast.ArrayType); ok {
					runtime.FoldArrayLen(td.Pkg, at, n)
				}
			}
			f.push(td)
		case bytecode.OpElemType:
			td := typedefOf(f.pop())
			if td == nil {
				f.trap("element-type source is not a type")
			}
			if v.H.ElemOf == nil {
				f.trap("element types require engine hooks")
			}
			et, err := v.H.ElemOf(td)
			if err != nil || et == nil {
				// the element may be named by a function-local `type` decl —
				// the package index cannot see it, but its typedef sits in
				// this frame's locals like any other local constant.
				if lt := v.localElemTypedef(f, td); lt != nil {
					et, err = lt, nil
				}
			}
			if err != nil {
				f.trapErr(err)
			}
			if et == nil {
				f.trap("cannot resolve element type of %s", tdName(td))
			}
			f.push(et)
		case bytecode.OpElemTypeOrNil:
			// fold-probe variant: an unresolvable element type is a
			// miss (NIL), never a trap — the caller falls back to the
			// ordinary evaluation path.
			if et := v.elemTypedef(f, typedefOf(f.pop())); et != nil {
				f.push(et)
			} else {
				f.push(runtime.NIL)
			}
		case bytecode.OpCoerce:
			td := typedefOf(f.pop())
			if td == nil {
				f.trap("declared type is not a type")
			}
			if f.vpack != nil && f.localVal(int(ins.A)) == runtime.Value(f.vpack) {
				v.coerceVarPack(f, f.vpack, td)
				f.vpack = nil
			}
			if c, boxed := f.boxedCell(int(ins.A)); boxed {
				c.Elem = v.coerce(f, c.Elem, td)
				c.Typ = td // future stores into this cell coerce the same way
			} else {
				f.locals[ins.A] = v.coerce(f, f.locals[ins.A], td)
				f.ltyps[ins.A] = td
			}
		case bytecode.OpCoerceTop:
			td := typedefOf(f.pop())
			if td == nil {
				f.trap("declared type is not a type")
			}
			f.stkSet(f.sp-1, v.coerce(f, f.stack[f.sp-1], td))
		case bytecode.OpCoerceN:
			// pop A typedefs then one value: a *Tuple coerces element-wise
			// (multi-value return), anything else against the first type
			cnt := int(ins.A)
			tds := make([]*runtime.TypeDef, cnt)
			for i := cnt - 1; i >= 0; i-- {
				tds[i] = typedefOf(f.pop())
			}
			x := f.pop()
			if tp, ok := x.(*runtime.Tuple); ok {
				elems := make([]runtime.Value, len(tp.Elems))
				v.profAllocs++
				for i, e := range tp.Elems {
					var td *runtime.TypeDef
					if i < len(tds) {
						td = tds[i]
					}
					elems[i] = v.coerce(f, e, td)
				}
				f.push(&runtime.Tuple{Elems: elems})
			} else {
				f.push(v.coerce(f, x, tds[0]))
			}
		case bytecode.OpCoerceGlobal:
			td := typedefOf(f.pop())
			if td == nil {
				f.trap("declared type is not a type")
			}
			name := consts[ins.A].(string)
			if gv, ok := f.fn.Pkg.Globals.Get(name); ok {
				if c, isCell := gv.(*runtime.Cell); isCell {
					c.Elem = v.coerce(f, c.Elem, td)
					c.Typ = td
				}
			}
		case bytecode.OpSpecialCall:
			// the site's Sym/Quote index the sym id and the quoted
			// call in Consts (P4-4a).
			ssite := f.ch.CallSites[ins.C]
			sym := consts[ssite.Sym].(runtime.SymbolID)
			q := consts[ssite.Quote].(*runtime.QuotedCall)
			if v.H.Special == nil {
				f.trap("OpSpecialCall: no special-form registry")
			}
			h, ok := v.H.Special(sym)
			if !ok {
				f.trap("unregistered special form %s.%s", sym.PackagePath, sym.Name)
			}
			res, err := h(&specialCtx{v: v, f: f, q: q}, q)
			if v.H.Prof != nil {
				v.profCheck()
			}
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			f.push(res)
		case bytecode.OpBox:
			x := f.pop()
			// the box cell takes the pointee's declared tag so `*p = v`
			// coerces like a store into `var p T`.
			f.push(&runtime.Cell{Elem: x, Typ: declaredTag(x)})
		case bytecode.OpCall:
			// arg statics live on the call site's ArgHint record
			// (P4-4a) — generic inference resolves them lazily.
			site := f.ch.CallSites[ins.C]
			var args []runtime.Value
			var spreadTd *runtime.TypeDef
			var fn runtime.Value
			var r runtime.Value
			var err error
			if int(ins.B) == 0 {
				// P4-3b: args stay in place — drop the callee slot,
				// and the top argc operands become the callee's
				// operand window; no argbuf copy.
				argc := int(ins.A)
				fn = f.stack[f.sp-argc-1]
				f.sp -= argc + 1
				f.stack = f.stack[:f.sp]
				// vals keeps the args — the callee's window covers
				// them; the next push drops the dead callee slot too.
				r, err = v.callInPlace(f, fn, argc, site.Args, site)
			} else {
				var hints []bytecode.ArgHint
				args, hints, spreadTd = v.popArgsCallSite(f, int(ins.A), int(ins.B), ins.Pos, site)
				fn = f.pop()
				r, err = v.callHints(f, fn, args, hints, spreadTd, site)
			}
			if v.H.Prof != nil {
				v.profCheck() // a long host call lands on its call line
			}
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			f.push(r)
		case bytecode.OpDefer:
			// callee + args are evaluated now (Go semantics); the call itself
			// runs at frame teardown, LIFO. The site's arg recipes resolve
			// at registration — the calling frame they read is gone by the
			// time the deferred call runs.
			site := f.ch.CallSites[ins.C]
			args, hints, spreadTd := v.popArgsCallSite(f, int(ins.A), int(ins.B), ins.Pos, site)
			fn := f.pop()
			var statics []*runtime.TypeDef
			if len(hints) > 0 {
				statics = v.resolveArgHints(f, hints)
			}
			f.defers = append(f.defers, deferredCall{fn: fn, args: args, pos: ins.Pos, typs: callTyps{statics: statics, spreadTd: spreadTd}})
		case bytecode.OpGo:
			// `go f(x)` spawns a real goroutine in this process: callee and
			// args are evaluated now; the call runs concurrently. Like
			// OpDefer, the site's recipes resolve against this frame — the
			// spawned frame cannot read them.
			site := f.ch.CallSites[ins.C]
			args, hints, spreadTd := v.popArgsCallSite(f, int(ins.A), int(ins.B), ins.Pos, site)
			fn := f.pop()
			var statics []*runtime.TypeDef
			if len(hints) > 0 {
				statics = v.resolveArgHints(f, hints)
			}
			v.spawn(fn, args, statics, spreadTd, true)
		case bytecode.OpEvalFragment:
			frag := consts[ins.A].(*bytecode.ASTFragment)
			if v.H.CompileExpr == nil {
				f.trap("OpEvalFragment: no CompileExpr hook")
			}
			ch, _ := frag.Cache.Load().(*bytecode.Chunk)
			if ch == nil {
				var err error
				ch, err = v.H.CompileExpr(f.fn.Pkg, frag.File, frag.Expr)
				if err != nil {
					panic(trapErr(ins.Pos, err))
				}
				frag.Cache.Store(ch)
			}
			r, err := v.call(&runtime.Function{Pkg: f.fn.Pkg, File: frag.File, Name: "<eval>", Chunk: ch}, nil, nil, nil)
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			f.push(r)
		case bytecode.OpDeferredTyp:
			// one deferred fact: resolve the site's type expression on
			// first execution (the engine answers under AllowIndex),
			// then apply the element/key peels the site spells — an
			// elided literal under a deferred container wants the
			// element's typedef, not the container's. Missing and
			// Failed both trap at the instruction's Pos.
			td, err := v.deferredLitTyp(f, f.ch.CallSites[ins.A])
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			f.push(td)
		case bytecode.OpDeferredValueTyp:
			// the value-typedef question OpTypGate asks — here pushed
			// as a value so unsafe.Sizeof/Alignof can size a deferred
			// operand's type without evaluating it.
			td, _, err := v.deferredValueTyp(f, f.ch.CallSites[ins.A])
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			f.push(td)
		case bytecode.OpJumpIfTypedef:
			x := f.pop()
			if c, ok := x.(*runtime.Cell); ok {
				x = c.Elem
			}
			if _, ok := x.(*runtime.TypeDef); ok {
				f.ip = int(ins.A)
			}
		case bytecode.OpLitKey:
			// a deferred literal key: the resolved literal type decides
			// whether the ident is a key expression (map/array — the
			// emitted expression instructions run next) or a field name
			// (push its const and jump over them).
			if !v.litKeyExprKey(f, f.ch.CallSites[ins.B]) {
				f.push(consts[ins.C])
				f.ip = int(ins.A)
			}
		case bytecode.OpConstExpr:
			// a deferred const-expr verdict: the site's expression
			// constant → the constant-emission variant follows; not
			// constant → jump to the non-constant variant.
			cnst, err := v.deferredConst(f, f.ch.CallSites[ins.B])
			if err != nil {
				panic(trapErr(ins.Pos, err))
			}
			if !cnst {
				f.ip = int(ins.A)
			}
		case bytecode.OpTypGate:
			// a deferred compare gate: the two sites resolve the
			// operands' typedefs; two distinct named types trap gc's
			// mismatched-types wording, anything else runs on.
			v.typGate(f, f.ch.CallSites[ins.A], f.ch.CallSites[ins.B], int(ins.C))
		case bytecode.OpPack:
			n := int(ins.A)
			el := make([]runtime.Value, n)
			for i := n - 1; i >= 0; i-- {
				el[i] = f.pop()
			}
			v.profAllocs++
			f.push(&runtime.Tuple{Elems: el})
		case bytecode.OpRejectTuple:
			// gc's `multiple-value f() in single-value context`: a
			// result tuple may sit on the stack only when it is the
			// whole argument list — the compiler emits this check
			// after every call used in any other position.
			if _, isTup := f.stack[f.sp-1].(*runtime.Tuple); isTup {
				f.trap("multiple-value call in single-value context")
			}
		case bytecode.OpUnpack:
			tv := f.pop()
			t, ok := tv.(*runtime.Tuple)
			if !ok {
				// single value to N targets: error unless N==1
				if ins.A == 1 {
					f.push(tv)
					break
				}
				f.trap("multi-assign from non-tuple %T", tv)
			}
			if len(t.Elems) != int(ins.A) {
				f.trap("unpack mismatch: %d values to %d names", len(t.Elems), ins.A)
			}
			for _, e := range t.Elems {
				f.push(e)
			}
		case bytecode.OpMakeComposite:
			f.push(v.makeComposite(f, ins))
		case bytecode.OpMakeClosure:
			proto := consts[ins.A].(*runtime.Function)
			if len(f.fn.Binds) > 0 {
				// a literal inside a generic function closes over the
				// instantiation's binds — `func(fn func(R) bool)` in
				// Pipe[int, int] types `func(func(int) bool)`. The
				// proto const is shared across instantiations, so the
				// binds attach to a copy, never to it.
				binds := map[string]runtime.Value{}
				for k, bv := range f.fn.Binds {
					binds[k] = bv
				}
				for k, bv := range proto.Binds {
					binds[k] = bv
				}
				proto = proto.WithBinds(binds)
				// The literal's own TParams stay empty (or its own), so
				// record which enclosing params the merged binds belong
				// to — OpLocalType/specializeType order outer args by it.
				proto.OuterTParams = f.fn.OuterParamNames()
			}
			if len(proto.Chunk.Upvals) == 0 {
				// a capture-free literal evaluates to the proto itself —
				// Go hoists it to a static func value, so repeated evals
				// share one object instead of allocating a Closure.
				f.push(proto)
				continue
			}
			cl := &runtime.Closure{Fn: proto}
			v.profAllocs++
			for _, d := range proto.Chunk.Upvals {
				if d.FromParentUpval {
					cl.Upvals = append(cl.Upvals, f.upvals[d.Index])
				} else {
					cl.Upvals = append(cl.Upvals, f.localCell(int(d.Index)))
				}
			}
			f.push(cl)
		case bytecode.OpBinary:
			b := f.pop()
			a := f.pop()
			f.push(v.binaryOpFlags(f, bytecode.BinOp(ins.A), a, b, int(ins.B)))
		case bytecode.OpUnary:
			a := f.pop()
			f.push(v.unaryOp(f, bytecode.UnOp(ins.A), a))
		case bytecode.OpJump:
			if int(ins.A) < f.ip {
				v.backEdge()
			}
			f.ip = int(ins.A)
		case bytecode.OpJumpFalse:
			if !truthy(f.pop()) {
				f.ip = int(ins.A)
			}
		case bytecode.OpJumpTrue:
			if truthy(f.pop()) {
				f.ip = int(ins.A)
			}
		case bytecode.OpLenIdxFold:
			// len(x[i]) / cap(x[i]): when x's element typedef is an
			// array the fold is a constant — Go never evaluates the
			// index, so skip the emitted index+index-op+call run.
			// The stack is [callee, base]; the fold collapses both
			// into the result so no call happens.
			base := f.stack[f.sp-1]
			if n, ok := v.lenIdxFold(f, base); ok {
				f.setSp(f.sp - 2)
				f.push(n)
				f.ip = int(ins.A)
			}
		case bytecode.OpLenDerefFold:
			// len(*p) / cap(*p): the call is a constant when p's type
			// is *[N]T — Go never evaluates the dereference. The stack
			// is [callee, probe]: a hit collapses both into N and skips
			// the emitted operand/deref/call run. B=0 probes the
			// operand's declared typedef (a miss pops it — the emitted
			// pointer expr still has to evaluate); B=1 probes the
			// evaluated pointer itself (a miss keeps it for OpDeref).
			top := f.stack[f.sp-1]
			if n, ok := v.lenDerefFold(f, top); ok {
				f.setSp(f.sp - 2)
				f.push(n)
				f.ip = int(ins.A)
			} else if ins.B == 0 {
				f.setSp(f.sp - 1)
			}
		case bytecode.OpLenCallFold:
			// len(x)/cap(x): the stack is [callee, operand]; when the
			// callee still is the predeclared builtin, call it straight
			// — the operand's own slot is the one-argument list — and
			// skip the emitted static+OpCall run. A rebound name falls
			// through to the ordinary call, and so does a lone call's
			// Tuple operand — the call may spread multi-results into
			// the builtin's params instead.
			n := f.sp
			if bf, ok := f.stack[n-2].(*runtime.BuiltinFunc); ok && bf == v.lenCapBuiltin(ins.B) {
				if _, tup := f.stack[n-1].(*runtime.Tuple); !tup {
					// keep OpCall's procExit safepoint: without the
					// check a goroutine looping on len/cap in a dead
					// process would spin instead of dying.
					if p := v.proc.Load(); p != nil {
						select {
						case <-p.Done():
							panic(procExit{})
						default:
						}
					}
					r, err := v.callBuiltin(bf, f.stack[n-1:n])
					if v.H.Prof != nil {
						v.profCheck()
					}
					if err != nil {
						panic(trapErr(ins.Pos, err))
					}
					f.setSp(n - 2)
					f.push(r)
					f.ip = int(ins.A)
				}
			}
		case bytecode.OpIter:
			coll := v.materialize(f, f.pop())
			if ins.B != 0 {
				// operand was `*x`: the pointer is the rangeable — a nil
				// *[N]T still yields indices, while `*x` on any other
				// nil pointer dereferences eagerly like OpDeref.
				if tn, ok := asTypedNil(coll); ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer && runtime.PtrArrayType(tn.Typ) == nil {
					panic(runtime.NilDerefPanic())
				}
			}
			f.push(v.newIterator(f, coll))
		case bytecode.OpRangeNext:
			it := f.localVal(int(ins.B)).(*runtime.Iterator)
			if it.Kind == 'f' {
				// Range over a function inverts control: the producer calls
				// yield, and each yield runs the loop body bounded to this
				// loop's (top, end) instruction range. top is this
				// instruction's index — f.ip already advanced past it.
				top := f.ip - 1
				v.driveFuncIter(f, it, int(ins.C)&3, top, int(ins.A))
				// The producer and every iteration already ran; f.ip sits
				// where the body last stopped:
				//   [top, end]  loop is done — take the exit jump
				//   < top       a goto left the loop backward — keep target
				//   > end       a goto forward or OpReturn — keep target
				if f.ip >= top && f.ip <= int(ins.A) {
					f.ip = int(ins.A)
				}
			} else if !v.iterNext(f, it, int(ins.C)&3, ins.C&4 != 0) {
				freeIter(it)
				f.ip = int(ins.A)
			}
		case bytecode.OpSend:
			val := f.pop()
			chv := f.pop()
			chRV, et, w := hostbridge.ChanOf(v.chanHooks(f), chv)
			if et != nil {
				val = v.coerce(f, val, et)
			} else {
				val = v.materialize(f, val)
			}
			sv, err := marshalHost.ChanSendValue(v, val, chRV.Type().Elem())
			if err != nil {
				f.trapErr(fmt.Errorf("cannot send on %s: %w", chRV.Type(), err))
			}
			hostbridge.ChanSend(v.chanHooks(f), chRV, sv, w)
		case bytecode.OpRecv:
			val, _ := func() (runtime.Value, bool) {
				chRV2, et2, w2 := hostbridge.ChanOf(v.chanHooks(f), f.pop())
				return hostbridge.ChanRecvRV(v.chanHooks(f), chRV2, et2, w2)
			}()
			f.push(val)
		case bytecode.OpRecvOK:
			val, ok := func() (runtime.Value, bool) {
				chRV2, et2, w2 := hostbridge.ChanOf(v.chanHooks(f), f.pop())
				return hostbridge.ChanRecvRV(v.chanHooks(f), chRV2, et2, w2)
			}()
			f.push(&runtime.Tuple{Elems: []runtime.Value{val, ok}})
		case bytecode.OpSelArm:
			if ins.B == 1 {
				val := f.pop()
				chv := f.pop()
				chRV, et, w := hostbridge.ChanOf(v.chanHooks(f), chv)
				if et != nil {
					val = v.coerce(f, val, et)
				} else {
					val = v.materialize(f, val)
				}
				sv, err := marshalHost.ChanSendValue(v, val, chRV.Type().Elem())
				if err != nil {
					f.trapErr(fmt.Errorf("cannot send on %s: %w", chRV.Type(), err))
				}
				f.push(&runtime.SelArm{Send: true, Wakeable: w.Misc, WakeChan: w.ChanPtr(), Case: hostbridge.SelectCaseSend(chRV, sv)})
			} else {
				chv := f.pop()
				chRV, et, w := hostbridge.ChanOf(v.chanHooks(f), chv)
				f.push(&runtime.SelArm{NRecv: int(ins.A), ETyp: et, Wakeable: w.Misc, WakeChan: w.ChanPtr(), Case: hostbridge.SelectCaseRecv(chRV)})
			}
		case bytecode.OpSelWait:
			// arms were pushed in source order; the jump table follows this
			// instruction: one OpJump per case, then the default's OpJump
			n := int(ins.A)
			arms := make([]*runtime.SelArm, n)
			for i := n - 1; i >= 0; i-- {
				a, ok := f.pop().(*runtime.SelArm)
				if !ok {
					f.trap("select arm is %T", f.stack[f.sp-1])
				}
				arms[i] = a
			}
			table := f.ip
			cases := make([]hostbridge.SelectCase, 0, n+2)
			for _, a := range arms {
				cases = append(cases, a.Case)
			}
			doneIdx := len(cases)
			cases = append(cases, hostbridge.SelectCaseRecv(hostbridge.RVOf(v.doneChan())))
			defIdx := -1
			if ins.B == 1 {
				defIdx = len(cases)
				cases = append(cases, hostbridge.SelectCaseDefault())
			}
			// a select without a default can park: it counts toward
			// deadlock unless some arm watches a wake-capable channel.
			wake := process.Wake{}
			for _, a := range arms {
				// Wakeable is only ever set for unmanaged/Misc arms — a
				// managed arm carries its channel in WakeChan instead.
				if a.Wakeable {
					wake.Misc = true
				}
				if a.WakeChan != 0 {
					wake.Chans = append(wake.Chans, a.WakeChan)
				}
			}
			var chosen int
			var open bool
			var recvd = hostbridge.RVOf(nil)
			if defIdx >= 0 {
				chosen, recvd, open = hostbridge.Select(cases)
			} else {
				unpark := v.park(wake)
				func() {
					defer unpark()
					chosen, recvd, open = hostbridge.Select(cases)
				}()
			}
			switch {
			case chosen == doneIdx:
				panic(procExit{})
			case chosen == defIdx:
				f.ip = table + n // last table slot: the default body
			default:
				a := arms[chosen]
				if !a.Send {
					switch a.NRecv {
					case 0:
						f.push(runtime.NIL)
					case 1:
						f.push(hostbridge.ChanZero(v.chanHooks(f), a, open, recvd))
					default:
						f.push(&runtime.Tuple{Elems: []runtime.Value{hostbridge.ChanZero(v.chanHooks(f), a, open, recvd), open}})
					}
				}
				f.ip = table + chosen
			}
		case bytecode.OpPanic:
			panic(&runtime.Panic{Value: f.pop(), VC: v})
		case bytecode.OpTrap:
			panic(trapFromConst(ins.Pos, consts[ins.A]))
		case bytecode.OpReturn:
			n := int(ins.A)
			switch {
			case n < 0:
				// bare return: gather named result slots after defers run
				f.retNamed = true
			case n > 0 && len(f.ch.NamedSlots) == n:
				// explicit return stores into the named result slots first,
				// so deferred calls can still mutate them before teardown
				for i := n - 1; i >= 0; i-- {
					f.putLocal(int(f.ch.NamedSlots[i]), f.pop())
				}
				f.retNamed = true
			default:
				if n == 1 {
					// the common single result rides the frame's
					// scratch slot — no backing array to allocate.
					f.res1[0] = f.pop()
					f.results = f.res1[:]
				} else {
					f.results = make([]runtime.Value, n)
					if n > 0 { // a length-0 slice allocates no backing array
						v.profAllocs++
					}
					for i := n - 1; i >= 0; i-- {
						f.results[i] = f.pop()
					}
				}
			}
			f.ip = len(code)
		default:
			f.trap("unknown opcode %d", ins.Op)
		}
	}
}

// fileOf returns the source file an instruction belongs to, via its
// position (precise for __init__, which mixes decls from several files),
// falling back to the function's own file.
func fileOf(f *frame, pkg *runtime.Package) *syntax.File {
	if pos := f.pos(); pos.IsValid() && pkg.Fset != nil {
		name := pkg.Fset.PositionFor(pos, false).Filename
		if sf, ok := pkg.FileByName[name]; ok {
			return sf
		}
	}
	return f.fn.File
}

// runBounded executes the frame's instructions while its ip stays strictly
// inside (lo, hi); reaching either edge returns to the caller, who
// interprets the stop: ip == lo is the loop back-edge (iteration done),
// anything else means the body abandoned the loop (break, goto out, or
// OpReturn). Bounds are saved/restored so bounded runs nest.
func (v *VM) runBounded(f *frame, lo, hi int) {
	oLo, oHi := f.boundLo, f.boundHi
	f.boundLo, f.boundHi = lo, hi
	defer func() { f.boundLo, f.boundHi = oLo, oHi }()
	v.loop(f)
}

func truthy(v runtime.Value) bool {
	switch x := v.(type) {
	case bool:
		return x
	case runtime.Nil:
		return false
	case *runtime.TypedNil:
		return false
	case *runtime.IfaceNil:
		return true // interface with a dynamic type is not nil
	case *runtime.Named:
		if u, ok := x.V.(*runtime.UConst); ok {
			return constTruthy(u)
		}
		return truthy(x.V)
	case *runtime.UConst:
		return constTruthy(x)
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}
