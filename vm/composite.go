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
	"go/constant"
	"go/token"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// selectMember implements base.name for import refs, packages, structs,
// typedefs, and cells.
func (v *VM) selectMember(f *frame, base runtime.Value, name string, sites ...*bytecode.GlobalSite) runtime.Value {
	// site is the calling instruction's per-site cache (OpSelect /
	// OpSelectCall); nested selects — dynamic dispatch on a promoted
	// member, unwrapping a ref — forward it: the slot's TypeID guard
	// keeps one typedef's answer from serving another.
	var site *bytecode.GlobalSite
	if len(sites) > 0 {
		site = sites[0]
	}
	switch b := base.(type) {
	case *runtime.ImportRef:
		if !token.IsExported(name) && !b.AllNames {
			f.trap("cannot refer to unexported name %s.%s", b.Path, name)
		}
		p, err := b.Materialize()
		if err != nil {
			f.trap("import %s: %s", b.Path, err)
		}
		mv, err := v.memberOf(p, name)
		if err != nil {
			f.trapErr(err)
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			mv = c.Elem
		} else if _, cached := p.Globals.Get(name); !cached {
			// cache a fresh materialization only: rebinding the value
			// MemberV just read from Globals would bump the env's
			// generation and drop every cached OpGlobal site of p.
			p.Globals.Set(name, mv)
		}
		return mv
	case *runtime.Package:
		if !token.IsExported(name) {
			f.trap("cannot refer to unexported name %s.%s", b.Path, name)
		}
		mv, err := v.memberOf(b, name)
		if err != nil {
			// member access on a package is namespace-only — the
			// value's meaning must not change with its type, so the
			// exported *runtime.Package metadata (Path, Name, Dir,
			// State, ...) lives behind the inspect.* accessors.
			// When the miss is one of those names, the trap spells
			// the escape hatch.
			if _, ok := v.hostMember(f, b, name); ok {
				hint := "use the inspect.* accessors"
				switch name {
				case "Path", "Name", "Dir", "State", "Standard":
					hint = fmt.Sprintf("use inspect.%s(pkg)", name)
				}
				f.trapErr(fmt.Errorf("%w (inspect-layer package metadata — %s)", err, hint))
			}
			f.trapErr(err)
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			mv = c.Elem
		}
		return mv
	case *runtime.Struct:
		return v.structMember(f, b, name, b, site)
	case *runtime.Named:
		return v.namedMember(f, b, name, b, site)
	case *runtime.Cell:
		// a cell can hold another ref (`p := &s` stores a ref value) —
		// select on the stored ref so a pointer receiver writes through
		// to the pointee, not into p's cell.
		switch b.Elem.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if f.selSeen == nil {
				f.selSeen = map[runtime.Value]bool{}
			}
			if f.selSeen[b.Elem] {
				f.trap("select %s on cyclic reference %T", name, b.Elem)
			}
			f.selSeen[b.Elem] = true
			r := v.selectMember(f, b.Elem, name, site)
			delete(f.selSeen, b.Elem)
			return r
		}
		switch e := b.Elem.(type) {
		case *runtime.Struct:
			return v.structMember(f, e, name, b, site)
		case *runtime.Named:
			return v.namedMember(f, e, name, b, site)
		case *runtime.TypedNil:
			return v.memberOfType(f, e.Typ, name, b, false)
		case *runtime.IfaceNil:
			return v.memberOfType(f, e.Typ, name, e, true)
		case *runtime.Slice:
			return v.typedMember(f, e.Typ, name, b, "slice")
		case *runtime.Map:
			return v.typedMember(f, e.Typ, name, b, "map")
		case *runtime.Chan:
			return v.typedMember(f, e.Typ, name, b, "chan")
		case *runtime.GoValue:
			// a host value stored in a cell (`var mu sync.Mutex`): select on
			// the boxed value so host methods resolve
			return v.selectMember(f, e, name, site)
		case *runtime.Package:
			// an inspect-layer package stored in a var
			return v.selectMember(f, e, name, site)
		case runtime.Nil:
			// a nil interface slot (var t hostbridge.Type; t := TypeOf(nil))
			// — Go's nil itab deref is a recoverable panic, not a trap.
			panic(runtime.NilDerefPanic())
		default:
			// a raw scalar (a bare time.Duration var) or any other
			// value with no cell-specific dispatch selects on the
			// element itself.
			return v.selectMember(f, b.Elem, name, site)
		}
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		dv, ok := runtime.Deref(base)
		recv := base
		if !ok {
			// a deref ref over a nil pointer is Go's nil dereference —
			// (*p).X / (*p).M() on a nil *T panic, not trap.
			if dr, isDR := base.(*runtime.DerefRef); isDR {
				if px, pok := runtime.Deref(dr.Ptr); pok {
					if tn, isTN := asTypedNil(px); isTN && tn.Typ.Kind == runtime.KindPointer {
						panic(runtime.NilDerefPanic())
					}
				}
			}
			// A receiver-position ref may sit over an operand that does
			// not share storage — a map element reads as a copy — or
			// over a base the ref cannot walk (m[k].f[i]). Select on
			// what the operand reads to: the re-based element ref when
			// storage is shared, the element value otherwise.
			if dv, recv, ok = v.receiverOf(f, base); !ok {
				f.trap("select %s on unresolved reference %T", name, base)
			}
		}
		switch t := dv.(type) {
		case *runtime.Struct:
			return v.structMember(f, t, name, recv, site)
		case *runtime.Named:
			return v.namedMember(f, t, name, recv, site)
		case *runtime.Slice:
			return v.typedMember(f, t.Typ, name, recv, "slice")
		case *runtime.Map:
			return v.typedMember(f, t.Typ, name, recv, "map")
		case *runtime.Chan:
			return v.typedMember(f, t.Typ, name, recv, "chan")
		case *runtime.TypedNil:
			// a nil value under a ref (p.errors when errors is a nil
			// named slice) still carries the ref so a pointer receiver
			// binds &base.f, not a detached copy. A ref the runtime
			// cannot read back (a field of a host struct) selects on
			// the nil itself.
			if _, ok := runtime.Deref(recv); !ok {
				recv = t
			}
			return v.memberOfType(f, t.Typ, name, recv, false)
		}
		// pointer boxes ([]*T), nils, host values, packages — dispatch
		// on the element value itself, like an unindexed select.
		switch dv.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if f.selSeen == nil {
				f.selSeen = map[runtime.Value]bool{}
			}
			if f.selSeen[dv] {
				f.trap("select %s on cyclic reference %T", name, dv)
			}
			f.selSeen[dv] = true
			r := v.selectMember(f, dv, name, site)
			delete(f.selSeen, dv)
			return r
		}
		return v.selectMember(f, dv, name, site)
	case *runtime.TypeDef:
		if _, isPtr := b.Anon.(*ast.StarExpr); isPtr {
			// `(*T).M` — a pointer method expression sees the full
			// method set (value and pointer receivers alike).
			if et := v.elemTypedef(f, b); et != nil {
				if m, ok := et.Methods[name]; ok && selVisible(selPkg(f), et.Pkg, name) {
					if m.PtrRecv {
						return m // receiver param *T binds the Cell argument
					}
					return v.methodExprDeref(b, m)
				}
				// a bound host basic's methods live on the host type —
				// the expression re-dispatches on the (pointer) receiver.
				// (*T).M sees the pointer method set, so probe *base where
				// base is the modeled type: bound struct reps mint pointers
				// (bytes.Buffer's rep is *bytes.Buffer).
				if rt := hostTypOf(et); rt != nil {
					for rt.Kind() == hostbridge.KindPointer {
						rt = rt.Elem()
					}
					if _, ok := hostbridge.PointerTo(rt).MethodByName(name); ok {
						return v.methodExprThunk(b, name)
					}
				}
				// promoted through an embedded field — re-select on the
				// actual receiver argument (`(*U).Sum` reaches I's value).
				if len(et.EmbedSpecs) > 0 || et.Kind == runtime.KindInterface {
					return v.methodExprThunk(b, name)
				}
			}
			f.trapCode(runtime.TrapCodeUndefined, "type %s has no method %s", tdName(b), name)
		}
		if m, ok := b.Methods[name]; ok && selVisible(selPkg(f), b.Pkg, name) {
			if m.PtrRecv {
				f.trap("invalid method expression %s.%s (needs pointer receiver)", tdName(b), name)
			}
			return m // method expression: T.M(recv, ...)
		}
		// a bound host basic's methods live on the host type
		// (hostbridge.StructTag.Get, time.Duration.String): the method
		// expression re-dispatches on its receiver argument like Go's
		// selector lowering — value methods only, matching T's set.
		if rt := hostTypOf(b); rt != nil {
			// bound struct reps mint pointers (*bytes.Buffer): T's
			// method-expression set is the named type's — value
			// receivers only; a pointer-only name traps like the
			// declared PtrRecv arm above.
			for rt.Kind() == hostbridge.KindPointer {
				rt = rt.Elem()
			}
			if _, ok := rt.MethodByName(name); ok {
				return v.methodExprThunk(b, name)
			}
			if _, ok := hostbridge.PointerTo(rt).MethodByName(name); ok {
				f.trap("invalid method expression %s.%s (needs pointer receiver)", tdName(b), name)
			}
		}
		// `U.Sum` — a promoted method through an embedded field — or
		// `I.m` — an interface requirement — dispatches on the concrete
		// receiver argument, like Go's selector lowering.
		if len(b.EmbedSpecs) > 0 || b.Kind == runtime.KindInterface {
			return v.methodExprThunk(b, name)
		}
		f.trapCode(runtime.TrapCodeUndefined, "type %s has no method %s", b.Name, name)
	case runtime.Nil:
		// selecting a member on a nil interface value is a nil-pointer
		// dereference in Go — a script panic, not a trap.
		panic(runtime.NilDerefPanic())
	case *runtime.IfaceNil:
		if runtime.IsNilIface(b) {
			// a method call on a nil interface value dereferences the
			// nil itable — same panic as a bare nil.
			panic(runtime.NilDerefPanic())
		}
		return v.memberOfType(f, b.Typ, name, b, true)
	case *runtime.TypedNil:
		return v.memberOfType(f, b.Typ, name, b, false)
	case time.Duration:
		// a raw duration selects host methods (Hours, String, ...) the
		// same way a GoValue box does.
		return v.selectMember(f, &runtime.GoValue{V: b}, name, site)
	case *runtime.Slice:
		return v.typedMember(f, b.Typ, name, b, "slice")
	case *runtime.Map:
		return v.typedMember(f, b.Typ, name, b, "map")
	case *runtime.Chan:
		return v.typedMember(f, b.Typ, name, b, "chan")
	case *runtime.GoValue:
		// host value (stdlib intrinsic result): fields first, then the
		// method set — Go forbids a field and method sharing a name, so
		// probing fields first is safe and keeps `cmd.Dir`-style access
		// working on host structs like *exec.Cmd.
		if mv, ok := v.hostMember(f, b.V, name, site); ok {
			return mv
		}
		f.trapCode(runtime.TrapCodeUndefined, "no member %s on host value %T", name, b.V)
	default:
		f.trapCode(runtime.TrapCodeUndefined, "select %s on %T", name, base)
	}
	return nil
}

// framePkgPath is the package path the running code belongs to — the
// lexical package of f's function. Go scopes unexported fields to that
// package, so hostField/setField use it to decide whether interpreted
// code may touch a host field: net/http's ServeMux can stamp r.pat on
// a host *http.Request; package main cannot.
func framePkgPath(f *frame) string {
	if f == nil || f.fn == nil || f.fn.Pkg == nil {
		return ""
	}
	return f.fn.Pkg.Path
}

// hostSelSlot is the method-table replay a host-receiver select site
// caches: the bound method's index and the boxing flags. Host method
// sets are static, so the slot's reflect.Type guard is the whole key —
// a site whose receiver type changes simply misses and resolves again.
type hostSelSlot struct {
	midx   int // rt's method-table index replayed via rv.Method(midx)
	tm     *hostbridge.Method
	opaque string // rt's string when it is an OpaqueContainers store
}

// hostMember resolves a field or method on a host value by selector
// depth like gc: a declared field wins, then the method set, and a
// field promoted through embedding only when nothing shallower claims
// the name (`v.Name` on *template.Template is the method, not the
// embedded Tree.Name field).
// (nil, false) reports that neither exists. f scopes unexported fields
// to their declaring package, the same gate Go applies — a site fixes
// that package lexically, so a cached method/miss stays correct. The
// resolutions ride the ordinary selectSlot so one site can alternate
// between receiver kinds without an atomic.Value type panic.
func (v *VM) hostMember(f *frame, hv any, name string, sites ...*bytecode.GlobalSite) (runtime.Value, bool) {
	var site *bytecode.GlobalSite
	if len(sites) > 0 {
		site = sites[0]
	}
	// A ScriptMethoder's answers are instance-scoped — a same-type
	// sibling can carry a different script method set — so it neither
	// reads nor writes the type-level site cache.
	_, isSM := hv.(runtime.ScriptMethoder)
	if site != nil && !isSM {
		rt := hostbridge.TypeOf(hv)
		if slot, _ := site.Cache.Load().(*selectSlot); slot != nil && slot.rt == rt && slot.name == name && slot.res != nil {
			switch slot.res.kind {
			case selHostMiss:
				return nil, false
			case selHostMember:
				return hostMethodBox(hv, name, hostbridge.RVOf(hv).Method(slot.res.hm.midx), slot.res.hm), true
			}
		}
	}
	val, res := v.hostMemberRes(f, hv, name)
	if site != nil && res != nil && !isSM && !(res.kind == selHostMiss && hostNil(hv)) {
		site.Cache.Store(&selectSlot{rt: hostbridge.TypeOf(hv), name: name, res: res})
	}
	return val, val != nil
}

// hostNil reports whether hv is a nil-able host value holding nil. A
// member miss against it is value-dependent — a non-nil sibling of the
// same type can still resolve the field — so the site must not record
// it at type level.
func hostNil(hv any) bool {
	rv := hostbridge.RVOf(hv)
	if !rv.IsValid() {
		return true
	}
	switch rv.Kind() {
	case hostbridge.KindPtr, hostbridge.KindUnsafePointer, hostbridge.KindInterface,
		hostbridge.KindSlice, hostbridge.KindMap, hostbridge.KindChan, hostbridge.KindFunc:
		return rv.IsNil()
	}
	return false
}

// hostMemberRes runs the full member resolution, reporting the
// cacheable outcome beside the value: a method slot on the method
// path, a miss slot on no-member, nil for the value-dependent paths
// (a field hit or a ScriptMethoder answer) no cache can replay.
func (v *VM) hostMemberRes(f *frame, hv any, name string) (runtime.Value, *selRes) {
	fv, fdepth, fieldOK := hostField(hv, name, framePkgPath(f))
	m := hostbridge.RVOf(hv).MethodByName(name)
	if fieldOK && fdepth == 0 {
		return hostbridge.GoSlotValueOf(fv), nil
	}
	if sm, ok := hv.(runtime.ScriptMethoder); ok {
		if fn, ok := sm.ScriptMethod(name); ok {
			bf := &runtime.BuiltinFunc{Name: name, Fn: fn}
			if m.IsValid() {
				bf.Target = m.Interface()
			}
			return bf, nil
		}
	}
	if fieldOK && !m.IsValid() {
		return hostbridge.GoSlotValueOf(fv), nil
	}
	// a promoted field (Tree.Name through an embedded *parse.Tree) sits
	// deeper than a declared method — gc's selector picks the shallower
	// member, so a same-named method wins the name.
	if !m.IsValid() {
		return nil, &selRes{kind: selHostMiss}
	}
	var tm *hostbridge.Method
	// the method value's own PC is a thunk (reflect.methodValueCall)
	// — the declared method's Func is the only handle that still
	// points at the real code, so keep it for inspect. Its index is
	// also the replay handle: without one the resolution cannot
	// memoize (Value.Method(i) shares the type's method-table order).
	if t, ok := hostbridge.TypeOf(hv).MethodByName(name); ok {
		t := t
		tm = &t
	}
	if tm == nil {
		return hostMethodBox(hv, name, m, nil), nil
	}
	slot := &hostSelSlot{midx: tm.Index, tm: tm}
	if rt := hostbridge.TypeOf(hv).String(); hostbridge.OpaqueContainers[rt] {
		slot.opaque = rt
	}
	return hostMethodBox(hv, name, m, slot), &selRes{kind: selHostMember, hm: slot}
}

// hostMethodBox wraps a bound host method value in its script-callable
// BuiltinFunc: the OpaqueContainers store path, the uncontended-lock
// inline fast path, the receiver-less %T signature, and the declared
// Func handle inspect keeps. A nil slot boxes the plain call — the
// uncacheable path an absent type-level Method forces.
func hostMethodBox(hv any, name string, m hostbridge.Value, slot *hostSelSlot) *runtime.BuiltinFunc {
	call := func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return callReflectFunc(name, m, vc, args)
	}
	if slot != nil && slot.opaque != "" {
		rt := slot.opaque
		call = func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return callOpaqueStore(rt, name, m, vc, args)
		}
	}
	if try := uncontendedLock(hv, name); try != nil {
		// Lock/RLock park on a helper goroutine watched against
		// proc.done (see blockingHostMethods) — a goroutine spawn per
		// call, which stdlib-internal mutexes (go/token's per-line
		// lock) pay thousands of times. An uncontended lock cannot
		// park, so take it inline and keep the helper for contention.
		slow := call
		call = func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 0 && try() {
				return runtime.NIL, nil
			}
			return slow(vc, args)
		}
	}
	bf := &runtime.BuiltinFunc{Name: name, Fn: call}
	// the bound method value spells the receiver-less signature
	// (`func() int` for Len) under %T.
	bf.Target = m.Interface()
	if slot != nil {
		bf.Method = slot.tm
	}
	return bf
}

// methodExprThunk builds a T.M/(*T).M method-expression value for a
// method reached through an embedded field or an interface
// requirement: `U.Sum`/`(*U).Sum`/`I.m` compile to a func(recv,
// args...) that re-selects the member on its receiver argument, so
// embedded and interface dispatch run on the concrete value like Go's
// selector lowering.
func (v *VM) methodExprThunk(td *runtime.TypeDef, name string) runtime.Value {
	return &runtime.BuiltinFunc{
		Name:     tdName(td) + "." + name,
		Forwards: true,
		Fn: func(vm runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("method expression %s.%s needs a receiver argument", tdName(td), name)
			}
			// `I.M(nil)` dispatches on the receiver's concrete value —
			// a nil interface has none, so the call is a nil-pointer
			// dereference panic in Go, not a lookup failure.
			if _, isNil := args[0].(runtime.Nil); isNil {
				panic(runtime.NilDerefPanic())
			}
			if runtime.IsNilIface(args[0]) {
				panic(runtime.NilDerefPanic())
			}
			m, ok := methodExprMember(vm, args[0], name)
			if !ok {
				return nil, &runtime.UndefinedError{Reason: fmt.Sprintf("type %s has no method %s", tdName(td), name)}
			}
			return vm.Call(m, args[1:])
		},
	}
}

// methodExprDeref adapts a value-receiver method for the (*T).M method
// expression: the receiver argument arrives as *T and binds its
// pointee, `(*S).val(&s)` calling val with s.
func (v *VM) methodExprDeref(td *runtime.TypeDef, m *runtime.Function) runtime.Value {
	return &runtime.BuiltinFunc{
		Name:     tdName(td) + "." + m.Name,
		Forwards: true,
		Fn: func(vm runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("method expression %s.%s needs a receiver argument", tdName(td), m.Name)
			}
			recv, ok := runtime.Deref(args[0])
			if !ok {
				return nil, fmt.Errorf("cannot use %T as %s receiver in method expression", args[0], tdName(td))
			}
			return vm.Call(m, append([]runtime.Value{recv}, args[1:]...))
		},
	}
}

// promotedViaMethodSet resolves a promoted member through the typedef's
// shared method set — methods, interface-dispatched and host members the
// set locates by declared index path. A member the set cannot name (a
// host embed's promoted FIELD is not a method) reports not-found so the
// caller falls back to the value walk; an ambiguous or outranked member
// is decided here — ambiguous traps like Go's compile-time rejection, a
// member deeper than the declared field path loses to it.
func (v *VM) promotedViaMethodSet(f *frame, s *runtime.Struct, name string, fieldPath []int, fieldFound bool) (runtime.Value, *selRes, bool) {
	if v.H.TypeMethodInfoOf == nil {
		return nil, nil, false
	}
	ms, err := v.H.TypeMethodInfoOf(s.Def, true)
	if err != nil || ms == nil {
		return nil, nil, false
	}
	fieldDepth := -1
	if fieldFound {
		fieldDepth = len(fieldPath) - 1
	}
	// an unexported selector names (call package, name): the set's
	// pkg-qualified keys — reqKey's — answer it (M4-2b).
	key := selKey(selPkg(f), name)
	if ad, contested := selGet(ms.Ambiguous, key); contested {
		if fieldDepth < 0 || ad <= fieldDepth {
			f.trap("ambiguous selector %s", name)
		}
		return nil, nil, false // the shallower declared field wins
	}
	pm, found := selGet(ms.Members, key)
	if !found {
		return nil, nil, false
	}
	if fieldDepth >= 0 {
		if pm.Depth > fieldDepth {
			return nil, nil, false // the declared field wins
		}
		if pm.Depth == fieldDepth {
			f.trap("ambiguous selector %s", name)
		}
	}
	if len(pm.Path) == 0 {
		return nil, nil, false
	}
	st, ix := v.fieldSlotAtPath(f, s, pm.Path, name)
	if st == nil {
		return nil, nil, false
	}
	slot := st.Fields[ix]
	res := &selRes{kind: selMemberSet, path: pm.Path}
	switch pm.Via {
	case "iface":
		// dispatch dynamically on the stored interface value — the
		// synthesized signature shell in Funcs is not callable.
		res.via = 1
		return v.selectMember(f, slot, name), res, true
	case "host":
		res.via = 2
		return v.selectMember(f, runtime.Unwrap(slot), name), res, true
	}
	fn, ok := selGet(ms.Funcs, key)
	if !ok {
		return nil, nil, false
	}
	res.m = fn
	return v.promotedValue(f, name, &promoted{m: fn, recv: slot, mst: st, midx: ix}), res, true
}

// promotedValue binds the member promotedMember found: a field value,
// a host member to reselect on, an interface member's dynamic dispatch,
// or a bound method with its receiver adjustments.
func (v *VM) promotedValue(f *frame, name string, pr *promoted) runtime.Value {
	switch {
	case pr.host != nil:
		return v.selectMember(f, pr.host, name)
	case pr.st != nil:
		return pr.st.Fields[pr.idx]
	case pr.m == nil:
		// interface-typed embedded field: dispatch on the
		// stored concrete value.
		return v.selectMember(f, pr.recv, name)
	}
	m := pr.m
	if err := m.EnsureCompiled(); err != nil {
		f.trapErr(err)
	}
	r := pr.recv
	if m.PtrRecv {
		// bind the field's live slot: a FieldRef over the struct
		// that holds it — `*p = x` inside the method writes the
		// stored field, not a copy of the value.
		if pr.mst != nil && pr.midx < len(pr.mst.Def.Fields) {
			r = v.ptrReceiver(&runtime.FieldRef{Base: pr.mst, Name: pr.mst.Def.Fields[pr.midx]})
		} else {
			r = v.ptrReceiver(r)
		}
	} else {
		if dv, ok := runtime.Deref(r); ok {
			r = dv
		} else if tn, isNil := r.(*runtime.TypedNil); isNil && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
			// a value method promoted through a nil embedded
			// pointer dereferences it at selection — `a.h()`
			// on A{B{*D}} with D nil panics like Go.
			panic(runtime.NilDerefPanic())
		}
		r = valueCopy(r)
	}
	return &runtime.BoundMethod{Recv: r, Fn: m}
}

// fieldAtPath binds the field a declared index path names — each step
// crosses one embedded field (reflect's Field.Index convention). A nil
// embedded pointer on the way panics on the implicit dereference, like
// the hit-through-nil path of the value search.
func (v *VM) fieldAtPath(f *frame, s *runtime.Struct, path []int, name string) runtime.Value {
	st, ix := v.fieldSlotAtPath(f, s, path, name)
	if st == nil {
		return nil
	}
	return st.Fields[ix]
}

// fieldSlotAtPath resolves the declared index path to its (struct,
// field index) slot — the write-target shape of fieldAtPath.
func (v *VM) fieldSlotAtPath(f *frame, s *runtime.Struct, path []int, name string) (*runtime.Struct, int) {
	st := s
	for i, ix := range path {
		if ix < 0 || ix >= len(st.Fields) {
			f.trap("field %s: index %d out of range on %s", name, ix, st.Def.Name)
			return nil, 0
		}
		if i == len(path)-1 {
			return st, ix
		}
		k := -1
		for j, ei := range st.Def.EmbedIdx {
			if ei == ix {
				k = j
				break
			}
		}
		if k < 0 || k >= len(st.Def.EmbedSpecs) {
			f.trap("field %s: no embed spec at index %d on %s", name, ix, st.Def.Name)
			return nil, 0
		}
		spec := st.Def.EmbedSpecs[k]
		var ptr bool
		if sx, ok := spec.(*ast.StarExpr); ok {
			ptr, spec = true, sx.X
		}
		embTd, err := v.H.ResolveType(st.Def, spec)
		if err != nil || embTd == nil {
			f.trap("field %s: cannot resolve embed on %s", name, st.Def.Name)
			return nil, 0
		}
		inner, ok := v.embedValue(f, st, ix, v.peelNamed(embTd), ptr)
		if !ok {
			panic(runtime.NilDerefPanic())
		}
		st = inner
	}
	return nil, 0
}

// declaredFieldPath resolves a selector's field to its declared index
// path for pinning into a runtime.FieldRef — nil keeps the ref's
// name-search. Field and member collisions stay observable through the
// ref's own rules, same as the BFS. The second result is the typedef
// the field resolves to, pinned onto the ref so a deeper selection
// never has to dereference it for its type.
//
// The base's typedef must come without evaluating lazy storage refs:
// Go evaluates a store target's indirections and element fetches at
// the store, after the right side runs (`w.In.X = f()` runs f before
// the nil panic on w.In). refTypedef is the type-level walk parallel
// to the storage walk — a ref's typedef answers from pinned or
// declared information, never from the value it points at, so the
// declared path pins even through nil embedded pointers and
// out-of-bounds indexes.
func (v *VM) declaredFieldPath(f *frame, base runtime.Value, name string) ([]int, *runtime.TypeDef) {
	if v.H.FieldPath == nil {
		return nil, nil
	}
	def := v.peelStructTypedef(v.refTypedef(f, base))
	if def == nil || len(def.LocalTypes) > 0 {
		return nil, nil
	}
	if idx, found, decided := v.H.FieldPath(def, name); decided && found {
		return idx, v.pathTypedef(def, idx)
	}
	return nil, nil
}

// refTypedef answers the typedef of the value base resolves to —
// statically. A lazy ref answers from what it already carries or what
// its base's type declares (an IndexRef's element type, a DerefRef's
// pointee), so neither the ref nor its target is ever evaluated: Go
// runs those fetches and derefs at the store, after the right side.
func (v *VM) refTypedef(f *frame, base runtime.Value) *runtime.TypeDef {
	switch b := base.(type) {
	case *runtime.FieldRef:
		return b.Def
	case *runtime.IndexRef:
		// the element type of the container — a type query on the
		// base's declared type, not an element fetch.
		return v.elemTypedef(f, v.refTypedef(f, b.Base))
	case *runtime.DerefRef:
		return v.elemTypedef(f, v.refTypedef(f, b.Ptr))
	case *runtime.Cell:
		// an inferred slot (`a := []S{...}`) stamps no declared type
		// on the cell — the stored value carries it instead.
		if b.Typ != nil {
			return b.Typ
		}
		return v.refTypedef(f, b.Elem)
	case *runtime.Named:
		return b.Typ
	case *runtime.Slice:
		return b.Typ
	case *runtime.Map:
		return b.Typ
	case *runtime.Chan:
		return b.Typ
	default:
		if s := runtime.StructOf(base); s != nil {
			return s.Def
		}
		return nil
	}
}

// peelStructTypedef peels aliases, named scalars and pointers down to
// the struct typedef a selector would see — the same hops pathTypedef
// makes per level. Nil stays nil.
func (v *VM) peelStructTypedef(def *runtime.TypeDef) *runtime.TypeDef {
	def = v.peelNamed(def)
	for def != nil && def.Kind == runtime.KindPointer && def.Elem != nil {
		def = def.Elem
	}
	return def
}

// pathTypedef reports the typedef a declared index path lands on —
// statically, off the embed specs and field typedefs, so a nil
// embedded pointer in the base is never touched by a type query.
func (v *VM) pathTypedef(def *runtime.TypeDef, path []int) *runtime.TypeDef {
	for i, ix := range path {
		var nt *runtime.TypeDef
		if i == len(path)-1 {
			if fts := v.fieldTypedefs(def); 0 <= ix && ix < len(fts) {
				nt = fts[ix]
			}
			if nt == nil {
				nt = v.embedTypedef(def, ix)
			}
		} else {
			nt = v.embedTypedef(def, ix)
		}
		if nt == nil {
			return nil
		}
		// a `*T` field's pointee typedef is already resolved on Elem;
		// named fields holding pointers reach the struct through it.
		def = v.peelStructTypedef(nt)
		if def == nil {
			return nil
		}
	}
	return def
}

// embedTypedef resolves the typedef of the embedded field at struct
// field index ix — nil when ix is not an embed or cannot resolve.
func (v *VM) embedTypedef(def *runtime.TypeDef, ix int) *runtime.TypeDef {
	for j, ei := range def.EmbedIdx {
		if ei != ix || j >= len(def.EmbedSpecs) {
			continue
		}
		spec := def.EmbedSpecs[j]
		if sx, ok := spec.(*ast.StarExpr); ok {
			spec = sx.X
		}
		nt, _ := v.H.ResolveType(def, spec)
		return nt
	}
	return nil
}

// pathPtr reports whether the declared index path crosses a pointer
// embedded field — composite-literal keys reject those outright (Go's
// "invalid implicit pointer indirection"), whatever the stored value.
func (v *VM) pathPtr(def *runtime.TypeDef, path []int) bool {
	for _, ix := range path[:len(path)-1] {
		k := -1
		for j, ei := range def.EmbedIdx {
			if ei == ix {
				k = j
				break
			}
		}
		if k < 0 || k >= len(def.EmbedSpecs) {
			return false
		}
		spec := def.EmbedSpecs[k]
		var ptr bool
		if sx, ok := spec.(*ast.StarExpr); ok {
			ptr, spec = true, sx.X
		}
		if ptr {
			return true
		}
		nt, err := v.H.ResolveType(def, spec)
		if err != nil {
			return false
		}
		def = v.peelNamed(nt)
		if def == nil {
			return false
		}
	}
	return false
}

// ptrReceiver binds the receiver of a `func (p *T) M` call for `x.M()`:
// when x's operand is a storage ref, x may already be a *T — a stored
// ref, a Named pointer, or a nil pointer — in which case the receiver is
// that pointer value, not &x (writing `*p =` reaches the pointee, not
// the variable). A plain value keeps the ref as &x; a non-ref operand
// boxes into a fresh cell (an unaddressable base like `getS().Set`,
// which Go rejects at compile time).
func (v *VM) ptrReceiver(r runtime.Value) runtime.Value {
	dv, ok := runtime.Deref(r)
	if !ok {
		// a nil *T receiver passes through as itself — `p == nil` and
		// `*p =` behave like Go, not like a **T pointing at the slot.
		if tn, isNil := r.(*runtime.TypedNil); isNil && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
			return r
		}
		return &runtime.Cell{Elem: r}
	}
	switch dv := dv.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return dv
	case *runtime.Named:
		if dv.Typ != nil && dv.Typ.Kind == runtime.KindPointer {
			return dv
		}
	case *runtime.TypedNil:
		if dv.Typ != nil && dv.Typ.Kind == runtime.KindPointer {
			return dv
		}
	}
	return r
}

// namedMember resolves base.name on a Named value: methods come only from
// the declared typedef (`type A B` does not inherit B's methods, like
// Go); fields reach through to the underlying struct's layout.
func (v *VM) namedMember(f *frame, n *runtime.Named, name string, recv runtime.Value, site *bytecode.GlobalSite) runtime.Value {
	td := n.Typ
	if site != nil && v.H.TypeIDOf != nil && td != nil && len(td.LocalTypes) == 0 {
		// same epoch-first discipline as structMember: a method-set
		// edit during resolution leaves the stored slot stale, and the
		// next read resolves again.
		epoch := runtime.MethodSetEpoch()
		tid := v.H.TypeIDOf(td)
		if slot, _ := site.Cache.Load().(*selectSlot); slot != nil &&
			slot.tid == tid && slot.epoch == epoch && slot.name == name {
			if val, ok := v.applyNamedSlot(f, n, name, recv, slot.res); ok {
				return val
			}
		}
		val, res := v.namedMemberRes(f, n, name, recv)
		if res != nil {
			site.Cache.Store(&selectSlot{tid: tid, epoch: epoch, name: name, res: res})
		}
		return val
	}
	val, _ := v.namedMemberRes(f, n, name, recv)
	return val
}

func (v *VM) namedMemberRes(f *frame, n *runtime.Named, name string, recv runtime.Value) (runtime.Value, *selRes) {
	// an anonymous *Declared typedef exposes the pointee's method set —
	// `(*T)(p)` keeps T's methods callable on the conversion result.
	td := n.Typ
	sp := selPkg(f)
	for td != nil && td.Kind == runtime.KindPointer && td.Spec == nil &&
		(td.Methods[name] == nil || !selVisible(sp, td.Pkg, name)) {
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			break
		}
		td = et
	}
	if m, ok := td.Methods[name]; ok && selVisible(sp, td.Pkg, name) {
		if err := m.EnsureCompiled(); err != nil {
			f.trapErr(err)
		}
		r := recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
		} else if td != n.Typ {
			// a value receiver reached through the peeled pointer binds
			// the pointee, re-tagged to the declared type.
			sv := n.V
			if dv, ok := runtime.Deref(sv); ok {
				sv = dv
			}
			r = valueCopy(runtime.Tag(td, sv))
		} else {
			// a value receiver binds a copy of the named value — for a
			// pointer-underlying declaration (`type P *Sq`) the pointer
			// itself is the receiver, so the pointee stays shared.
			r = valueCopy(n)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}, &selRes{kind: selNamedMethod, m: m, ntd: td}
	}
	if td != nil && (td.IfaceBase || td.Kind == runtime.KindInterface) {
		// a declared-over-interface tag has no members of its own
		// beyond declared overrides (checked above): `e.Error()` on
		// `type E error` dispatches on the payload the interface holds.
		return v.selectMember(f, n.V, name), nil
	}
	// fields live on the underlying struct value — promoted fields of the
	// underlying type are stored as fields on it, but promoted METHODS of
	// the underlying type are not part of the named type's method set. A
	// pointer-underlying declaration (`type P *Sq`) dereferences first.
	sv := n.V
	if dv, ok := runtime.Deref(n.V); ok {
		sv = dv
	}
	// a pointer conversion across declared types — `(*inner.Tag)(t)` on
	// a *Tag for `type Tag inner.Tag` — keeps the pointee's own Named
	// tag; fields still live on the struct underneath.
	for {
		nn, isNamed := sv.(*runtime.Named)
		if !isNamed {
			break
		}
		sv = nn.V
		if dv, ok := runtime.Deref(sv); ok {
			sv = dv
		}
	}
	if gv, isGo := sv.(*runtime.GoValue); isGo {
		// a host-boxed payload (a tagged host composite literal — its
		// Named tag only names the declared type) resolves fields and
		// methods on the box itself, e.g. p.Get() on &sync.Pool{...}.
		// a defined type over a host composite keeps the host box: gc
		// gives it only the promoted method set — fields plus the
		// anonymous field's methods — so the host type's own declared
		// methods must not resolve here (gc: Wrap.M yes, Wrap.Q no —
		// M4-2c). Fields (direct or promoted through embedding) answer
		// first — with no methods of its own, `Tpl(*t).Name` is the
		// embedded Tree.Name field, not the host Name() method.
		if td != nil && td.Kind == runtime.KindNamedBasic && td.HostNew == nil {
			if u := v.peelNamed(td); u != nil && u.HostNew != nil {
				pmd := hostbridge.HostPromotedMethodInner(gv.V, name)
				if fv, fdepth, fok := hostField(gv.V, name, framePkgPath(f)); fok &&
					(pmd < 0 || fdepth <= pmd) {
					return hostbridge.GoSlotValueOf(fv), nil
				}
				if pmd < 0 {
					f.trapCode(runtime.TrapCodeUndefined, "%s has no field or method %s", tdName(n.Typ), name)
				}
				// a shallower promoted method wins over a deeper field —
				// it falls through to hostMember.
			}
		}
		if mv, ok := v.hostMember(f, gv.V, name); ok {
			return mv, nil
		}
		f.trapCode(runtime.TrapCodeUndefined, "no member %s on host value %T", name, gv.V)
	}
	if td != nil && td.HostScalar != nil {
		// a bound host scalar (time.Duration) stores the raw host
		// value — its methods live on the host type, dispatched
		// through reflection like the bare value's own case in
		// selectMember. A payload still in script shape — the int64
		// or const-domain UConst a Named{monthTD, ...} keeps —
		// materializes to the host scalar first so its declared
		// methods (time.Month.String) resolve.
		hv := sv
		if cv, ok := runtime.HostScalarOf(td, sv); ok {
			hv = cv
		}
		if mv, ok := v.hostMember(f, hv, name); ok {
			return mv, nil
		}
	}
	if td != nil && td.HostNew != nil && td.Kind == runtime.KindNamedBasic {
		// a bound host named basic keeps a script payload (a
		// hostbridge.StructTag's string): its declared method set is
		// empty, but the host type's methods apply once the payload
		// is materialized as the host type — tag.Get("json").
		rt := hostbridge.TypeOf(td.HostNew())
		if rv := hostbridge.RVOf(sv); rv.IsValid() && rv.Type().ConvertibleTo(rt) {
			if mv, ok := v.hostMember(f, rv.Convert(rt).Interface(), name); ok {
				return mv, nil
			}
		}
	}
	if s, isStruct := sv.(*runtime.Struct); isStruct {
		for i, fn := range s.Def.Fields {
			if fn == name && selVisible(sp, s.Def.Pkg, fn) {
				return s.Fields[i], &selRes{kind: selField, idx: i}
			}
		}
		// the declaration answers the field side when it can —
		// non-field members compete at a shallower-or-equal depth,
		// though a defined type inherits none of them.
		if v.H.FieldPath != nil && len(s.Def.LocalTypes) == 0 {
			if idx, found, decided := v.H.FieldPath(s.Def, name); decided {
				if found {
					if pr, ok := v.promotedMemberEx(f, s, name, true, false, len(idx)-1); ok {
						if pr.host != nil {
							return v.selectMember(f, pr.host, name), nil
						}
						// methods and interface members do not
						// inherit — the trap below reports.
						f.trapCode(runtime.TrapCodeUndefined, "%s has no field or method %s", tdName(n.Typ), name)
					}
					return v.fieldAtPath(f, s, idx, name), &selRes{kind: selPath, path: idx}
				}
				if pr, ok := v.promotedMemberEx(f, s, name, true, false, -1); ok {
					if pr.host != nil {
						return v.selectMember(f, pr.host, name), nil
					}
				}
				f.trapCode(runtime.TrapCodeUndefined, "%s has no field or method %s", tdName(n.Typ), name)
				return nil, nil
			}
		}
		if pr, ok := v.promotedMember(f, s, name, true); ok {
			if pr.host != nil {
				return v.selectMember(f, pr.host, name), nil
			}
			if pr.st != nil {
				return pr.st.Fields[pr.idx], nil
			}
			// a defined type does not inherit promoted methods or
			// interface specs — fall through to the no-member trap.
		}
	}
	f.trapCode(runtime.TrapCodeUndefined, "%s has no field or method %s", tdName(n.Typ), name)
	return nil, nil
}

func (v *VM) setField(f *frame, base runtime.Value, name string, val runtime.Value) {
	// write through any pointer chain: *Cell (local var or *T), FieldRef,
	// IndexRef — assignment targets the struct they resolve to. Named
	// struct-underlying values write into the underlying fields.
	for {
		if n, ok := base.(*runtime.Named); ok {
			base = n.V
			continue
		}
		if _, isGV := base.(*runtime.GoValue); isGV {
			// a host value is the peel's leaf — the GoValue case
			// below writes into its box; dereferencing a *T box
			// would mint an endless chain of pointee boxes.
			break
		}
		dv, ok := runtime.Deref(base)
		if !ok {
			break
		}
		base = dv
	}
	switch b := base.(type) {
	case *runtime.Struct:
		for i, fn := range b.Def.Fields {
			if fn == name && selVisible(selPkg(f), b.Def.Pkg, fn) {
				// the field's declared type constrains the write —
				// `s.f = nil` on a *T field stores a typed nil.
				var ft *runtime.TypeDef
				if fts := v.fieldTypedefs(b.Def); i < len(fts) {
					ft = fts[i]
				}
				nv := v.coerce(f, val, ft)
				if !overwriteArrayIn(b.Fields[i], nv) {
					b.Fields[i] = nv
				}
				return
			}
		}
		// the declaration answers the field side when it can — a
		// declared path binds its slot directly, and only non-field
		// members compete at a shallower-or-equal depth.
		if v.H.FieldPath != nil && len(b.Def.LocalTypes) == 0 {
			if idx, found, decided := v.H.FieldPath(b.Def, name); decided {
				if found {
					if pr, ok := v.promotedMemberEx(f, b, name, true, false, len(idx)-1); ok {
						if pr.host != nil {
							v.setField(f, pr.host, name, val)
							return
						}
						f.trapCode(runtime.TrapCodeUndefined, "%s has no field %s", b.Def.Name, name)
					}
					st, fi := v.fieldSlotAtPath(f, b, idx, name)
					if st == nil {
						return
					}
					var ft *runtime.TypeDef
					if fts := v.fieldTypedefs(st.Def); fi < len(fts) {
						ft = fts[fi]
					}
					nv := v.coerce(f, val, ft)
					if !overwriteArrayIn(st.Fields[fi], nv) {
						st.Fields[fi] = nv
					}
					return
				}
				if pr, ok := v.promotedMemberEx(f, b, name, true, false, -1); ok {
					if pr.host != nil {
						v.setField(f, pr.host, name, val)
						return
					}
				}
				f.trapCode(runtime.TrapCodeUndefined, "%s has no field %s", b.Def.Name, name)
			}
		}
		if pr, ok := v.promotedMember(f, b, name, true); ok {
			if pr.host != nil {
				v.setField(f, pr.host, name, val)
				return
			}
			if pr.st == nil {
				// a promoted method/interface member is not
				// assignable — `s.M = x` has no field in Go either.
				f.trapCode(runtime.TrapCodeUndefined, "%s has no field %s", b.Def.Name, name)
			}
			var ft *runtime.TypeDef
			if fts := v.fieldTypedefs(pr.st.Def); pr.idx < len(fts) {
				ft = fts[pr.idx]
			}
			nv := v.coerce(f, val, ft)
			if !overwriteArrayIn(pr.st.Fields[pr.idx], nv) {
				pr.st.Fields[pr.idx] = nv
			}
			return
		}
		f.trapCode(runtime.TrapCodeUndefined, "%s has no field %s", b.Def.Name, name)
	case *runtime.GoValue:
		// host struct behind a pointer: `cmd.Dir = "sub"` writes through
		// reflection so intrinsics like os/exec stay wired to scripts.
		rv := hostbridge.RVOf(b.V)
		for rv.Kind() == hostbridge.KindPointer || rv.Kind() == hostbridge.KindInterface {
			if rv.IsNil() {
				f.trap("set field %s on nil host %T", name, b.V)
			}
			rv = rv.Elem()
		}
		if rv.Kind() != hostbridge.KindStruct {
			f.trap("set field %s on host value %T", name, b.V)
		}
		// A struct boxed by value (`var t T`, or a declared type whose
		// HostNew spells T) is an unaddressable copy; move it into
		// addressable storage so the field write can land, then put the
		// updated struct back in the box — gc value semantics.
		copied := false
		if !rv.CanAddr() {
			cp := hostbridge.NewElem(rv)
			cp.Set(rv)
			rv = cp
			copied = true
		}
		fv := rv.FieldByName(name)
		if !fv.IsValid() {
			f.trapCode(runtime.TrapCodeUndefined, "host value %T has no field %s", b.V, name)
		}
		// Go lets a package write its own unexported fields; interpreted
		// stdlib code runs in that package, so ServeMux stamping r.pat on
		// a host *http.Request lands in the real memory. Anything else —
		// another package's private field — still traps like gc's
		// rejection, before the value converts.
		sf, _ := rv.Type().FieldByName(name)
		ownPkg := fv.CanAddr() && sf.PkgPath != "" && sf.PkgPath == framePkgPath(f)
		if !fv.CanSet() && !ownPkg {
			f.trap("host value %T has no settable field %s", b.V, name)
		}
		// exec.Cmd.Dir follows the script's virtual cwd: Go resolves a
		// relative Dir in the process cwd, and minigo's process-cwd
		// equivalent is the engine cwd rather than the host process's.
		if name == "Dir" && rv.Type().PkgPath() == "os/exec" && rv.Type().Name() == "Cmd" && v.H.CwdAbs != nil {
			if dir, ok := runtime.Unwrap(v.materialize(f, val)).(string); ok && dir != "" && !filepath.IsAbs(dir) {
				val = v.H.CwdAbs(dir)
			}
		}
		nv, err := marshalHost.HostFieldValue(val, rv.Type(), name, fv.Type(), v)
		if err != nil {
			f.trapErr(fmt.Errorf("set field %s: %w", name, err))
		}
		if fv.CanSet() {
			fv.Set(nv)
		} else {
			hostbridge.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem().Set(nv)
		}
		if copied {
			b.V = rv.Interface()
		}
		return
	case *runtime.TypedNil:
		if b.Typ != nil && b.Typ.Kind == runtime.KindPointer {
			panic(runtime.NilDerefPanic())
		}
		f.trap("set field %s on nil %s", name, tdName(b.Typ))
	case *runtime.IfaceNil:
		if b.Typ != nil && b.Typ.Kind == runtime.KindPointer {
			panic(runtime.NilDerefPanic())
		}
		f.trap("set field %s on nil %s", name, tdName(b.Typ))
	case *runtime.ImportRef:
		// package-level assignment: `runtime.MemProfileRate = 1`
		// writes into the bound cell (or the global slot) so later
		// reads of the member see it.
		p, err := b.Materialize()
		if err != nil {
			f.trap("import %s: %s", b.Path, err)
		}
		if p.Index != nil && !p.LazyInit && p.State() < runtime.Initializing {
			// Go initializes an import before the importer's code runs:
			// initialize p now, or its own initializer would later reset
			// what this write stored — go/scanner's init sets
			// scannerhooks.StringEnd, which go/parser (Go 1.26) reads.
			if err := p.EnsureReadyRun(v.runInit); err != nil {
				f.trapErr(err)
			}
		}
		if existing, ok := p.Globals.Get(name); ok {
			if c, isCell := existing.(*runtime.Cell); isCell {
				c.Elem = val
				return
			}
		}
		p.Globals.Set(name, val)
	case *runtime.FieldRef:
		// interior write through a field select crossing a map element
		// (m[k].f.g = v): the field read lands on the element copy, so
		// Go allows it only when the field itself is reference-shaped —
		// m[k].g.x on a struct field stays a compile rejection here.
		x, shared := v.refThrough(f, b)
		if !shared {
			f.trap("set field %s on %T", name, base)
		}
		v.setField(f, x, name, val)
	case *runtime.IndexRef:
		// interior write through an index select crossing a map
		// element (m[k].f[0].x = v on a slice-of-struct field): the
		// element's storage is shared through the slice, so the field
		// write lands — through an array element it stays rejected.
		x, shared := v.refThrough(f, b)
		if !shared {
			f.trap("set field %s on %T", name, base)
		}
		v.setField(f, x, name, val)
	default:
		f.trap("set field %s on %T", name, base)
	}
}

func (v *VM) index(f *frame, base, idx runtime.Value) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		return v.index(f, dv, idx)
	}
	switch base.(type) {
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		// A ref the plain deref cannot resolve may still reach a
		// readable value — a chain crossing a map element (m[k].a[i],
		// where m[k] is a copy but the slice field shares backing)
		// resolves the same hops refThrough walks for the store side.
		if dv, ok := v.refThrough(f, base); ok {
			return v.index(f, dv, idx)
		}
	}
	rawIdx := idx                               // the declared key check needs the operand's named tag
	idx = runtime.Unwrap(v.materialize(f, idx)) // named key/index types hash as their value
	base = v.materialize(f, base)               // `const s = "x"; s[0]` indexes a UConst
	switch b := base.(type) {
	case *runtime.Named:
		return v.index(f, b.V, rawIdx)
	case *runtime.IfaceNil:
		return v.index(f, &runtime.TypedNil{Typ: b.Typ}, rawIdx)
	case *runtime.TypedNil:
		switch b.Typ.Kind {
		case runtime.KindMap:
			// the key's declared-type check is static — a nil-map read
			// still rejects `B{1}` against map[A]V like gc.
			v.checkMapKey(f, b.Typ, rawIdx)
			return v.mapZero(f, b.Typ) // reading a nil map yields the zero value
		case runtime.KindSlice:
			panic(runtime.BoundsPanic(runtime.Unwrap(idx), 0))
		case runtime.KindPointer:
			// p[i] on a nil *[N]T dereferences the pointer
			panic(runtime.NilDerefPanic())
		default:
			f.trap("index on nil %s", tdName(b.Typ))
		}
	case *runtime.Slice:
		i, ok := hostbridge.IndexSmallInt(idx)
		if !ok {
			if u, isU := hostbridge.UintOperand(idx); isU {
				panic(runtime.BoundsPanic(u, int(b.Len())))
			}
			f.trap("slice index is %T", idx)
		}
		if i < 0 && v.boundUnsigned(rawIdx) {
			// a wide unsigned index wrapped below zero — report its
			// uint64 magnitude like gc.
			panic(runtime.BoundsPanic(uint64(i), int(b.Len())))
		}
		if b.Virtual() {
			// a virtual zero-size slice vends its shared element value.
			if l := b.Len(); i < 0 || i >= l {
				panic(runtime.BoundsPanic(i, int(l)))
			}
			return v.elemRead(f, b.Typ, b.Zero)
		}
		return v.elemRead(f, b.Typ, b.Elems[i])
	case *runtime.Map:
		val, found := b.Get(v.mapKeyOperand(f, b.Typ, rawIdx))
		if !found {
			val = v.mapZero(f, b.Typ)
		}
		return v.elemRead(f, b.Typ, val)
	case string:
		i, ok := hostbridge.IndexSmallInt(idx)
		if !ok {
			if u, isU := hostbridge.UintOperand(idx); isU {
				panic(runtime.BoundsPanic(u, len(b)))
			}
			f.trap("string index is %T", idx)
		}
		if i < 0 && v.boundUnsigned(rawIdx) {
			panic(runtime.BoundsPanic(uint64(i), len(b)))
		}
		return runtime.Tag(v.builtinTypedef("uint8"), int64(b[i]))
	case *runtime.GoValue:
		// a boxed host named map or slice (url.Values, http.Header,
		// net.IP) keeps its box for method dispatch; index the host
		// value itself.
		if val, _, ok := marshalHost.HostIndex(v.indexHooks(f), b, rawIdx); ok {
			return val
		}
		f.trap("index on %T", base)
	default:
		f.trap("index on %T", base)
		return nil
	}
	return nil
}

// hostIndex reads b[idx] on a boxed host map, slice, array or string. ok
// is false when b holds none of those; found is the map comma-ok.
// hostIndexInt reads idx as an in-range index into the host slice, array
// or string rv, panicking like a script slice index when it is out of
// range (wide unsigned indices included).
// hostMapKey converts idx to the key type of the host map rv.
// hostSetIndex writes b[idx] = val on a boxed host map or slice
// (http.Header, url.Values, net.IP), converting val to the element type.
// ok is false when b holds neither; a host array value is a copy, so
// writing it is not offered.
// hostElemRef returns b[idx] as a box over the host value itself when
// the element is a slice or map, so a nested write (q["tag"][0] = v,
// h[k][i] = v) reaches host storage the way Go's shared backing does. ok
// is false for any other base or element shape.
// indexHooks bundles the frame-side operations the hostbridge host
// index/slice ops need: trap position, constant materialization, and the
// unsigned-domain bound checks.
func (v *VM) indexHooks(f *frame) *hostbridge.IndexHooks {
	return &hostbridge.IndexHooks{
		VC:   v,
		Trap: f.trap,
		Materialize: func(x runtime.Value) runtime.Value {
			return v.materialize(f, x)
		},
		BoundUnsigned: v.boundUnsigned,
		Bounds: func(lo, hi runtime.Value, n int64) (hostbridge.BoundOperand, hostbridge.BoundOperand) {
			return v.bounds(f, lo, hi, n)
		},
		MaxBound: func(max runtime.Value, capN int64) hostbridge.BoundOperand {
			return v.maxBound(f, max, capN)
		},
	}
}

// hostBoxOf peels the pointers, refs and names off v to the boxed host
// value behind it (a *Cell holding an http.Header reads as the header).
// The walk is bounded so a pointer cycle cannot spin.
func hostBoxOf(v runtime.Value) (*runtime.GoValue, bool) {
	for range 8 {
		switch x := v.(type) {
		case *runtime.GoValue:
			return x, true
		case *runtime.Named:
			v = x.V
			continue
		}
		dv, ok := runtime.Deref(v)
		if !ok {
			return nil, false
		}
		v = dv
	}
	return nil, false
}

// indexAlias is v.index for the container operand of a nested store or
// address-of (`h[k][i] = v`, `&h[k][i]`, a tuple assignment's pinned
// operand): when base is a boxed host map or slice whose element is a
// slice or map, the element stays boxed over its host storage — the
// plain read hands back a script copy a write would not survive. Any
// other base reads as v.index does.
func (v *VM) indexAlias(f *frame, base, idx runtime.Value) runtime.Value {
	if gv, ok := hostBoxOf(base); ok {
		if ev, ok := marshalHost.HostElemRef(v.indexHooks(f), gv, idx); ok {
			return ev
		}
	}
	return v.index(f, base, idx)
}

// readElemRef reads the element or field a pointer value names when only
// the VM's own reads resolve it — IndexRef.Get and FieldRef.Get know
// script storage alone, not a boxed host slice. ok is false for any
// other pointer.
func (v *VM) readElemRef(f *frame, ptr runtime.Value) (runtime.Value, bool) {
	switch r := ptr.(type) {
	case *runtime.IndexRef:
		return v.index(f, r.Base, r.Key), true
	case *runtime.FieldRef:
		return v.selectMember(f, r.Base, r.Name), true
	}
	return nil, false
}

// HostMapDelete runs the delete builtin on a boxed host map; ok is false
// when m does not hold a map.
func (v *VM) HostMapDelete(m *runtime.GoValue, key runtime.Value) (ok bool) {
	return marshalHost.HostMapDelete(v.indexHooks(v.topFrame()), m, key)
}

// HostClear runs the clear builtin on a boxed host map or slice; ok is
// false when m holds neither.
func (v *VM) HostClear(m *runtime.GoValue) (ok bool) {
	return marshalHost.HostClear(m)
}

// when m does not hold a map.
// HostClear runs the clear builtin on a boxed host map or slice; ok is
// false when m holds neither.
// hostSlice evaluates b[lo:hi(:max)] on a boxed host slice or string. The
// result keeps the host type (net.IP's ip[:2] is a net.IP), so a named
// slice stays boxed for its methods.
// fieldTypedefs returns declared field types for a struct typedef (nil
// when the hook is unset or resolution fails — coerce passes through).
func (v *VM) fieldTypedefs(td *runtime.TypeDef) []*runtime.TypeDef {
	if td == nil || v.H.FieldTypes == nil {
		return nil
	}
	fts, err := v.H.FieldTypes(td)
	if err != nil {
		return nil
	}
	return fts
}

// promoted is the promoted (embedded) member that won the breadth-first
// search in promotedMember — exactly one shape is set:
//
//   - st+idx: a field — st.Fields[idx] is the value
//   - m+recv+mst+midx: a method — recv is the embedded field value it
//     binds to for a value receiver; a pointer receiver binds a
//     FieldRef over mst's midx field instead, so `*p = x` writes reach
//     the stored field rather than a copy of it
//   - recv only (m == nil): an interface-typed embedded field — the
//     member dispatches dynamically on the stored value
//   - host: a value reached through a host-embedded field — the member
//     still has to be selected on it (host fields and methods share the
//     namespace there)
type promoted struct {
	st   *runtime.Struct
	idx  int
	m    *runtime.Function
	recv runtime.Value
	mst  *runtime.Struct
	midx int
	host runtime.Value
}

// methCand is a promoted method found at one BFS level: the declared
// function plus the embedded field slot it binds to.
type methCand struct {
	m   *runtime.Function
	st  *runtime.Struct
	idx int
}

// promotedMember locates `name` among s's promoted (embedded) members —
// fields, methods, interface-stored values and host members — using the
// same breadth-first layering as inspect.MethodSetOf: the shallowest
// embed depth wins and multiple hits at the same level trap as
// ambiguous (Go rejects them at compile time). Host-typed embeds
// (sync.Mutex, sync.Pool, ...) join the search as leaves — their
// promoted surface is the boxed value's exported fields and methods, so
// a name promoted through a host embed and a script embed at the same
// depth is ambiguous too. Members that exist only through a nil
// embedded pointer are recorded as they cross the nil link: a nil path
// strictly shallower than every real hit still wins — Go selects it,
// then a field read or value-receiver call panics on the implicit
// dereference while a pointer-receiver method binds the nil — and a
// same-depth tie is ambiguous like Go's compile-time rejection.
// allowPtr controls whether *T embeds are traversed — composite-literal
// keys forbid pointer indirection (Go reports "invalid implicit pointer
// indirection"), while selector access allows it.
func (v *VM) promotedMember(f *frame, s *runtime.Struct, name string, allowPtr bool) (*promoted, bool) {
	return v.promotedMemberEx(f, s, name, allowPtr, true, -1)
}

// promotedMemberEx is promotedMember with the field side optionally
// already decided by the declaration: scanFields=false skips the
// per-level field-name scan (the caller knows the name is no field —
// the embedded subtrees still queue, since methods and host or
// interface members live deeper), and maxWin >= 0 caps the search at a
// declared field's winning depth — a member at exactly maxWin ties it
// (ambiguous, like Go's compile-time rejection) while deeper members
// cannot outrank it, so the walk stops.
func (v *VM) promotedMemberEx(f *frame, s *runtime.Struct, name string, allowPtr, scanFields bool, maxWin int) (*promoted, bool) {
	if v.H.ResolveType == nil || v.H.IfaceReqs == nil {
		return nil, false
	}
	// an unexported selector names (call package, name): members of
	// the same name declared in another package do not answer, and
	// requirement/method sets are probed by the pkg-qualified key
	// reqKey writes (M4-2b).
	sp := selPkg(f)
	key := selKey(sp, name)
	var hostHits []hostHit
	level := []*runtime.Struct{s}
	// nilDepth/nilPaths track resolutions that exist only through a nil
	// embedded pointer: Go selects members statically, so such a member
	// is not "undefined" — reaching it binds a nil receiver or panics
	// on the implicit dereference (and a same-depth tie with a real
	// path is ambiguous, like Go's compile-time rejection). Members
	// behind the nil are never bindable — reaching them dereferences
	// the nil pointer, so a winning nil path always panics.
	nilDepth, nilPaths := 0, 0
	for depth := 0; len(level) > 0 && depth < 32 && (maxWin < 0 || depth < maxWin); depth++ {
		var hits []slot
		var methCands []methCand
		var ifaceCands []runtime.Value
		var next []*runtime.Struct
		for _, st := range level {
			if st == nil || st.Def == nil {
				continue
			}
			for k, spec := range st.Def.EmbedSpecs {
				var ptr bool
				texpr := spec
				if sx, ok := spec.(*ast.StarExpr); ok {
					ptr = true
					texpr = sx.X
				}
				if ptr && !allowPtr {
					continue
				}
				if k >= len(st.Def.EmbedIdx) {
					continue
				}
				idx := st.Def.EmbedIdx[k]
				embTd, err := v.H.ResolveType(st.Def, texpr)
				if err != nil || embTd == nil {
					continue
				}
				raw := embTd
				embTd = v.peelNamed(embTd)
				if embTd == nil {
					continue
				}
				if embTd.Kind == runtime.KindInterface {
					// an interface embed promises its method set
					// through the stored value — it competes at the
					// same depth as fields and declared methods, and
					// dispatches dynamically on the concrete value.
					if idx < len(st.Fields) {
						if reqs, err := v.H.IfaceReqs(embTd); err == nil && selHas(reqs, key) {
							ifaceCands = append(ifaceCands, st.Fields[idx])
						}
					}
					continue
				}
				if embTd.HostNew != nil {
					// a host-typed embed is a leaf: name is promoted
					// when the boxed type exposes it — checked at type
					// level so a nil stored pointer still resolves (and
					// then panics on the dereference, via nilPaths).
					// inner adds the host type's own embedding depth:
					// a member promoted inside it (parse.Tree.Root)
					// lands below a shallower script member. A defined
					// type (type MyMutex sync.Mutex) carries the fields
					// but not the methods — its method set starts empty.
					if idx >= len(st.Fields) {
						continue
					}
					inner := hostbridge.HostMemberInner(embTd.HostNew(), name, embTd == raw)
					if embTd != raw {
						// a defined type over a host type keeps the methods
						// promoted through the host's anonymous fields
						// (gc: Wrap.M on `type Wrap Base` — M4-2c).
						if pi := hostbridge.HostPromotedMethodInner(embTd.HostNew(), name); pi >= 0 && (inner < 0 || pi < inner) {
							inner = pi
						}
					}
					if inner < 0 {
						continue
					}
					abs := depth + 1 + inner
					recv := st.Fields[idx]
					if ptr && hostbridge.HostNilEmbed(recv) && !hostbridge.HostNilCallable(embTd.HostNew(), name) {
						// reachable only through a nil embedded
						// pointer — same accounting as a nil
						// script-embed path.
						if nilDepth == 0 || abs < nilDepth {
							nilDepth, nilPaths = abs, 1
						} else if abs == nilDepth {
							nilPaths++
						}
						continue
					}
					hostHits = append(hostHits, hostHit{abs, runtime.Unwrap(recv)})
					continue
				}
				// a promoted method competes at the same depth as a
				// field — the receiver is the embedded field value
				// itself. Only the embed's own methods count — a
				// defined type does not inherit the underlying type's
				// methods — except through an alias, which IS its
				// target type (`type A = T` promotes T's methods).
				mtd := raw
				if raw.Kind == runtime.KindAlias {
					mtd = embTd
				}
				if m, ok := mtd.Methods[name]; ok && idx < len(st.Fields) && selVisible(sp, mtd.Pkg, name) {
					methCands = append(methCands, methCand{m, st, idx})
				}
				if len(embTd.Fields) == 0 {
					// non-struct embeds (interfaces, basics) promote
					// nothing else.
					continue
				}
				j := -1
				if scanFields {
					for fi, fn := range embTd.Fields {
						if fn == name && selVisible(sp, embTd.Pkg, fn) {
							j = fi
							break
						}
					}
				}
				if j < 0 {
					inner, _ := v.embedValue(f, st, idx, embTd, ptr)
					if inner != nil {
						next = append(next, inner)
						continue
					}
					if ptr {
						// a nil embedded pointer cannot be searched
						// by value — ask the type whether the member
						// exists deeper, and at which depth. Field
						// hits are only recorded when the field side
						// is still open: a decided field path's own
						// nil links panic in fieldAtPath instead.
						if scanFields {
							if d, c := v.embedTypeDepth(embTd, name, sp, map[*runtime.TypeDef]bool{}); d > 0 {
								abs := depth + 1 + d
								if nilDepth == 0 || abs < nilDepth {
									nilDepth, nilPaths = abs, c
								} else if abs == nilDepth {
									nilPaths += c
								}
							}
						}
						if d, c := v.embedTypeMethodDepth(embTd, name, sp, map[*runtime.TypeDef]bool{}); d > 0 {
							abs := depth + 1 + d
							if nilDepth == 0 || abs < nilDepth {
								nilDepth, nilPaths = abs, c
							} else if abs == nilDepth {
								nilPaths += c
							}
						}
					}
					continue
				}
				inner, ok := v.embedValue(f, st, idx, embTd, ptr)
				if !ok {
					// found only through a nil embedded pointer — Go
					// panics on the implicit dereference.
					panic(runtime.NilDerefPanic())
				}
				hits = append(hits, slot{inner, j})
			}
		}
		// host members due at this level compete now; deeper ones
		// stay pending for a later level.
		var thisHost []runtime.Value
		keep := hostHits[:0]
		for _, hh := range hostHits {
			if hh.abs <= depth+1 {
				thisHost = append(thisHost, hh.recv)
			} else {
				keep = append(keep, hh)
			}
		}
		hostHits = keep
		total := len(hits) + len(thisHost) + len(methCands) + len(ifaceCands)
		if total > 0 {
			if nilDepth > 0 && nilDepth <= depth+1 {
				// a nil path at this level makes the selector
				// ambiguous; a strictly shallower nil member wins
				// outright — Go selects it, then the nil binds or
				// panics.
				if nilDepth == depth+1 || nilPaths > 1 {
					f.trap("ambiguous selector %s", name)
				}
				panic(runtime.NilDerefPanic())
			}
			if maxWin >= 0 && depth+1 == maxWin {
				// a member at the declared field's depth ties it —
				// Go rejects the selector as ambiguous.
				f.trap("ambiguous selector %s", name)
			}
			if total > 1 {
				f.trap("ambiguous selector %s", name)
			}
			switch {
			case len(thisHost) == 1:
				return &promoted{host: thisHost[0]}, true
			case len(methCands) == 1:
				mc := methCands[0]
				return &promoted{m: mc.m, recv: mc.st.Fields[mc.idx], mst: mc.st, midx: mc.idx}, true
			case len(ifaceCands) == 1:
				return &promoted{recv: ifaceCands[0]}, true
			default:
				return &promoted{st: hits[0].st, idx: hits[0].idx}, true
			}
		}
		if nilDepth > 0 && nilDepth <= depth+1 {
			if nilPaths > 1 || nilDepth == maxWin {
				// several nil paths, or a nil-path member tying the
				// declared field's depth — ambiguous like Go.
				f.trap("ambiguous selector %s", name)
			}
			panic(runtime.NilDerefPanic())
		}
		level = next
	}
	if len(hostHits) > 0 {
		// pending host members deeper than any walkable level —
		// the shallowest of them wins now.
		minAbs := hostHits[0].abs
		for _, hh := range hostHits[1:] {
			if hh.abs < minAbs {
				minAbs = hh.abs
			}
		}
		if maxWin >= 0 && minAbs >= maxWin {
			// a host member at the declared field's depth ties it
			// (ambiguous); a deeper one cannot outrank it.
			if minAbs == maxWin {
				f.trap("ambiguous selector %s", name)
			}
			return nil, false
		}
		if nilDepth > 0 && nilDepth <= minAbs {
			if nilDepth == minAbs || nilPaths > 1 {
				f.trap("ambiguous selector %s", name)
			}
			panic(runtime.NilDerefPanic())
		}
		var recv runtime.Value
		n := 0
		for _, hh := range hostHits {
			if hh.abs == minAbs {
				n++
				recv = hh.recv
			}
		}
		if n > 1 {
			f.trap("ambiguous selector %s", name)
		}
		return &promoted{host: recv}, true
	}
	if nilDepth > 0 && (maxWin < 0 || nilDepth <= maxWin) {
		if nilPaths > 1 || nilDepth == maxWin {
			f.trap("ambiguous selector %s", name)
		}
		panic(runtime.NilDerefPanic())
	}
	return nil, false
}

type slot struct {
	st  *runtime.Struct
	idx int
}

// hostHit is a host member's pending resolution: the absolute depth at
// which it would win — which includes the embedding inside the host
// type itself (template.Template → *parse.Tree → Root), so a hit can
// land below the level that discovered it — and the embedded value to
// select on.
type hostHit struct {
	abs  int
	recv runtime.Value
}

// hostMemberInner reports how deeply name is promoted inside the boxed
// type — 0 for a direct exported field or a method of the slot type,
// higher when it resolves through the host's own embeds
// (*template.Template → *parse.Tree → Root is inner 1), -1 when absent.
// Answered at type level so a zero whose anonymous pointer fields are
// nil still resolves; only actual member access dereferences the stored
// value. methods is false for a defined type over a host type: it
// carries fields, not the method set.
// hostMethodInner counts the embed levels inside the host type that
// name's method is promoted through — 0 when it is declared on t's own
// method set. t is the slot's declared type: a *T slot contributes the
// methods of *T, including pointer receivers of embedded values.
// methodInner: name is in t's method set — how deeply inside t it is
// promoted. Each anonymous field contributes its own type's method set
// — *E when the chain went through a pointer, an interface as itself —
// and the shallowest path wins. reflect cannot tell a method declared
// on t from one promoted through an embed, so a name found in a
// child's set counts via that child; contributed types are visited
// once per path so a recursively-embedded host type (struct{ *T })
// terminates instead of overflowing the stack.
// hostNilCallable reports whether name is a pointer-receiver method on
// the boxed type — the only member callable through a nil stored
// pointer: Go passes the nil receiver straight to the method, while
// fields and value-receiver methods must dereference it first.
// hostNilEmbed reports whether an embedded host-typed field's stored
// value is nil — a TypedNil/IfaceNil slot or a nil pointer box — so a
// member resolved through it panics on the implicit dereference.
// embedTypeDepth answers for the type what a value search cannot when an
// embedded pointer is nil: whether `name` is promoted through td's own
// embedded fields, the shallowest relative depth at which it resolves
// (1 = a direct field of a type embedded in td), and how many distinct
// paths reach it at that depth. Depth 0 means unreachable.
func (v *VM) embedTypeDepth(td *runtime.TypeDef, name string, selPkg *runtime.Package, seen map[*runtime.TypeDef]bool) (int, int) {
	if td == nil || seen[td] || v.H.ResolveType == nil {
		return 0, 0
	}
	seen[td] = true
	defer delete(seen, td)
	best, cnt := 0, 0
	for k, spec := range td.EmbedSpecs {
		texpr := spec
		if sx, ok := spec.(*ast.StarExpr); ok {
			texpr = sx.X
		}
		if k >= len(td.EmbedIdx) {
			continue
		}
		et, err := v.H.ResolveType(td, texpr)
		if err != nil || et == nil {
			continue
		}
		raw := et
		et = v.peelNamed(et)
		if et == nil {
			continue
		}
		d, c := 0, 1
		if et.HostNew != nil {
			// a host-typed leaf resolves name on the boxed type's
			// exported field/method surface — plus the host's own
			// internal embed depth.
			if inner := hostbridge.HostMemberInner(et.HostNew(), name, et == raw); inner >= 0 {
				d = 1 + inner
			}
		} else {
			for _, fn := range et.Fields {
				if fn == name && selVisible(selPkg, et.Pkg, fn) {
					d = 1
					break
				}
			}
			if d == 0 {
				if sd, sc := v.embedTypeDepth(et, name, selPkg, seen); sd > 0 {
					d, c = 1+sd, sc
				}
			}
		}
		if d == 0 {
			continue
		}
		if best == 0 || d < best {
			best, cnt = d, c
		} else if d == best {
			cnt += c
		}
	}
	return best, cnt
}

// embedTypeMethodDepth is embedTypeDepth for methods: whether `name`
// promotes through td's own embedded types, the shallowest relative
// depth at which it resolves (1 = a method declared on a type embedded
// in td), and how many distinct paths reach it at that depth. Any
// member found this way lives inside the nil pointer that triggered the
// type-level walk — reaching its receiver dereferences the nil, so the
// hit is never bindable: it counts for depth and ambiguity and panics
// on use. Depth 0 means unreachable.
func (v *VM) embedTypeMethodDepth(td *runtime.TypeDef, name string, selPkg *runtime.Package, seen map[*runtime.TypeDef]bool) (int, int) {
	if td == nil || seen[td] || v.H.ResolveType == nil || v.H.IfaceReqs == nil {
		return 0, 0
	}
	key := selKey(selPkg, name)
	seen[td] = true
	defer delete(seen, td)
	best, cnt := 0, 0
	for k, spec := range td.EmbedSpecs {
		texpr := spec
		if sx, ok := spec.(*ast.StarExpr); ok {
			texpr = sx.X
		}
		if k >= len(td.EmbedIdx) {
			continue
		}
		et, err := v.H.ResolveType(td, texpr)
		if err != nil || et == nil {
			continue
		}
		raw := et
		et = v.peelNamed(et)
		if et == nil {
			continue
		}
		d, c := 0, 1
		switch {
		case et.Kind == runtime.KindInterface:
			// an embedded interface promises name through the
			// stored value — it exists at this depth.
			if reqs, err := v.H.IfaceReqs(et); err == nil && selHas(reqs, key) {
				d = 1
			}
		case et.HostNew != nil:
			if inner := hostbridge.HostMemberInner(et.HostNew(), name, et == raw); inner >= 0 {
				d = 1 + inner
			} else if et != raw {
				// defined type over a host type: promoted anonymous-field
				// methods still answer (M4-2c).
				if inner := hostbridge.HostPromotedMethodInner(et.HostNew(), name); inner >= 0 {
					d = 1 + inner
				}
			}
		default:
			// only the embed's own methods — a defined type does not
			// inherit the underlying type's methods — except through
			// an alias, which IS its target type.
			mtd := raw
			if raw.Kind == runtime.KindAlias {
				mtd = et
			}
			if _, ok := mtd.Methods[name]; ok && selVisible(selPkg, mtd.Pkg, name) {
				d = 1
			}
		}
		if d == 0 {
			if sd, sc := v.embedTypeMethodDepth(et, name, selPkg, seen); sd > 0 {
				d, c = 1+sd, sc
			}
		}
		if d == 0 {
			continue
		}
		if best == 0 || d < best {
			best, cnt = d, c
		} else if d == best {
			cnt += c
		}
	}
	return best, cnt
}

// embedValue returns the struct value stored in an embedded field,
// dereferencing pointer embeds (nil reports false so the caller decides
// between skipping a deeper search and panicking on a hit) and
// materializing a NIL-initialized non-pointer embed in place so field
// writes through it land.
func (v *VM) embedValue(f *frame, st *runtime.Struct, i int, td *runtime.TypeDef, ptr bool) (*runtime.Struct, bool) {
	if i < 0 || i >= len(st.Fields) {
		return nil, false
	}
	val := st.Fields[i]
	if ptr {
		dv, ok := runtime.Deref(val)
		if !ok {
			return nil, false
		}
		val = dv
	}
	if n, ok := val.(*runtime.Named); ok {
		val = n.V
	}
	if s, ok := val.(*runtime.Struct); ok {
		return s, true
	}
	if _, isNil := val.(runtime.Nil); isNil || val == nil {
		z := v.zeroValue(f, td)
		st.Fields[i] = z
		if n, ok := z.(*runtime.Named); ok {
			z = n.V
		}
		if s, ok := z.(*runtime.Struct); ok {
			return s, true
		}
	}
	return nil, false
}

// elemTypedef resolves the element type of a container typedef (nil when
// unresolvable — callers then pass values through or reject).
func (v *VM) elemTypedef(f *frame, td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || v.H.ElemOf == nil {
		return nil
	}
	et, err := v.H.ElemOf(v.peelNamed(td))
	if err == nil && et != nil {
		return et
	}
	// the element may be named by a function-local `type` decl — the
	// package index cannot see those, but the typedef sits in this
	// frame's locals like any other local constant.
	if f != nil {
		return v.localElemTypedef(f, td)
	}
	return nil
}

// localElemTypedef resolves a container's element typedef when its AST
// names a function-local type: walk the underlying type expression to the
// element name, then find that typedef among the frame's locals/upvals.
func (v *VM) localElemTypedef(f *frame, td *runtime.TypeDef) *runtime.TypeDef {
	u := v.peelNamed(td)
	x := u.TypeExpr()
	for x != nil {
		switch t := x.(type) {
		case *ast.ParenExpr:
			x = t.X
		case *ast.Ellipsis:
			x = t.Elt
		case *ast.StarExpr:
			x = t.X
		case *ast.ArrayType:
			x = t.Elt
		case *ast.MapType:
			x = t.Value
		case *ast.ChanType:
			x = t.Value
		default:
			if id, ok := x.(*ast.Ident); ok {
				return v.localTypedefOf(f, id.Name)
			}
			return nil
		}
	}
	return nil
}

// localTypedefOf finds a typedef value bound as a local constant — how a
// `type` decl inside a function body stores its typedef.
func (v *VM) localTypedefOf(f *frame, name string) *runtime.TypeDef {
	for i := range f.locals {
		if td, ok := f.localVal(i).(*runtime.TypeDef); ok && td.Name == name {
			return td
		}
	}
	for _, c := range f.upvals {
		if c == nil {
			continue
		}
		if td, ok := c.Elem.(*runtime.TypeDef); ok && td.Name == name {
			return td
		}
	}
	return nil
}

// mapKeyTypedef returns the declared key typedef of a map typedef —
// `map[K]V` yields K — nil when the key type cannot be resolved (the
// operand then passes through unchecked, like a missing element type).
func (v *VM) mapKeyTypedef(f *frame, td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || v.H.ResolveType == nil {
		return nil
	}
	u := v.peelNamed(td)
	if u == nil || u.Kind != runtime.KindMap {
		return nil
	}
	x := u.TypeExpr()
	mt, ok := x.(*ast.MapType)
	if !ok {
		return nil
	}
	if kt, err := v.resolveOperandType(u, mt.Key); err == nil && kt != nil {
		return kt
	}
	// a key named by a function-local `type` decl — the package index
	// cannot see those, but the typedef sits in this frame's locals.
	if f != nil {
		if id, ok := mt.Key.(*ast.Ident); ok {
			return v.localTypedefOf(f, id.Name)
		}
	}
	return nil
}

// mapKeyOperand coerces a map index operand to the map's declared key
// type. `m[k]` type-checks k against K statically — `B{1}` against
// map[A]int is gc's compile reject even though the structs share a
// shape — so the check runs before the map's value/nil paths see the
// operand. The caller passes the un-materialized operand: a named key
// type's tag is what the check needs.
func (v *VM) mapKeyOperand(f *frame, td *runtime.TypeDef, idx runtime.Value) runtime.Value {
	if kt := v.mapKeyTypedef(f, td); kt != nil {
		k := v.coerce(f, idx, kt)
		if kt = v.peelAlias(kt); kt.Kind == runtime.KindInterface {
			// an interface key keeps its dynamic type: the map keys
			// it by (type, value) (runtime.IfaceKeyOf) and ranging
			// yields it typed.
			return k
		}
		u := runtime.Unwrap(k)
		if n, ok := k.(*runtime.Named); ok && hostbridge.BasicKeyPayload(u) {
			// a named basic key (`map[K]V`, map[time.Month]V) keeps its
			// tag: the map hashes the payload (runtime.CanonicalKey
			// unwraps), while range, fmt and every reader of the
			// stored key see a K, not the bare int.
			return n
		}
		// a boxed host named basic under its twin's key type keys like
		// the twin's values: m[as.Tok] finds m[token.DEFINE] — and is
		// stored as one, tagged like the twin's constants
		// (m[t.Month()] ranges as a time.Month).
		if !isGoValue(u) {
			return u
		}
		p, rt, ok := runtime.HostNamedScalar(u)
		if !ok || !v.hostTypedefIs(rt, kt) {
			return u
		}
		if kt.HostScalar == nil && kt.HostNew != nil {
			// a bound host enum without a scalar (hostbridge.Kind) keeps
			// its GoValue box: the named display and identity live in
			// the box, a bare payload key would lose both.
			return u
		}
		if kt.HostNew != nil {
			return p
		}
		return runtime.Tag(kt, p)
	}
	return runtime.Unwrap(v.materialize(f, idx))
}

// basicKeyPayload reports whether x, a [runtime.Named] key's payload, is a
// basic scalar — a script int64/string/... or a bound host scalar such as
// a raw time.Month — rather than a composite or a host box.
// ifaceKeyed reports whether the map typedef td declares an interface
// key type — a map made of it keys by (dynamic type, value)
// ([runtime.Map.IfaceKey]).
func (v *VM) ifaceKeyed(f *frame, td *runtime.TypeDef) bool {
	kt := v.mapKeyTypedef(f, td)
	if kt == nil {
		return false
	}
	kt = v.peelAlias(kt)
	return kt != nil && kt.Kind == runtime.KindInterface
}

// NewMap implements [runtime.VMCaller].
func (v *VM) NewMap(td *runtime.TypeDef) *runtime.Map {
	return runtime.NewMap(td, v.ifaceKeyed(v.topFrame(), td))
}

// checkMapKey runs the declared-key-type check and drops the operand —
// a nil-map read or write still rejects a mismatched key statically,
// before the nil-map panic or zero value fires.
func (v *VM) checkMapKey(f *frame, td *runtime.TypeDef, idx runtime.Value) {
	v.mapKeyOperand(f, td, idx)
}

// MapKeyOperand implements [runtime.VMCaller].
func (v *VM) MapKeyOperand(mapTd *runtime.TypeDef, key runtime.Value) runtime.Value {
	// the caller's frame resolves function-local key typedefs, so the
	// builtin sees the same key normalization an index expression produces.
	return v.mapKeyOperand(v.topFrame(), mapTd, key)
}

// elemRead coerces a container element read to its declared element
// typedef — b[i] on []byte is uint8-typed, not a bare int64, so %T and
// cross-type assignment see the element's real type.
func (v *VM) elemRead(f *frame, cont *runtime.TypeDef, x runtime.Value) runtime.Value {
	if cont == nil {
		return x
	}
	if _, ok := x.(*runtime.Named); ok {
		return x // stored value already carries its tag
	}
	et := v.elemTypedef(f, cont)
	if et == nil {
		return x
	}
	return v.coerce(f, x, et)
}

// mapZero is the value a map read produces for a missing key or a nil
// map: the zero of the map's element typedef when the type is known,
// NIL otherwise.
func (v *VM) mapZero(f *frame, td *runtime.TypeDef) runtime.Value {
	if td == nil || v.H.ElemOf == nil {
		return runtime.NIL
	}
	// `type M2 M` maps: ElemOf on the declared name resolves to the
	// underlying typedef, so peel first to reach the element type.
	et, err := v.H.ElemOf(v.peelNamed(td))
	if err != nil || et == nil {
		// a function-local element type falls back to frame locals.
		et = v.localElemTypedef(f, td)
	}
	if et == nil {
		return runtime.NIL
	}
	return v.zeroValue(f, et)
}

// indexOK implements the comma-ok form `v, ok := m[k]`: for maps ok is
// whether the key is present; other indexables always report ok.
func (v *VM) indexOK(f *frame, base, idx runtime.Value) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		return v.indexOK(f, dv, idx)
	}
	rawIdx := idx // the declared key check needs the operand's named tag
	// an untyped-const key (`const k = "x"; v, ok := m[k]`) hashes as
	// its materialized value, like index's plain read
	idx = runtime.Unwrap(v.materialize(f, idx))
	if n, ok := base.(*runtime.Named); ok {
		return v.indexOK(f, n.V, rawIdx)
	}
	if in, ok := base.(*runtime.IfaceNil); ok {
		return v.indexOK(f, &runtime.TypedNil{Typ: in.Typ}, rawIdx)
	}
	if tn, ok := base.(*runtime.TypedNil); ok {
		if tn.Typ.Kind == runtime.KindMap {
			v.checkMapKey(f, tn.Typ, rawIdx)
			return &runtime.Tuple{Elems: []runtime.Value{v.mapZero(f, tn.Typ), false}}
		}
		return &runtime.Tuple{Elems: []runtime.Value{v.index(f, base, idx), true}}
	}
	if m, ok := base.(*runtime.Map); ok {
		val, found := m.Get(v.mapKeyOperand(f, m.Typ, rawIdx))
		if !found {
			val = v.mapZero(f, m.Typ)
		}
		return &runtime.Tuple{Elems: []runtime.Value{val, found}}
	}
	if gv, ok := base.(*runtime.GoValue); ok {
		if val, found, ok := marshalHost.HostIndex(v.indexHooks(f), gv, rawIdx); ok {
			return &runtime.Tuple{Elems: []runtime.Value{val, found}}
		}
	}
	return &runtime.Tuple{Elems: []runtime.Value{v.index(f, base, rawIdx), true}}
}

func (v *VM) setIndex(f *frame, base, idx, val runtime.Value) {
	if dv, ok := runtime.Deref(base); ok {
		v.setIndex(f, dv, idx, val)
		return
	}
	if n, ok := base.(*runtime.Named); ok {
		v.setIndex(f, n.V, idx, val)
		return
	}
	rawIdx := idx // the declared key check needs the operand's named tag
	idx = runtime.Unwrap(v.materialize(f, idx))
	switch b := base.(type) {
	case *runtime.IfaceNil:
		v.setIndex(f, &runtime.TypedNil{Typ: b.Typ}, rawIdx, val)
	case *runtime.TypedNil:
		switch b.Typ.Kind {
		case runtime.KindMap:
			// a mismatched key rejects statically — before the nil-map
			// assign panic fires like gc's nil-map write would.
			v.checkMapKey(f, b.Typ, rawIdx)
			panic(runtime.NilMapAssignPanic())
		case runtime.KindSlice:
			panic(runtime.BoundsPanic(runtime.Unwrap(idx), 0))
		case runtime.KindPointer:
			panic(runtime.NilDerefPanic())
		default:
			f.trap("index assign on nil %s", tdName(b.Typ))
		}
	case *runtime.Slice:
		i, ok := hostbridge.IndexSmallInt(idx)
		if !ok {
			if u, isU := hostbridge.UintOperand(idx); isU {
				panic(runtime.BoundsPanic(u, int(b.Len())))
			}
			f.trap("slice index is %T", idx)
		}
		if i < 0 && v.boundUnsigned(rawIdx) {
			panic(runtime.BoundsPanic(uint64(i), int(b.Len())))
		}
		// the slice's declared element type constrains the write.
		if et := v.elemTypedef(f, b.Typ); et != nil {
			val = v.coerce(f, val, et)
		}
		if b.Virtual() {
			// a virtual zero-size element can't be observed — the type
			// has a single value — but the bounds check still applies.
			if l := b.Len(); i < 0 || i >= l {
				panic(runtime.BoundsPanic(i, int(l)))
			}
			return
		}
		b.SetElem(i, val)
	case *runtime.Map:
		idx = v.mapKeyOperand(f, b.Typ, rawIdx)
		if idx != nil && !hostbridge.TypeOf(idx).Comparable() {
			f.trap("map key %T is not comparable", idx)
		}
		// the map's declared element type constrains the write.
		if et := v.elemTypedef(f, b.Typ); et != nil {
			val = v.coerce(f, val, et)
		}
		b.Insert(idx, val)
	case *runtime.IndexRef:
		// a nested index lvalue like m[k][i] = v over a map of slices
		// evaluates the outer index against the stored slice — read the
		// ref's element (v.index, not Get: it also yields the map zero
		// for a missing key) and assign through it, sharing the stored
		// backing like Go. refThrough also resolves a base that itself
		// crosses a map element (m[k].f[i][j] over a slice field) — its
		// value is usable for reads even where it reports unwritable.
		bx, _ := v.refThrough(f, b.Base)
		if bx == nil {
			bx = b.Base
		}
		if gv, ok := hostBoxOf(bx); ok {
			// q["tag"][0] = v on a boxed host map or slice: write the
			// host element's own backing, not the script copy
			// v.index would hand back. A base reached through a
			// pointer ((*p)["tag"][0] = v) peels to its box first.
			if ev, ok := marshalHost.HostElemRef(v.indexHooks(f), gv, b.Key); ok {
				v.setIndex(f, ev, rawIdx, val)
				return
			}
		}
		x := v.index(f, bx, b.Key)
		if m := b.Map(); m != nil && !runtime.SharedElem(x) {
			// Go rejects interior writes on a non-reference map
			// element at compile time (m[k] is a copy) — trap
			// instead of mutating the stored value.
			f.trap("index assign on %T", base)
		}
		v.setIndex(f, x, rawIdx, val)
	case *runtime.FieldRef:
		// interior write through a field select crossing a map element
		// (m[k].f[i] = v on a struct element): the field read lands on
		// the element copy, so Go allows it only when the field itself
		// is reference-shaped or reachable through shared storage
		// (m[k].p.a[i] writes the pointee's array) — an array or
		// struct field stays a compile rejection here.
		x, shared := v.refThrough(f, b)
		if x == nil || !shared {
			f.trap("index assign on %T", base)
		}
		v.setIndex(f, x, rawIdx, val)
	case *runtime.GoValue:
		// a boxed host named map or slice (w.Header(), url.Values,
		// net.IP) keeps its box for method dispatch; write the host
		// value itself so the host sees the change.
		if !marshalHost.HostSetIndex(v.indexHooks(f), b, rawIdx, val) {
			f.trap("index assign on %T", base)
		}
	default:
		f.trap("index assign on %T", base)
	}
}

// receiverOf resolves the receiver operand of an unresolvable
// receiver-position ref — an IndexRef emitted for `s[i].M()` whose
// element cannot be reached by storage. A nested base the ref cannot
// walk resolves through refThrough (m[k].f[i] reads the stored copy's
// slice field), then: a map element reads as the Go copy it is (a
// missing key yields the element zero through v.index), while a
// resolvable container re-bases the ref so the element stays
// addressable and a pointer receiver writes shared storage. An element
// that still cannot resolve falls back to the value read, so the
// operand's own bounds or nil panic keeps its Go shape.
func (v *VM) receiverOf(f *frame, base runtime.Value) (dv, recv runtime.Value, ok bool) {
	if rb, isIR := base.(*runtime.IndexRef); isIR {
		bx, _ := v.refThrough(f, rb.Base)
		if bx == nil {
			bx = rb.Base
		}
		if _, isMap := runtime.Unwrap(bx).(*runtime.Map); !isMap {
			nr := &runtime.IndexRef{Base: bx, Key: rb.Key}
			if e, ok := nr.Get(); ok {
				return e, nr, true
			}
		}
		dv = v.index(f, bx, rb.Key)
		return dv, dv, true
	}
	// a FieldRef or DerefRef that cannot materialize (its chain crosses
	// a map element or a host field) still resolves to a value — the
	// member binds the re-based ref when the chain stays writable, the
	// resolved value otherwise.
	if _, isRef := base.(*runtime.FieldRef); isRef {
		x, shared := v.refThrough(f, base)
		if x == nil {
			return nil, nil, false
		}
		if shared {
			return x, base, true
		}
		return x, x, true
	}
	if _, isDR := base.(*runtime.DerefRef); isDR {
		dv, ok := runtime.Deref(base)
		return dv, base, ok
	}
	return nil, nil, false
}

// refThrough resolves an lvalue ref chain to its current value where
// runtime.Deref fails because the chain crosses a map element —
// IndexRef.Get and FieldRef.Get stay gated so `m[k][i] = v` and
// `m[k].f = v` keep their traps. The bool reports whether an interior
// write into the resolved value's storage is legal Go: a map element
// reads as a copy (writable only when the element is reference-shaped
// itself), while a hop into shared storage keeps or restores
// writability — m[k].g.s[i] through a struct field g and a slice field
// s is legal, m[k].g.x is not. A nil value means the chain cannot
// resolve at all.
func (v *VM) refThrough(f *frame, base runtime.Value) (runtime.Value, bool) {
	switch b := base.(type) {
	case *runtime.IndexRef:
		if x, ok := b.Get(); ok {
			return x, true // a resolved element is storage
		}
		if m := b.Map(); m != nil {
			// Get is gated for copies; the raw element read lands on
			// the map element itself (writable only when it is
			// reference-shaped) or the map zero for a missing key.
			// an interface-keyed map hashes the key's dynamic
			// type — keep a Named key's tag like IndexRef.Get.
			k := b.Key
			if !m.IfaceKey {
				k = runtime.Unwrap(k)
			}
			x, found := m.Get(k)
			if !found {
				x = v.index(f, b.Base, b.Key)
			}
			return x, runtime.SharedElem(x)
		}
		bv, shared := v.refThrough(f, b.Base)
		if bv == nil {
			return nil, false
		}
		switch cb := runtime.Unwrap(bv).(type) {
		case *runtime.Map:
			k := b.Key
			if !cb.IfaceKey {
				k = runtime.Unwrap(k)
			}
			x, found := cb.Get(k)
			if !found {
				x = v.index(f, bv, b.Key)
			}
			return x, runtime.SharedElem(x)
		case *runtime.Slice:
			i, ok := runtime.Unwrap(b.Key).(int64)
			if !ok {
				return nil, false
			}
			// raw element read — v.index would copy a struct element
			// and silently lose the write. The element's own shape
			// counts too: m[k].a[i].x writes through when a is
			// [N]*S (a pointer element) even though a itself is a copy.
			x := cb.Elems[i]
			return x, shared || runtime.SharedElem(x)
		}
		x := v.index(f, bv, b.Key)
		return x, shared
	case *runtime.FieldRef:
		bv, shared := v.refThrough(f, b.Base)
		if bv == nil {
			return nil, false
		}
		x := v.selectMember(f, bv, b.Name)
		return x, runtime.SharedElem(x) || shared
	case *runtime.DerefRef:
		return v.refThrough(f, b.Ptr)
	default:
		// A ref chain crossing a package member (pkg.F.G = v,
		// pkg.A[i] = v): the package operand is the shared base —
		// selectMember reads the global cell's live value, so the
		// store lands like the one-level `pkg.V = x` store does.
		switch base.(type) {
		case *runtime.Package, *runtime.ImportRef:
			return base, true
		}
		return runtime.Deref(base)
	}
}

func (v *VM) slice(f *frame, base, lo, hi, max runtime.Value) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		return v.slice(f, dv, lo, hi, max)
	}
	base = v.materialize(f, base) // `const s = "x"; s[i:j]` slices a UConst
	if n, ok := base.(*runtime.Named); ok {
		return v.slice(f, n.V, lo, hi, max)
	}
	three := max != runtime.NIL && max != nil
	switch b := base.(type) {
	case *runtime.IfaceNil:
		return v.slice(f, &runtime.TypedNil{Typ: b.Typ}, lo, hi, max)
	case *runtime.TypedNil:
		if b.Typ.Kind != runtime.KindSlice {
			f.trap("slice on nil %s", tdName(b.Typ))
		}
		// s[0:0] / s[:] on a nil slice is a valid empty result
		l, h := v.bounds(f, lo, hi, 0)
		m := v.maxBound(f, max, 0)
		if r := hostbridge.SliceBoundsReason(l, h, m, 0, three, "capacity"); r != "" {
			panic(runtime.SliceBoundsPanic(r))
		}
		return b
	case *runtime.Slice:
		if b.Virtual() {
			// a virtual zero-size slice bounds-checks against its
			// logical length and capacity like a real one — the
			// two-index high may pass len up to cap — and the
			// sub-slice stays virtual: s[:0] keeps the capacity.
			word := "capacity"
			if _, isArr := v.arrayLen(f, b.Typ); isArr {
				word = "length" // array-typed values report "with length"
			}
			l, h := v.bounds(f, lo, hi, b.Len())
			if three {
				m := v.maxBound(f, max, b.Cap())
				if r := hostbridge.SliceBoundsReason(l, h, m, b.Cap(), true, word); r != "" {
					panic(runtime.SliceBoundsPanic(r))
				}
				return &runtime.Slice{N: h.I - l.I, CapN: m.I - l.I, Zero: b.Zero, Typ: sliceTypOf(b.Typ)}
			}
			if r := hostbridge.SliceBoundsReason(l, h, boundOperand{}, b.Cap(), false, word); r != "" {
				panic(runtime.SliceBoundsPanic(r))
			}
			return &runtime.Slice{N: h.I - l.I, CapN: b.Cap() - l.I, Zero: b.Zero, Typ: sliceTypOf(b.Typ)}
		}
		n := int64(len(b.Elems))
		l, h := v.bounds(f, lo, hi, n)
		_, isArr := v.arrayLen(f, b.Typ)
		word, cp := "capacity", int64(cap(b.Elems))
		if isArr {
			// an array-typed value reports "with length" like gc — the
			// native b.Elems[...] panic says "capacity" regardless.
			word, cp = "length", n
		}
		if three {
			m := v.maxBound(f, max, cp)
			// unsigned operands evaluate in the uint64 domain — the
			// host Elems[...] panic would print their int64 wrap.
			if isArr || l.Unsigned || h.Unsigned || m.Unsigned {
				if r := hostbridge.SliceBoundsReason(l, h, m, cp, true, word); r != "" {
					panic(runtime.SliceBoundsPanic(r))
				}
			}
			return &runtime.Slice{Elems: b.Elems[l.I:h.I:m.I], Typ: sliceTypOf(b.Typ), Host: b.HostReslice3(l.I, h.I, m.I)}
		}
		if isArr || l.Unsigned || h.Unsigned {
			if r := hostbridge.SliceBoundsReason(l, h, boundOperand{}, cp, false, word); r != "" {
				panic(runtime.SliceBoundsPanic(r))
			}
		}
		return &runtime.Slice{Elems: b.Elems[l.I:h.I], Typ: sliceTypOf(b.Typ), Host: b.HostReslice(l.I, h.I)}
	case string:
		if three {
			// a 3-index slice on a string is a compile reject in Go —
			// loud-fail like the other compile-time checks.
			f.trap("cannot slice a string with 3 indices")
		}
		l, h := v.bounds(f, lo, hi, int64(len(b)))
		if l.Unsigned || h.Unsigned {
			if r := hostbridge.SliceBoundsReason(l, h, boundOperand{}, int64(len(b)), false, "length"); r != "" {
				panic(runtime.SliceBoundsPanic(r))
			}
		}
		return b[l.I:h.I]
	case *runtime.GoValue:
		if r, ok := marshalHost.HostSlice(v.indexHooks(f), b, lo, hi, max, three); ok {
			return r
		}
		f.trap("slice on %T", base)
		return nil
	default:
		f.trap("slice on %T", base)
		return nil
	}
}

// boundOperand is one index/slice-bound operand: its int64 position,
// uint64 magnitude and domain flag — defined in internal/hostbridge.
type boundOperand = hostbridge.BoundOperand

// boundOperand is one index/slice-bound operand: its int64 position,
// its uint64 reading, and whether its domain is unsigned. Bounds
// checks and error text evaluate in the operand's own domain so a
// wide unsigned index reports 18446744073709551615, not the int64
// wrap -1.
// indexSmallInt reads an index operand as int64, looking inside a
// host GoValue's box — a host uint32(3) indexes like the constant 3,
// while unsigned widths that cannot fit int64 keep the boxed-wide
// out-of-bounds panic path.
// hostUint reads a host-boxed unsigned integer of any width as
// uint64. Host values arrive carrying their concrete type — uint,
// uint8..32, uintptr, uint64 — where script integers ride int64.
// boundUnsigned reports whether the operand's declared domain is an
// unsigned integer width — a Named tag's sized name or a boxed host
// unsigned.
func (v *VM) boundUnsigned(x runtime.Value) bool {
	switch t := x.(type) {
	case *runtime.Named:
		return unsignedName(sizedNameOf(t.Typ))
	case *runtime.GoValue:
		_, isU := hostbridge.HostUint(t.V)
		return isU
	}
	return false
}

// boundOperand extracts one index/slice-bound operand: its int64
// position (wrapped bits for a boxed wide uint64 — such an index is
// always out of bounds) and its domain flag for the error text.
// sliceBoundsReason renders Go's boundsError text for a failed slice
// operation — mirroring the check order and message shapes of the
// runtime's goPanicSlice* family. Every operand evaluates in its own
// domain: a negative signed index reports its bare form without the
// capacity/length suffix (Go's boundsNegErrorFmts), an unsigned one
// its uint64 magnitude. Returns "" when every index is in bounds.
// boundOperand extracts one index/slice-bound operand: its int64
// position (wrapped bits for a boxed wide uint64 — such an index is
// always out of bounds) and its domain flag for the error text.
func (v *VM) boundOperand(f *frame, x runtime.Value, def int64) boundOperand {
	i, u, unsigned := def, uint64(def), false
	ux := runtime.Unwrap(v.materialize(f, x))
	if iv, ok := runtime.SmallIntOf(ux); ok {
		i = iv
		u = uint64(iv)
		unsigned = v.boundUnsigned(x)
	} else if g, isG := ux.(*runtime.GoValue); isG {
		if uv, isU := hostbridge.HostUint(g.V); isU {
			i = int64(uv)
			u = uv
			unsigned = true
		}
	}
	return hostbridge.BoundOperandOf(i, u, unsigned)
}

func (v *VM) bounds(f *frame, lo, hi runtime.Value, n int64) (boundOperand, boundOperand) {
	// a host integer scalar (time.Duration) reads as its int64 value —
	// `a[d:]` on a Duration d bounds by its nanoseconds, like gc.
	return v.boundOperand(f, lo, 0), v.boundOperand(f, hi, n)
}

// maxBound reads a 3-index slice's max operand; the full-expression form
// `a[low:high:]` uses the container's capacity. The slice operator itself
// (`elems[l:h:m]`) enforces low <= high <= max <= cap with Go's panic.
func (v *VM) maxBound(f *frame, max runtime.Value, capN int64) boundOperand {
	return v.boundOperand(f, max, capN)
}

// litKeyIndex resolves a composite-literal key to its int index: a
// *runtime.ImplicitIndex positional continues the running index `last`;
// named constants unwrap to int64. ok=false for anything else.
func (v *VM) litKeyIndex(f *frame, k runtime.Value, last int64) (int64, bool) {
	if _, isImp := k.(*runtime.ImplicitIndex); isImp {
		return last + 1, true
	}
	k = v.materialize(f, k)
	if nk, ok := k.(*runtime.Named); ok {
		k = nk.V
	}
	if iv, ok := runtime.SmallIntOf(k); ok {
		return iv, true
	}
	// a host scalar index (`reflect.Bool` in gob's decOpTable) arrives
	// boxed — gc only needs its integer value.
	return hostbridge.HostInt64(k)
}

func (v *VM) makeComposite(f *frame, ins bytecode.Instruction) runtime.Value {
	n := int(ins.A)
	kv := ins.B == 1
	// pop typedef then elems? No: compiler emitted typedef first, then elems.
	// stack: [typedef, e1, e2, ...] — typedef is BELOW elems.
	total := n
	if kv {
		total = n * 2
	}
	raw := make([]runtime.Value, total)
	for i := total - 1; i >= 0; i-- {
		raw[i] = f.pop()
	}
	tdv := f.pop()
	td, ok := tdv.(*runtime.TypeDef)
	if !ok {
		f.trap("composite literal on non-type %T", tdv)
	}
	return v.compositeOf(f, td, n, kv, raw)
}

// compositeOf builds a T{...} value for td — makeComposite's body once
// the typedef and element values are known. A pointer element literal
// (`[]*T{{...}}`, `map[K]*T{"k": {...}}`) recurses into it so the elided
// `&T{...}` pointee constructs like a direct T{...} — structs included,
// but also arrays, slices and maps.
func (v *VM) compositeOf(f *frame, td *runtime.TypeDef, n int, kv bool, raw []runtime.Value) runtime.Value {
	if td.HostNew != nil {
		// host-backed type (sync.Mutex, sync.Pool, ...): the literal
		// yields a fresh boxed host value; keyed fields initialize the
		// exported fields of the host struct through reflection
		// (`&sync.Pool{New: f}`).
		hv := td.HostNew()
		if rt := hostbridge.TypeOf(hv); rt != nil && rt.Kind() == hostbridge.KindSlice {
			// a bound named slice (net.IP{1, 2, 3, 4})
			return v.hostSliceLiteral(f, td, rt, n, kv, raw)
		}
		if n > 0 {
			if !kv {
				v.initHostPositional(f, td, hv, raw[:n])
			} else {
				v.initHostLiteral(f, td, hv, raw[:2*n])
			}
		}
		// the literal is value semantics (T{}, not &T{} — the & applies
		// outside and re-wraps as *T); tag the box with its declared
		// typedef so %T/TypeOf read T while the pointer-shaped payload
		// keeps pointer methods callable.
		return runtime.Tag(td, &runtime.GoValue{V: hv})
	}
	// a type alias builds the underlying composite
	if td.Kind == runtime.KindAlias && v.H.Underlying != nil {
		if u, err := v.H.Underlying(td); err == nil && u != nil {
			td = u
		}
	}
	switch td.Kind {
	case runtime.KindSlice:
		s := &runtime.Slice{Typ: td}
		if an, isArr := v.arrayLen(f, td); isArr {
			// an array literal keeps its declared length: unlisted
			// elements are the element zero; overruns and out-of-range
			// indexes are a compile reject — surfaced as a trap here.
			et := v.elemTypedef(f, td)
			zv := v.zeroValue(f, et)
			s.Elems = make([]runtime.Value, an)
			for i := range s.Elems {
				s.Elems[i] = valueCopy(zv)
			}
			if kv {
				last := int64(-1)
				for i := 0; i < n; i++ {
					ival, ok := v.litKeyIndex(f, raw[i*2], last)
					if !ok {
						f.trap("array literal index %T", raw[i*2])
					}
					last = ival
					if ival < 0 || ival >= an {
						f.trap("array index %d out of bounds [0:%d]", ival, an)
					}
					s.Elems[ival] = v.coerce(f, raw[i*2+1], et)
				}
			} else {
				if int64(n) > an {
					f.trap("index %d out of bounds [0:%d] in array literal", an, an)
				}
				for i := range raw {
					s.Elems[i] = v.coerce(f, raw[i], et)
				}
			}
			return s
		}
		if kv {
			// indexed literal: size is max index + 1, gaps are zero.
			// A named const index (`[T]{MyKind: v}`) unwraps to int64.
			idx := make([]int64, n)
			max := int64(-1)
			last := int64(-1)
			for i := 0; i < n; i++ {
				ival, ok := v.litKeyIndex(f, raw[i*2], last)
				if !ok {
					f.trap("slice literal index %T", raw[i*2])
				}
				last = ival
				idx[i] = ival
				if ival > max {
					max = ival
				}
			}
			s.Elems = make([]runtime.Value, max+1)
			zv := v.zeroValue(f, v.elemTypedef(f, td))
			for i := range s.Elems {
				s.Elems[i] = valueCopy(zv)
			}
			// keyed values coerce like positional ones — otherwise an
			// untyped constant element keeps its UConst box, which reads
			// as a different dynamic type under DeepEqual/`%T` than the
			// coerced element a positional literal produces.
			et := v.elemTypedef(f, td)
			for i := 0; i < n; i++ {
				s.Elems[idx[i]] = v.coerce(f, raw[i*2+1], et)
			}
		} else {
			// elements coerce to the declared element type — `[]any{x}`
			// boxes a typed nil while `[]*int{x}` keeps it
			et := v.elemTypedef(f, td)
			for i := range raw {
				raw[i] = v.coerce(f, raw[i], et)
			}
			s.Elems = raw
		}
		if at, ok := td.Anon.(*ast.ArrayType); ok {
			if _, isEll := at.Len.(*ast.Ellipsis); isEll {
				// `[...]T` takes its length from the element count —
				// fold it so the typedef spells the concrete [N]T like
				// Go's inferred length.
				runtime.FoldArrayLen(td.Pkg, at, int64(len(s.Elems)))
			}
		}
		return s
	case runtime.KindMap:
		m := runtime.NewMap(td, v.ifaceKeyed(f, td))
		et := v.elemTypedef(f, td)
		for i := 0; i < n; i++ {
			if _, isImp := raw[i*2].(*runtime.ImplicitIndex); isImp {
				f.trap("positional element in keyed map literal")
			}
			// literal keys bind the declared key type like index
			// operands — `map[float64]V{1: x}` stores float64(1), and
			// `map[A]V{B{1}: x}` rejects the mismatched named key.
			m.Insert(v.mapKeyOperand(f, td, raw[i*2]), v.coerce(f, raw[i*2+1], et))
		}
		return m
	case runtime.KindPointer:
		// elided `&T{...}` inside a []*T{...} literal: build the pointee
		// composite exactly like a direct T{...} (a struct fills fields,
		// an array/slice/map its elements) and box it in a fresh cell.
		if v.H.ElemOf == nil {
			f.trap("pointer element types require engine hooks")
		}
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			et = v.localElemTypedef(f, td)
			if et == nil {
				if err != nil {
					f.trapErr(err)
				}
				f.trap("cannot resolve element type of %s", tdName(td))
			}
		}
		return &runtime.Cell{Elem: v.compositeOf(f, et, n, kv, raw)}
	case runtime.KindStruct, runtime.KindNamedBasic:
		// `type A B` literals build the underlying composite while keeping
		// the declared tag: zeroValue returns Named{A, <underlying>}, and
		// field names/types resolve through the underlying typedef.
		etd := td
		if td.Kind == runtime.KindNamedBasic {
			if p := v.peelNamed(td); p != nil && p != td {
				etd = p
			}
		}
		switch etd.Kind {
		case runtime.KindSlice, runtime.KindMap:
			// a named type over a composite (`type B [2]int`,
			// `type B A`, `type B []int`, `type B map[K]V`): the literal
			// is the underlying composite built through the same kind
			// switch, tagged with the declared type — the struct fill
			// below would produce a bogus empty *Struct.
			return &runtime.Named{Typ: td, V: v.compositeOf(f, etd, n, kv, raw)}
		}
		z := v.zeroValue(f, td)
		wrap, _ := z.(*runtime.Named)
		var s *runtime.Struct
		if wrap != nil {
			s, _ = wrap.V.(*runtime.Struct)
		} else {
			s, _ = z.(*runtime.Struct)
		}
		if s == nil {
			s = &runtime.Struct{Def: etd, Fields: make([]runtime.Value, len(etd.Fields))}
			for i := range s.Fields {
				s.Fields[i] = runtime.NIL
			}
			if wrap != nil {
				wrap.V = s
			}
		}
		if kv {
			fts := v.fieldTypedefs(etd)
			for i := 0; i < n; i++ {
				name, ok := raw[i*2].(string)
				if !ok {
					f.trap("struct literal key %T", raw[i*2])
				}
				found := v.setLitField(f, s, etd, fts, name, raw[i*2+1])
				if !found {
					f.trapCode(runtime.TrapCodeUndefined, "%s has no field %s", tdName(td), name)
				}
			}
		} else {
			fts := v.fieldTypedefs(etd)
			for i := 0; i < n && i < len(s.Fields); i++ {
				var ft *runtime.TypeDef
				if i < len(fts) {
					ft = fts[i]
				}
				if i < len(etd.Fields) && etd.Fields[i] == "_" {
					// a blank field discards its literal element —
					// `struct{a,_,c int}{1,2,3}` stores 1,0,3 — but
					// the element still has to be assignable.
					_ = v.coerce(f, raw[i], ft)
					continue
				}
				s.Fields[i] = v.coerce(f, raw[i], ft)
			}
		}
		if wrap != nil {
			return wrap
		}
		return s
	default:
		f.trap("composite literal for kind %d", td.Kind)
	}
	return nil
}

// setLitField writes one `Name: value` entry of a struct literal. A
// declared field wins by name; otherwise the key resolves through
// promoted (embedded) fields — Go 1.27's promoted-field literal keys.
// Pointer embeds cannot supply literal keys (Go rejects "invalid
// implicit pointer indirection"), so promotedMember is called with
// allowPtr=false.
func (v *VM) setLitField(f *frame, s *runtime.Struct, def *runtime.TypeDef, fts []*runtime.TypeDef, name string, val runtime.Value) bool {
	for fi, fn := range def.Fields {
		if fn == "_" {
			continue // Go reports `_: v` as "unknown field _"
		}
		if fn == name && selVisible(selPkg(f), def.Pkg, fn) {
			var ft *runtime.TypeDef
			if fi < len(fts) {
				ft = fts[fi]
			}
			s.Fields[fi] = v.coerce(f, val, ft)
			return true
		}
	}
	if v.H.FieldPath != nil && def != nil && len(def.LocalTypes) == 0 {
		if idx, found, decided := v.H.FieldPath(def, name); decided {
			if !found || v.pathPtr(def, idx) {
				// no declared field, or one a key cannot reach — only a
				// host member can still bind the key.
				if pr, ok := v.promotedMemberEx(f, s, name, false, false, -1); ok && pr.host != nil {
					v.setField(f, pr.host, name, val)
					return true
				}
				return false
			}
			if pr, ok := v.promotedMemberEx(f, s, name, false, false, len(idx)-1); ok {
				if pr.host != nil {
					v.setField(f, pr.host, name, val)
					return true
				}
				return false // methods don't promote into literal keys
			}
			st, fi := v.fieldSlotAtPath(f, s, idx, name)
			if st == nil {
				return false
			}
			var ft *runtime.TypeDef
			if ifts := v.fieldTypedefs(st.Def); fi < len(ifts) {
				ft = ifts[fi]
			}
			st.Fields[fi] = v.coerce(f, val, ft)
			return true
		}
	}
	if pr, ok := v.promotedMember(f, s, name, false); ok {
		if pr.host != nil {
			v.setField(f, pr.host, name, val)
			return true
		}
		if pr.st == nil {
			return false // methods don't promote into literal keys
		}
		var ft *runtime.TypeDef
		if ifts := v.fieldTypedefs(pr.st.Def); pr.idx < len(ifts) {
			ft = ifts[pr.idx]
		}
		pr.st.Fields[pr.idx] = v.coerce(f, val, ft)
		return true
	}
	return false
}

// valueCopy implements Go assignment semantics: structs copy by value —
// recursively, since a struct field is itself a copied value — while
// slices, maps and pointers share.
func valueCopy(v runtime.Value) runtime.Value {
	return runtime.Copy(v)
}

// hostStructCopy snapshots a HostNew-backed host value for assignment:
// the box is a `*T` so `var b bytes.Buffer = a` would otherwise alias
// a's storage where gc copies the struct. The Named tag, when present,
// wraps the snapshot unchanged.
func hostStructCopy(x runtime.Value) runtime.Value {
	if n, ok := x.(*runtime.Named); ok {
		if nv, ok := runtime.HostStructCopy(n.V); ok {
			return &runtime.Named{Typ: n.Typ, V: nv}
		}
		return x
	}
	if nv, ok := runtime.HostStructCopy(x); ok {
		return nv
	}
	return x
}

// hostNewStructTagged reports whether v carries a HostNew struct tag of
// its own — the same mark runtime.Copy's Named branch checks before
// snapshotting the box, so a value reporting true here already came
// back copied (or came back unchanged for the same reason
// HostStructCopy would report false).
func hostNewStructTagged(v runtime.Value) bool {
	return runtime.HostStructTagged(v)
}

// memberOf resolves a package member through MemberV so that a lazy
// package initializer triggered from inside this VM runs on THIS VM — a
// spawned goroutine's first touch of a package must not execute __init__
// on the engine's root VM.
func (v *VM) memberOf(p *runtime.Package, name string) (runtime.Value, error) {
	return p.MemberV(name, v.H.Materialize, v.runInit)
}

// lenDerefFold reports the constant length of *p when p's type is
// *[N]T — Go folds len/cap of a dereferenced array pointer without
// evaluating the deref. probe is either the operand's declared typedef
// (the static tier: folds without evaluating anything) or the
// evaluated pointer value (the nil-pointer tier: only a nil *[N]T
// matters — a live pointer derefs fine on the normal path).
func (v *VM) lenDerefFold(f *frame, probe runtime.Value) (runtime.Value, bool) {
	var td *runtime.TypeDef
	switch t := probe.(type) {
	case *runtime.TypeDef:
		td = t
	case *runtime.TypedNil:
		td = t.Typ
	case *runtime.Named:
		if tn, ok := t.V.(*runtime.TypedNil); ok {
			td = tn.Typ
		} else if t.Typ != nil && t.Typ.Kind == runtime.KindPointer {
			td = t.Typ
		}
	}
	at := runtime.PtrArrayType(td)
	if at == nil {
		return nil, false
	}
	n, ok := v.arrayLen(f, &runtime.TypeDef{Anon: at, Pkg: td.Pkg, File: td.File})
	if !ok {
		return nil, false
	}
	return int64(n), true
}

// lenIdxFold reports the constant length of x[i] when x's element
// typedef is an array — Go folds len/cap of an index into an
// array-typed element without evaluating the index at all.
func (v *VM) lenIdxFold(f *frame, base runtime.Value) (runtime.Value, bool) {
	td := typedefOf(base)
	if td == nil {
		// typed nils and live containers carry their declared typedef —
		// `var s [][30]int` is a TypedNil, not something Deref peels.
		switch b := base.(type) {
		case *runtime.TypedNil:
			td = b.Typ
		case *runtime.Slice:
			td = b.Typ
		case *runtime.Map:
			td = b.Typ
		case *runtime.Named:
			td = b.Typ
		}
	}
	if td == nil || td.Anon == nil {
		return nil, false
	}
	var elt ast.Expr
	switch t := td.Anon.(type) {
	case *ast.ArrayType:
		elt = t.Elt // x[i] on an array/slice has the element's type
	case *ast.MapType:
		elt = t.Value // x[k] on a map has the value's type
	default:
		return nil, false
	}
	at, ok := elt.(*ast.ArrayType)
	if !ok || at.Len == nil {
		return nil, false
	}
	// the element's array wraps in a fresh typedef — carry the
	// declaring package so its folded lengths are findable: folds
	// keyed by const names live on the owner, and a nil-Pkg typedef
	// neither reads them nor re-evaluates the name.
	n, ok := v.arrayLen(f, &runtime.TypeDef{Anon: at, Kind: runtime.KindSlice, Pkg: td.Pkg, File: td.File})
	if !ok {
		return nil, false
	}
	return int64(n), true
}

// lenCapBuiltin resolves the predeclared len/cap builtins once — the
// values OpLenCallFold's identity probe compares the resolved callee
// against. which selects: 0 len, 1 cap. A nil result (no Builtin hook,
// or the name unbound) simply never folds — the plain call runs.
func (v *VM) lenCapBuiltin(which int32) runtime.Value {
	if !v.lenCapDone {
		v.lenCapDone = true
		if v.H.Builtin != nil {
			v.lenB, _ = v.H.Builtin("len")
			v.capB, _ = v.H.Builtin("cap")
		}
	}
	if which == 0 {
		return v.lenB
	}
	return v.capB
}

// lenConstInt reads an array-length value: a materialized int64, or a
// UConst still carrying its constant — const decls keep the UConst
// form so each use materializes for context (`const N = 5; [N]T`).
func lenConstInt(x runtime.Value) (int64, bool) {
	switch v := runtime.Unwrap(x).(type) {
	case int64:
		return v, true
	case *runtime.UConst:
		iv := v.V
		if iv.Kind() != constant.Int {
			var ok bool
			if iv, ok = toIntConst(iv); !ok {
				return 0, false
			}
		}
		return constant.Int64Val(iv)
	}
	return 0, false
}

// arrayLen reports the element count of an array typedef — an
// *ast.ArrayType that kept its length. The length may be a literal
// (`[3]int`) or a package-level const name (`[N]int`); slice typedefs
// and `[...]T` forms report ok=false.
func (v *VM) arrayLen(f *frame, td *runtime.TypeDef) (int64, bool) {
	if td == nil {
		return 0, false
	}
	info := runtime.ArrayASTInfoOf(td)
	if info == nil || info.Type.Len == nil {
		return 0, false
	}
	at := info.Type
	pkg := info.Pkg
	if pkg == nil {
		pkg = td.Pkg
	}
	file := info.File
	if file == nil {
		file = td.File
	}
	// a canonical literal (`[3]T`) or an already-folded length answers
	// without evaluating anything.
	if n, ok := runtime.FoldedArrayLen(pkg, at); ok {
		return n, true
	}
	if v.H.ConstEval != nil {
		if cv, ok := v.H.ConstEval(pkg, file, at.Len); ok {
			if iv, ok2 := toIntConst(cv); ok2 {
				if n, ok3 := constant.Int64Val(iv); ok3 {
					return runtime.FoldArrayLen(pkg, at, n), true
				}
			}
		}
	}
	switch l := at.Len.(type) {
	case *ast.BasicLit:
		// the length literal evaluates in the constant domain — `1e1`,
		// `'a'` and `1_000` all spell integer lengths that
		// strconv.ParseInt cannot read.
		if iv, ok := toIntConst(constant.MakeFromLiteral(l.Value, l.Kind, 0)); ok {
			if n, ok := constant.Int64Val(iv); ok {
				return runtime.FoldArrayLen(pkg, at, n), true
			}
		}
		return 0, false
	case *ast.SelectorExpr:
		// A package-qualified const reads the imported package's const
		// bindings directly. Going through a normal selector expression
		// would initialize the package even when the constant is already
		// bound, mixing initializer output into a type-only fixture.
		if id, ok := l.X.(*ast.Ident); ok && pkg != nil && file != nil {
			if ref := pkg.Scopes[file][id.Name]; ref != nil {
				if ip, err := ref.Materialize(); err == nil && ip != nil {
					_ = ip.EnsureConstsRun(v.runInit)
					if gv, ok := ip.Globals.Get(l.Sel.Name); ok {
						if d, ok2 := runtime.Deref(gv); ok2 {
							gv = d
						}
						if n, ok2 := lenConstInt(gv); ok2 {
							return runtime.FoldArrayLen(pkg, at, n), true
						}
					}
				}
			}
		}
	case *ast.Ident:
		// a named const: its value lives in the package env once init
		// ran (Materialize returns NIL for const decls); fall back to the
		// index for a const declared below the use or not yet bound.
		if pkg != nil {
			readGlobal := func() (int64, bool) {
				gv, ok := pkg.Globals.Get(l.Name)
				if !ok {
					return 0, false
				}
				if d, ok2 := runtime.Deref(gv); ok2 {
					gv = d
				}
				return lenConstInt(gv)
			}
			if n, ok := readGlobal(); ok {
				return runtime.FoldArrayLen(pkg, at, n), true
			}
			// Constants bind without running the package initializer.
			// Try them only after the already-bound read: const bootstrap
			// itself can need an array length, and re-entering its
			// sync.Once would wait forever.
			_ = pkg.EnsureConstsRun(v.runInit)
			if n, ok := readGlobal(); ok {
				return runtime.FoldArrayLen(pkg, at, n), true
			}
			if pkg.Index != nil && v.H.Materialize != nil {
				if d := pkg.Index.Consts[l.Name]; d != nil {
					if mv, err := v.H.Materialize(pkg, d); err == nil {
						if n, ok := lenConstInt(mv); ok {
							return runtime.FoldArrayLen(pkg, at, n), true
						}
					}
				}
			}
		}
	}
	// Any other constant form ([N*2]int, [N+1]int) compiles and runs in
	// the typedef's package scope. Bind consts first — the expression's
	// plain globals read values without running package init, while a
	// selector's package needs its own const-only bootstrap. This keeps
	// full constant semantics in the ordinary compiler instead of
	// requiring ConstEval's static shortcut to cover every valid form.
	if pkg != nil && v.H.CompileExpr != nil && lenConstShaped(at.Len) {
		_ = pkg.EnsureConstsRun(v.runInit)
		ast.Inspect(at.Len, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || file == nil {
				return true
			}
			if ref := pkg.Scopes[file][id.Name]; ref != nil {
				if ip, err := ref.Materialize(); err == nil && ip != nil {
					_ = ip.EnsureConstsRun(v.runInit)
				}
			}
			return true
		})
		var n int64
		var ok bool
		func() {
			defer func() { _ = recover() }()
			if ch, err := v.H.CompileExpr(pkg, file, at.Len); err == nil && ch != nil {
				if r, err2 := v.call(&runtime.Function{Pkg: pkg, File: file, Name: "<arraylen>", Chunk: ch}, nil, nil, nil); err2 == nil {
					if iv, ok2 := lenConstInt(r); ok2 {
						n, ok = iv, true
					}
				}
			}
		}()
		if ok {
			return runtime.FoldArrayLen(pkg, at, n), true
		}
	}
	return 0, false
}

// lenConstShaped reports whether an array-length expression can only be
// a constant expression — names, literals, parens and constant
// arithmetic. Anything else (ellipsis, calls, indexing) is either the
// `[...]T` form or a non-constant Go would reject, and is not worth
// evaluating.
func lenConstShaped(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit, *ast.Ident:
		return true
	case *ast.ParenExpr:
		return lenConstShaped(x.X)
	case *ast.UnaryExpr:
		return lenConstShaped(x.X)
	case *ast.BinaryExpr:
		return lenConstShaped(x.X) && lenConstShaped(x.Y)
	case *ast.SelectorExpr:
		_, ok := x.X.(*ast.Ident)
		return ok
	case *ast.CallExpr:
		// Calls in a valid constant length are conversions or the
		// constant forms of len/cap/min/max. This syntactic gate cannot
		// tell a conversion from a function call, but a real function
		// call is invalid Go here; check only the operands' shape.
		for _, a := range x.Args {
			if !lenConstShaped(a) {
				return false
			}
		}
		switch f := x.Fun.(type) {
		case *ast.Ident:
			return true
		case *ast.SelectorExpr:
			_, ok := f.X.(*ast.Ident)
			return ok
		}
	}
	return false
}

// copyArray clones an array value for Go's assignment semantics —
// `b := a` on arrays owns a distinct backing array, and nested arrays
// copy recursively.
func (v *VM) copyArray(f *frame, s *runtime.Slice, td *runtime.TypeDef) *runtime.Slice {
	el := make([]runtime.Value, len(s.Elems))
	et := v.elemTypedef(f, td)
	_, nested := v.arrayLen(f, et)
	for i, e := range s.Elems {
		if inner, ok := e.(*runtime.Slice); ok && nested {
			el[i] = v.copyArray(f, inner, et)
			continue
		}
		el[i] = e
	}
	return &runtime.Slice{Elems: el, Typ: s.Typ}
}

// maskInt applies integer-width wraparound for a conversion into a
// sized type: int8(200) is -56 like Go. Unsized names pass through.
// sizedNameOf resolves the builtin width name behind a tag: for a
// declared `type MyU8 uint8` the mask applies through the underlying
// ident, not the declared name.
func sizedNameOf(td *runtime.TypeDef) string {
	return runtime.TypedefIdentOf(td, sizedIntName)
}

// sizedIntName reports whether name is a builtin integer whose domain
// differs from bare int64 — maskInt wraps those values, and the
// unsigned names also switch binaryOp's division/shift/compare domain.
// int64 itself stays untagged: it IS the bare model. uint tags so
// `x := uint(v)` keeps unsigned semantics; int stays bare too.
func sizedIntName(name string) bool {
	switch name {
	case "int8", "int16", "int32", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

// basicNameOf resolves the underlying builtin basic-type name behind a
// typedef the way sizedNameOf resolves ints: `type F32 float32` and the
// bare float32 typedef both read "float32". Delegates to the shared
// runtime helper (the basic-name sets differ only on "error", which
// none of the numeric callers can name).
func basicNameOf(td *runtime.TypeDef) string {
	return runtime.BasicNameOf(td)
}

// bareScalarName names a bare scalar for the mismatched-types message —
// bound-package constants and host-call results arrive as plain
// int64/float64/... values.
func bareScalarName(x runtime.Value) (string, bool) {
	switch x.(type) {
	case int64:
		return "int", true
	case float64:
		return "float64", true
	case complex128:
		return "complex128", true
	case string:
		return "string", true
	case bool:
		return "bool", true
	}
	return "", false
}

// bareScalarDomain groups a bare scalar for the mismatch check. Numeric
// scalars share one domain: an under-typed value (a generic T-argument,
// a materialized const) can't be told from a declared int64, so only a
// cross-domain mismatch is statically sure.
func bareScalarDomain(x runtime.Value) (string, bool) {
	switch x.(type) {
	case int64, float64:
		return "num", true
	case complex128:
		return "cplx", true
	case string:
		return "str", true
	case bool:
		return "bool", true
	}
	return "", false
}

// tdScalarDomain resolves the named operand's underlying basic to the
// same domain — `type Small int8` and `Duration` are numeric, StructTag
// is string. A non-basic underlying ("", false) is never scalar-shaped.
func (v *VM) tdScalarDomain(td *runtime.TypeDef) (string, bool) {
	utd := v.peelNamed(td)
	if utd == nil {
		return "", false
	}
	switch basicNameOf(utd) {
	case "int", "int8", "int16", "int32", "int64", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64":
		return "num", true
	case "complex64", "complex128":
		return "cplx", true
	case "string":
		return "str", true
	case "bool":
		return "bool", true
	}
	return "", false
}

// bareScalarShared reports whether bare scalar x and the named operand's
// underlying basic share a domain — `x + len(s)` on `var x int` and
// `tag + hostStr` are in-domain while `s + 1` crosses.
func (v *VM) bareScalarShared(x runtime.Value, td *runtime.TypeDef) bool {
	name, ok := bareScalarDomain(x)
	if !ok {
		return false
	}
	tdName, ok := v.tdScalarDomain(td)
	return ok && tdName == name
}

// unsignedName reports whether a sized-int typedef name is an unsigned
// width — the unsigned names evaluate division, remainder, shifts and
// ordered comparisons in the uint64 domain.
func unsignedName(name string) bool {
	switch name {
	case "uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

func maskInt(n int64, name string) int64 {
	switch name {
	case "int8":
		return int64(int8(n))
	case "int16":
		return int64(int16(n))
	case "int32", "rune":
		return int64(int32(n))
	case "uint8", "byte":
		return int64(uint8(n))
	case "uint16":
		return int64(uint16(n))
	case "uint32":
		return int64(uint32(n))
	case "uint64", "uintptr", "uint":
		return int64(uint64(n))
	}
	return n
}
