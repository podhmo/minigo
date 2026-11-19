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
	"go/token"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// coerceVarPack converts the packed trailing arguments of a variadic
// call to the parameter's element type, like Go's implicit assignment
// of each argument to T: `f(1)` for `...float64` holds 1.0, `...uint8`
// truncates a non-constant shift `1<<n`, `...any` boxes 'a' as a rune.
// The pack is fresh (prepFrame built it), so converting in place is
// safe.
func (v *VM) coerceVarPack(f *frame, pack *runtime.Slice, td *runtime.TypeDef) {
	// only a constant (or nil) changes under the element conversion — a
	// runtime argument already has its type — so a pack of plain values
	// (the common `f(x, y)`) skips the element-type lookup entirely.
	first := -1
	for i, e := range pack.Elems {
		if varPackConverts(e) {
			first = i
			break
		}
	}
	if first < 0 || v.H.ElemOf == nil {
		return
	}
	utd := v.peelNamed(td)
	if utd == nil || utd.Kind != runtime.KindSlice {
		return
	}
	et, err := v.H.ElemOf(utd)
	if err != nil || et == nil {
		return
	}
	// a basic element type takes an untyped constant straight from the
	// materialization memo coerceConcrete fills (`sum(i, 2, 3)` converts
	// the same chunk constants on every call).
	direct := et.Kind != runtime.KindInterface && et.Kind != runtime.KindAlias && et.HostNew == nil
	for i, e := range pack.Elems[first:] {
		if u, ok := e.(*runtime.UConst); ok && direct {
			if r, hit := u.Memo(et); hit {
				pack.Elems[first+i] = r
				continue
			}
		}
		if varPackConverts(e) {
			pack.Elems[first+i] = v.coerce(f, e, et)
		}
	}
}

// varPackConverts reports whether a packed variadic argument still
// needs its element type: an untyped or typed constant, or a bare nil.
func varPackConverts(e runtime.Value) bool {
	if _, ok := constPayload(e); ok {
		return true
	}
	_, isNil := e.(runtime.Nil)
	return isNil
}

// Call invokes a function-like value: [runtime.Function],
// [runtime.Closure], [runtime.BoundMethod], [runtime.BuiltinFunc],
// [runtime.TypeDef] (a conversion), or a [runtime.Cell] wrapping any of
// those. Script panics and traps are returned as errors. Unless a scope
// is held with [VM.EnsureProc], the outermost Call on a VM opens the
// script process and ends it on return.
func (v *VM) Call(callee runtime.Value, args []runtime.Value) (result runtime.Value, err error) {
	return v.callBounded(callee, args, nil, nil)
}

// callBounded is Call carrying the call site's spread element typedef
// into generic inference; nil spreadTd is an ordinary call.
func (v *VM) callBounded(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (result runtime.Value, err error) {
	return v.callBoundedCatch(callee, args, statics, spreadTd, false)
}

// CallCatching is Call for a host callback whose host side recovers a
// panic of its own accord — text/template's safeCall around a FuncMap
// func. Called from a goroutine other than the VM's (an http handler
// executing a template), Call treats a panic in callee as a crash of the
// process; here it comes back as the returned error, for the host to
// re-raise where its recover is.
func (v *VM) CallCatching(callee runtime.Value, args []runtime.Value) (runtime.Value, error) {
	return v.callBoundedCatch(callee, args, nil, nil, true)
}

// callBoundedCatch is callBounded; catch returns a foreign goroutine's
// panic as the error instead of failing the process.
func (v *VM) callBoundedCatch(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef, catch bool) (result runtime.Value, err error) {
	// goroutineID parses runtime.Stack, whose traceback walks the
	// interpreter's deep Go stack — tens of µs per call. Callbacks from
	// a SyncCallbacks builtin skip it through ownerCaller.
	gid := goroutineID()
	v.callMu.Lock()
	if v.callDepth > 0 && v.callGid != gid {
		v.callMu.Unlock()
		// a foreign goroutine — a retained host callback firing off-VM
		// (WaitGroup.Go, time.AfterFunc, a Pool.New hit from another
		// script goroutine): the owning goroutine holds this frame
		// stack, so run the call on a spawned child VM of the same
		// process and join it instead of racing the owner's frames.
		// a Call from a helper running one of this process's blocking
		// host calls is a synchronous re-entry — the host invoking a
		// script callback inside the call (sync.Once.Do's f). Its panic
		// propagates back through the join: the helper's recover hands
		// it to callReflectFunc, which re-panics it on the calling
		// goroutine where the script's own recover can see it. Failing
		// the process here would kill it before that recover runs.
		failProc := !catch
		if p := v.proc.Load(); p != nil && p.IsSyncCaller(gid) {
			failProc = false
		}
		t := v.spawn(callee, args, statics, spreadTd, failProc)
		werr := t.Wait()
		return t.Result, werr
	}
	v.callDepth++

	v.callGid = gid
	v.callMu.Unlock()
	return v.callEntered(callee, args, statics, spreadTd)
}

// ownerCaller is the VMCaller a SyncCallbacks builtin receives: the
// builtin runs on the goroutine that owns the VM's in-flight Call and
// calls back before it returns, so a Call through it is a same-goroutine
// re-entry by construction and needs no goroutine id.
type ownerCaller struct{ *VM }

// bindProc keeps the owner binding: the embedded *VM's bindProc would
// rebind this caller to the VM's current proc, but an ownerCaller is
// already bound to its Call frame and must not pick up another proc.
func (o ownerCaller) bindProc() runtime.VMCaller { return o }

func (o ownerCaller) Call(callee runtime.Value, args []runtime.Value) (runtime.Value, error) {
	v := o.VM
	v.callMu.Lock()
	if v.callDepth == 0 {
		// no Call in flight to re-enter — take the general path.
		v.callMu.Unlock()
		return v.Call(callee, args)
	}
	v.callDepth++
	v.callMu.Unlock()
	return v.callEntered(callee, args, nil, nil)
}

// callEntered runs a Call whose callDepth increment is already done —
// callBounded's same-goroutine path — and undoes it on return.
func (v *VM) callEntered(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (result runtime.Value, err error) {
	defer func() {
		v.callMu.Lock()
		v.callDepth--
		if v.callDepth == 0 {
			v.callGid = 0
		}
		v.callMu.Unlock()
	}()
	p := v.proc.Load()
	if p == nil {
		v.EnsureProc()
		defer v.ReleaseProc()
		p = v.proc.Load()
	}
	// register the calling goroutine for deadlock detection — released
	// when this Call's outermost frame returns.
	gid := v.callGid
	p.EnterG(gid)
	defer p.LeaveG(gid)
	defer func() {
		if r := recover(); r != nil {
			if ex, isExit := r.(*ExitRequest); isExit {
				p.Kill()
				result, err = nil, nil
				if ex.Code != 0 {
					err = ex
				}
			} else {
				result, err = nil, asError(r)
			}
		}
		// a goroutine's panic fails the process like Go's crash: report
		// the real failure over this call's own outcome (including a
		// procExit this goroutine received while unwinding)
		if ferr := p.FatalErr(); ferr != nil && (err == nil || IsProcExit(err)) {
			err = ferr
		}
	}()
	return v.call(callee, args, statics, spreadTd)
}

// call is Call without the boundary: script *Panic / *Trap propagate as Go
// panics through intermediate frames so defers and recover() see them.
// spreadTd carries the element typedef a trailing `xs...` slice supplied
// at the call site; nil when the call had no spread.
func (v *VM) call(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (runtime.Value, error) {
	return v.callWindow(nil, callee, args, -1, statics, spreadTd)
}

// callInPlace is call for an OpCall whose args stay on the operand
// stack: the caller dropped callee and statics and left its top argc
// operands, which become the callee's window — no argbuf copy. f is the
// calling frame — its argbuf backs any materialized args slice. The
// dispatch paths that need a real slice (linkname receiver prepend,
// builtin, conversion, host func) materialize it via inPlaceArgs.
func (v *VM) callInPlace(f *frame, callee runtime.Value, argc int, statics []*runtime.TypeDef) (runtime.Value, error) {
	return v.callWindow(f, callee, nil, argc, statics, nil)
}

// inPlaceArgs materializes argc operands off the arena tail — the
// in-place call's args — when a dispatch arm needs a slice. The caller
// frame's argbuf backs it (same reuse popArgsCall had); builtins that
// keep their args take a private copy in callBuiltin like before.
func (v *VM) inPlaceArgs(f *frame, argc int, args []runtime.Value) []runtime.Value {
	if argc < 0 || args != nil {
		return args
	}
	buf := f.argbuf
	if cap(buf) < argc {
		buf = make([]runtime.Value, argc)
	} else {
		// nil only the delta — len(buf) is the previous call's count,
		// so [argc, len) is exactly where stale args sit.
		stale := len(buf)
		buf = buf[:argc]
		if argc < stale {
			clear(f.argbuf[argc:stale])
		}
	}
	copy(buf, v.vals[len(v.vals)-argc:])
	f.argbuf = buf
	return buf
}

func (v *VM) callWindow(f *frame, callee runtime.Value, args []runtime.Value, winArgs int, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (runtime.Value, error) {
	if p := v.proc.Load(); p != nil {
		select {
		case <-p.Done():
			// the process ended while this call was queued — a sibling
			// panic already won; die like Go's exit() rather than
			// running the call's observable effects.
			panic(procExit{})
		default:
		}
	}
	var seen map[*runtime.Function]bool
	for {
		// a bodiless decl may carry //go:linkname wiring it to another
		// package's implementation — resolve before dispatch so a
		// builtin or typedef target sees the generic callee path.
		if fn := linknameDecl(callee); fn != nil && v.H.Linkname != nil {
			tv, ok, err := v.H.Linkname(v, fn)
			if err != nil {
				return nil, err
			}
			if ok && !seen[fn] {
				if seen == nil {
					seen = map[*runtime.Function]bool{}
				}
				seen[fn] = true
				args = v.inPlaceArgs(f, winArgs, args)
				// materialized — the args no longer sit in the arena window.
				winArgs = -1
				callee, args = relinkCallee(callee, tv, args)
				continue
			}
		}
		switch c := callee.(type) {
		case *runtime.BuiltinFunc:
			return v.callBuiltin(c, v.inPlaceArgs(f, winArgs, args))
		case *runtime.TypeDef:
			args = v.inPlaceArgs(f, winArgs, args)
			if len(args) != 1 {
				return nil, fmt.Errorf("conversion to %s needs exactly one argument", c.Name)
			}
			return v.convert(c, args[0])
		case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
			// calling a nil function value panics like a nil deref in Go
			panic(runtime.NilDerefPanic())
		case *runtime.Named:
			// a value of a named func type calls through its underlying
			callee = runtime.Unwrap(callee)
			continue
		case *runtime.GoValue:
			if fv := reflect.ValueOf(c.V); fv.IsValid() && fv.Kind() == reflect.Func {
				name := fmt.Sprintf("%v", fv.Type())
				margs, err := v.matBuiltinArgs(expandSpread(v.inPlaceArgs(f, winArgs, args)), c.V)
				if err != nil {
					return nil, fmt.Errorf("%s %w", name, err)
				}
				return callReflectFunc(name, fv, v, margs)
			}
			return nil, fmt.Errorf("value of type %T is not callable", callee)
		default:
			if dv, ok := runtime.Deref(callee); ok {
				callee = dv
				continue
			}
		}
		break
	}
	fr, err := v.prepFrameSeen(callee, args, winArgs, statics, spreadTd, seen)
	if err != nil {
		return nil, err
	}
	v.exec(fr)
	var r runtime.Value = runtime.NIL
	if fr.sp > 0 {
		r = fr.stack[fr.sp-1]
	}
	freeFrame(fr)
	return r, nil
}

// callBuiltin runs a builtin. It lives outside call's callee switch so
// the panic-annotating defer open-codes: inside the for {} it allocated
// a heap closure on every builtin call.
func (v *VM) callBuiltin(c *runtime.BuiltinFunc, args []runtime.Value) (runtime.Value, error) {
	if !c.Forwards {
		args = expandSpread(args)
	}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		r = asScriptPanic(r)
		// a builtin has no frame of its own — record one attributed
		// to the OpCall site so its failure names the builtin.
		if c.Name != "" {
			var entry string
			if n := len(v.frames); n > 0 {
				caller := v.frames[n-1]
				entry = v.frameLine(caller, caller.pos(), c.Name+"() (builtin)")
			} else {
				entry = c.Name + "() (builtin)"
			}
			switch e := r.(type) {
			case *runtime.Panic:
				e.Frames = append(e.Frames, entry)
			case *runtime.Trap:
				e.Frames = append(e.Frames, entry)
			}
		}
		panic(r)
	}()
	if c.Pkg != nil {
		// a package-bound host call sees materialized constants —
		// interpreter builtins (Pkg nil) get UConst so their own
		// constant arms keep working (append's element-type
		// conversion, real/imag's constant domain).
		var err error
		if args, err = v.matBuiltinArgs(args, c.Target); err != nil {
			return nil, fmt.Errorf("%s %w", c.Name, err)
		}
	}
	// a builtin may retain its args slice past the call — task.F
	// binds it into a taskCall — while args may ride the caller
	// frame's argbuf, which the next OpCall rewrites. Retaining
	// builtins get a private copy like popArgs always produced.
	if c.RetainsArgs {
		args = append([]runtime.Value{}, args...)
	}
	if c.SyncCallbacks {
		return c.Fn(ownerCaller{v}, args)
	}
	return c.Fn(bindCaller(v), args)
}

// linknameDecl returns the bodiless *runtime.Function behind a callable —
// a Function itself, a Closure's Fn, or a BoundMethod's Fn — or nil when
// the callee has a body to run.
func linknameDecl(callee runtime.Value) *runtime.Function {
	var fn *runtime.Function
	switch c := callee.(type) {
	case *runtime.Function:
		fn = c
	case *runtime.Closure:
		fn = c.Fn
	case *runtime.BoundMethod:
		fn = c.Fn
	}
	if fn == nil || fn.Decl == nil || fn.Decl.Body != nil {
		return nil
	}
	return fn
}

// relinkCallee substitutes the linkname-resolved target for callee,
// keeping a bound method's receiver: a resolved Function is called as a
// method of the original receiver, while a non-function target (a
// builtin, a host func) takes the receiver as its first argument — the
// call sequence gc emits for a linked method.
func relinkCallee(callee, tv runtime.Value, args []runtime.Value) (runtime.Value, []runtime.Value) {
	bm, ok := callee.(*runtime.BoundMethod)
	if !ok {
		return tv, args
	}
	if tfn, ok := tv.(*runtime.Function); ok {
		return &runtime.BoundMethod{Recv: bm.Recv, Fn: tfn, Direct: bm.Direct}, args
	}
	return tv, append([]runtime.Value{bm.Recv}, args...)
}

// prepFrameSeen is prepFrame carrying the linkname resolutions already
// followed for this call — a pull decl whose pushed symbol resolves to
// itself (or a longer cycle) would otherwise re-dispatch forever.
func (v *VM) prepFrameSeen(callee runtime.Value, args []runtime.Value, winArgs int, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef, seen map[*runtime.Function]bool) (*frame, error) {
	// an in-place call reads its args off the arena tail — the view is
	// aliased, never written, so inference and the BoundMethod arm see
	// them without a copy.
	if winArgs >= 0 && args == nil {
		args = v.vals[len(v.vals)-winArgs:]
	}
	var fn *runtime.Function
	var upvals []*runtime.Cell
	switch c := callee.(type) {
	case *runtime.Function:
		fn = c
	case *runtime.Closure:
		fn = c.Fn
		upvals = c.Upvals
	case *runtime.BoundMethod:
		fn = c.Fn
		if !fn.PtrRecv {
			if tn, isNil := asTypedNil(c.Recv); isNil && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
				if c.Direct {
					// a callee-position bind dispatches the *T→T
					// wrapper directly: gc reports a nil
					// dereference, not the checked-wrapper text.
					panic(runtime.NilDerefPanic())
				}
				// an interface-boxed nil binds the receiver lazily; the
				// implicit *T→T method wrapper panics when the call
				// actually runs.
				qual := ""
				if fn.Pkg != nil && fn.Pkg.Name != "" {
					qual = fn.Pkg.Name + "."
				}
				panic(runtime.PlainPanic(fmt.Sprintf("value method %s%s called using nil *%s pointer", qual, fn.Name, fn.Recv)))
			}
		}
		if winArgs >= 0 {
			// in-place args live in the arena — shift the window one
			// slot right so the receiver can take its head.
			base := len(v.vals) - winArgs
			v.vals = append(v.vals, nil)
			copy(v.vals[base+1:], v.vals[base:len(v.vals)-1])
			v.vals[base] = c.Recv
			winArgs++
			args = v.vals[len(v.vals)-winArgs:]
		} else {
			args = append([]runtime.Value{c.Recv}, args...)
		}
	default:
		return nil, fmt.Errorf("value of type %T is not callable", callee)
	}
	// a bodiless declaration may carry a //go:linkname directive whose
	// target argument wires it to a real implementation in another
	// package — substitute the resolved target and re-dispatch.
	if fn != nil && fn.Decl != nil && fn.Decl.Body == nil {
		if v.H.Linkname != nil {
			tv, ok, err := v.H.Linkname(v, fn)
			if err != nil {
				return nil, err
			}
			if ok && !seen[fn] {
				if seen == nil {
					seen = map[*runtime.Function]bool{}
				}
				seen[fn] = true
				return v.prepFrameSeen(tv, args, winArgs, statics, spreadTd, seen)
			}
		}
		// An unresolved bodiless decl compiles to a zero-return shim,
		// which is only acceptable for the result-free no-op class
		// (registration hooks and memory primitives an interpreter
		// needn't run). A decl with results would silently answer
		// wrong — runtime_rand replying 0 looped maphash.MakeSeed —
		// so trap instead of running the shim.
		if fn.Decl.Type != nil && fn.Decl.Type.Results != nil && len(fn.Decl.Type.Results.List) > 0 {
			where := ""
			if fn.Pkg != nil && fn.Pkg.Fset != nil {
				where = fmt.Sprintf(" declared at %s", fn.Pkg.Fset.Position(fn.Decl.Pos()))
			}
			name := fn.Name
			if fn.Pkg != nil && fn.Pkg.Name != "" {
				name = fn.Pkg.Name + "." + name
			}
			return nil, fmt.Errorf("no implementation for bodiless decl %s%s", name, where)
		}
	}
	// generic function called without instantiation (Id(40)): infer the
	// unbound type arguments from the runtime argument types. A method of
	// an instantiated generic type arrives partly bound — the receiver's
	// own type binds stay, only the method's own type params infer.
	if fn != nil && len(fn.TParams) > 0 && hasUnbound(fn.TParams, fn.Binds) {
		inferred, err := v.inferCached(fn, expandSpread(args), statics, spreadTd)
		if err != nil {
			return nil, err
		}
		fn = inferred
	}
	if err := fn.EnsureCompiled(); err != nil {
		// attribute the failure to the callee: name its declaration so the
		// trap (recorded at the caller's OpCall site) still says where the
		// uncompilable function lives.
		where := ""
		if fn.Decl != nil && fn.Pkg != nil && fn.Pkg.Fset != nil {
			where = fmt.Sprintf(" declared at %s", fn.Pkg.Fset.Position(fn.Decl.Pos()))
		}
		return nil, fmt.Errorf("compile %s%s: %w", fn.Name, where, err)
	}
	if len(v.frames) >= maxFrames {
		return nil, fmt.Errorf("stack exhausted: frame limit %d", maxFrames)
	}
	ch := fn.Chunk
	fr := framePool.Get().(*frame)
	fr.fn, fr.ch, fr.upvals = fn, ch, upvals
	// The window starts at the arena's live top: for a nested call
	// that is the caller's operand end; for a root call the arena is
	// reset. Dead tails from unwound frames are already reclaimed by
	// the unwinding frame's own stack reset, so nothing trims here —
	// trimming to a suspended top frame's end would wrongly cut into
	// the running frame's operands during a parked yield body.
	fr.v = v
	if len(v.frames) == 0 {
		v.vals = v.vals[:0]
	}
	if winArgs >= 0 {
		// an in-place call: the caller's top winArgs operands are the
		// arguments — the window covers them, no copy. Binding reads
		// them off the window and empties it afterward.
		fr.base = len(v.vals) - winArgs
		fr.sp = winArgs
		args = v.vals[fr.base:]
	} else {
		fr.base = len(v.vals)
		fr.sp = 0
	}
	fr.stack = v.vals[fr.base:]
	// locals is a value array: only slots the compiler marked boxed
	// (P4-2a) — or every slot on an unclassified chunk — get a cell;
	// plain slots hold their value in place and allocate nothing.
	// Boxed cells come out of one block — a cell per local was the
	// interpreter's largest allocation source (a frame per call,
	// NLocals+2 objects each); addressability is unchanged since
	// locals[i] may still be replaced or captured. A chunk that cannot
	// leak its cells recycles the block the pooled frame carries; a
	// chunk that can allocates fresh cells every call.
	nCells := 0
	for i := 0; i < ch.NLocals; i++ {
		if ch.Boxed == nil || (i < len(ch.Boxed) && ch.Boxed[i]) {
			nCells++
		}
	}
	var cells []runtime.Cell
	if nCells > 0 {
		if chunkCellsEscape(ch) {
			cells = make([]runtime.Cell, nCells)
		} else {
			if cap(fr.cells) < nCells {
				fr.cells = make([]runtime.Cell, nCells)
			}
			cells = fr.cells[:nCells]
		}
	}
	if cap(fr.locals) < ch.NLocals {
		fr.locals = make([]runtime.Value, ch.NLocals)
	} else {
		fr.locals = fr.locals[:ch.NLocals]
	}
	if cap(fr.ltyps) < ch.NLocals {
		fr.ltyps = make([]*runtime.TypeDef, ch.NLocals)
	} else {
		fr.ltyps = fr.ltyps[:ch.NLocals]
	}
	if cap(fr.slotRefs) < nCells {
		fr.slotRefs = make([]localCell, nCells)
	}
	refs := fr.slotRefs[:nCells]
	ci := 0
	for i := 0; i < ch.NLocals; i++ {
		if ch.Boxed == nil || (i < len(ch.Boxed) && ch.Boxed[i]) {
			cells[ci].Elem = runtime.NIL
			refs[ci].c = &cells[ci]
			fr.locals[i] = &refs[ci]
			ci++
		} else {
			fr.locals[i] = runtime.NIL
		}
	}
	// bind params; arity mismatches are compile errors in Go, so trap
	n := ch.NParams
	want := n
	if ch.IsVararg {
		want = n - 1 // the last slot collects the rest; zero extras is fine
	}
	if len(args) < want {
		return nil, fmt.Errorf("not enough arguments to %s: %d given, want %d", fn.Name, len(args), want)
	}
	if !ch.IsVararg && len(args) > n {
		return nil, fmt.Errorf("too many arguments to %s: %d given, want %d", fn.Name, len(args), n)
	}
	for i := 0; i < n; i++ {
		var a runtime.Value = runtime.NIL
		if i < len(args) {
			a = args[i]
		}
		if ch.IsVararg && i == n-1 {
			rest := runtime.NIL
			if i < len(args) {
				if sp, ok := args[len(args)-1].(*runtime.Spread); ok {
					// `f(xs...)` binds the variadic parameter to the
					// passed slice itself (Go's no-copy special case).
					// A virtual spread stays virtual: every element is
					// the type's single zero-size value either way.
					if len(args) == i+1 {
						// sharing the *Slice is safe because slice
						// headers are never mutated in place: a
						// callee's `xs = append(xs, …)` or `xs =
						// xs[1:]` stores a fresh header in its own
						// cell, while element writes reach the
						// shared backing array as in Go.
						rest = sp.S
					} else if !sp.S.Virtual() {
						// Go rejects extra variadic args in front of
						// `xs...`, so valid Go never reaches this arm;
						// minigo accepts `sum(5, xs...)` as an
						// extension (testdata/features SpreadTail),
						// which concatenates into a fresh pack — no
						// aliasing, since no single caller slice
						// matches the parameter.
						pack := &runtime.Slice{Elems: append(append([]runtime.Value{}, args[i:len(args)-1]...), sp.S.Elems...)}
						fr.vpack = pack
						rest = pack
					} else {
						rn := int64(len(args)-i-1) + sp.S.Len()
						rest = &runtime.Slice{N: rn, CapN: rn, Zero: sp.S.Zero, Typ: sp.S.Typ}
					}
				} else {
					pack := &runtime.Slice{Elems: append([]runtime.Value{}, args[i:]...)}
					fr.vpack = pack
					rest = pack
				}
			}
			fr.putLocal(i, rest)
			break
		}
		fr.putLocal(i, valueCopy(a))
	}
	if winArgs >= 0 {
		// params consumed — nil the arg slots (their values now live
		// in locals) so the arena's dead region pins nothing, and
		// empty the window for the first op.
		clear(fr.stack[:winArgs])
		fr.sp = 0
		fr.stack = fr.stack[:0]
	}
	return fr, nil
}

// expandSpread replaces a trailing `xs...` marker over a real slice with
// one argument per element, for callees that take a flat argument list
// (builtins, host funcs, generic inference). A virtual slice's marker
// stays: append and variadic binding expand it lazily, and every other
// callee fails on it rather than answering wrong.
func expandSpread(args []runtime.Value) []runtime.Value {
	n := len(args)
	if n == 0 {
		return args
	}
	sp, ok := args[n-1].(*runtime.Spread)
	if !ok || sp.S.Virtual() {
		return args
	}
	out := make([]runtime.Value, 0, n-1+len(sp.S.Elems))
	out = append(out, args[:n-1]...)
	return append(out, sp.S.Elems...)
}

func firstArg(args []runtime.Value) runtime.Value {
	if len(args) == 0 {
		return runtime.NIL
	}
	return args[0]
}

// popArgs pops argc args off the stack, preceded by the argc static
// typedefs the compiler emitted above them — one declared typedef per
// source argument so generic inference binds T to the argument's static
// type (Go's rule), not the dynamic type the value happens to hold.
// mode is the OpCall B flag: 1 expands a trailing slice/string spread
// (`f(xs...)`) and reports the spread slice's element typedef so a
// generic callee can still infer `Sum(n...)`'s T=int off a nil []int; 2
// spreads a lone call argument's result tuple (`f(g())` — the only
// multi-value spread Go allows).
func (v *VM) popArgs(f *frame, argc int, mode int, pos token.Pos) ([]runtime.Value, []*runtime.TypeDef, *runtime.TypeDef) {
	return v.popArgsBuf(f, argc, mode, pos, make([]runtime.Value, argc), make([]*runtime.TypeDef, argc))
}

// popArgsCall is popArgs reusing the frame's arg scratch — valid only on
// the synchronous OpCall path, where the popped slices are dead by the
// time the frame runs again. OpDefer and OpGo retain theirs past the
// call boundary and must use popArgs.
func (v *VM) popArgsCall(f *frame, argc int, mode int, pos token.Pos) ([]runtime.Value, []*runtime.TypeDef, *runtime.TypeDef) {
	argbuf, statbuf := f.argbuf, f.statbuf
	if cap(argbuf) < argc {
		argbuf = make([]runtime.Value, argc)
	} else {
		// nil only the delta — len(argbuf) is the previous call's
		// count, so [argc, len) is exactly where stale args sit.
		stale := len(argbuf)
		argbuf = argbuf[:argc]
		if argc < stale {
			clear(f.argbuf[argc:stale])
		}
	}
	if cap(statbuf) < argc {
		statbuf = make([]*runtime.TypeDef, argc)
	} else {
		statbuf = statbuf[:argc]
		for i := range statbuf {
			statbuf[i] = nil
		}
	}
	args, statics, spreadTd := v.popArgsBuf(f, argc, mode, pos, argbuf, statbuf)
	f.argbuf, f.statbuf = argbuf, statbuf
	return args, statics, spreadTd
}

// popArgsBuf pops into caller-provided buffers (sized at least argc):
// the spread modes may still reassign args to a longer or foreign slice.
func (v *VM) popArgsBuf(f *frame, argc int, mode int, pos token.Pos, args []runtime.Value, statics []*runtime.TypeDef) ([]runtime.Value, []*runtime.TypeDef, *runtime.TypeDef) {
	for i := argc - 1; i >= 0; i-- {
		if td, ok := f.pop().(*runtime.TypeDef); ok {
			statics[i] = td
		}
	}
	for i := argc - 1; i >= 0; i-- {
		args[i] = f.pop()
	}
	// a lone call argument spreads its result tuple into the callee's
	// params — `swap(swap(a, b))` — the only multi-value spread Go
	// allows; any other single value stays one argument.
	if mode == 2 && argc == 1 {
		if t, ok := args[0].(*runtime.Tuple); ok {
			args = t.Elems
		}
		// the lone static describes the call expression's declared result;
		// a tuple expansion past position 0 leaves it unused (staticAt
		// bounds-checks), and a single result — `f(error(e))` — keeps it.
		return args, statics, nil
	}
	// args stay lazy across the boundary: the callee's declared-param
	// coerce applies Go's constant-to-type conversion (`f('a')` into an
	// int param), and host marshaling materializes what is left.
	if mode == 1 {
		if argc == 0 {
			f.trap("spread call with no arguments")
		}
		// the last static belongs to the spread source — the slice type,
		// not the element — so positional arg slots stay aligned only up
		// to it.
		statics = statics[:argc-1]
		last := args[argc-1]
		var declared *runtime.TypeDef
		if dv, ok := runtime.Deref(last); ok {
			last = dv
		}
		if sv, ok := UnboxHostSlice(last); ok {
			// a boxed host slice (net.IP) spreads as its elements; the
			// callee sees a copy, not the host backing
			last = sv
		}
		if in, ok := last.(*runtime.IfaceNil); ok {
			last = &runtime.TypedNil{Typ: in.Typ}
		}
		if tn, ok := last.(*runtime.TypedNil); ok {
			if tn.Typ.Kind != runtime.KindSlice {
				f.trap("cannot use nil %s as spread argument", tdName(tn.Typ))
			}
			declared = tn.Typ
			args = args[:argc-1] // nil slice spreads to zero args
			return args, statics, v.elemTypedef(f, declared)
		}
		var namedTyp *runtime.TypeDef
		if n, ok := last.(*runtime.Named); ok {
			namedTyp = n.Typ
			last = n.V
		}
		// a declared untyped const keeps its UConst box past the call
		// boundary — `const s = "ab"; append(b, s...)` spreads like
		// the literal, so materialize before the string check.
		last = v.materialize(f, last)
		if str, ok := last.(string); ok {
			// append([]byte, s...) spreads the string's bytes — the
			// only legal string spread in Go.
			args = args[:argc-1]
			for i := 0; i < len(str); i++ {
				args = append(args, int64(str[i]))
			}
			return args, statics, nil
		}
		if _, isNil := last.(runtime.Nil); isNil {
			args = args[:argc-1]
			return args, statics, nil
		}
		if last == nil || last == runtime.NIL {
			// f(nil...) on a nil slice expands to zero arguments
			args = args[:argc-1]
			return args, statics, nil
		}
		s, ok := last.(*runtime.Slice)
		if !ok {
			f.trap("cannot use %T as spread argument", last)
		}
		declared = s.Typ
		// the slice rides to the callee whole: a script variadic
		// parameter binds to it directly (Go passes the slice itself,
		// so element writes and in-capacity appends reach the
		// caller), and callees without a bytecode frame expand it
		// through expandSpread.
		args[argc-1] = &runtime.Spread{S: s}
		spreadTd := v.elemTypedef(f, declared)
		if spreadTd == nil && namedTyp != nil {
			spreadTd = v.elemTypedef(f, namedTyp)
		}
		return args, statics, spreadTd
	}
	return args, statics, nil
}
