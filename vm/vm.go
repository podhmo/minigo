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
	"go/token"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Hooks are the engine-provided services a [VM] needs, set in [VM].H.
// Builtin and Materialize are required to resolve global names; a hook
// whose comment describes nil behavior is optional.
type Hooks struct {
	// Prof, when set, samples script call stacks (see Profiler).
	Prof *Profiler
	// Builtin resolves a predeclared name (len, append, int, ...).
	Builtin func(name string) (runtime.Value, bool)
	// Materialize builds the runtime value for a package-level decl
	// (function, type, var, const) on first access.
	Materialize func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)
	// CompileExpr compiles a single expression into a chunk for
	// OpEvalAST and for constant array lengths such as [N*2]int; the
	// expression sees globals, file imports and builtins but not the
	// caller's locals (see compile.Expr). Nil makes OpEvalAST trap.
	CompileExpr func(pkg *runtime.Package, file *syntax.File, e ast.Expr) (*bytecode.Chunk, error)
	// CompileScopedExpr compiles an expression with a caller-scope overlay
	// (name -> caller slot/upval index) for special forms (see
	// compile.ExprScoped). Nil makes a special form's Eval fail.
	CompileScopedExpr func(pkg *runtime.Package, file *syntax.File, e ast.Expr, locals, upvals map[string]int) (*bytecode.Chunk, error)
	// Special resolves a canonical symbol to its special-form handler.
	// Nil makes every special-form call trap.
	Special func(id runtime.SymbolID) (runtime.SpecialFunc, bool)
	// MethodsOf returns the method names callable on a dynamic value
	// (structs: declared + promoted; host values: reflect method set).
	MethodsOf func(v runtime.Value) (map[string]bool, error)
	// MethodSetOf is an optional extended MethodsOf that also reports
	// whether the returned set may be incomplete because an embedded
	// type failed to resolve. Nil falls back to MethodsOf (complete).
	MethodSetOf func(v runtime.Value) (map[string]bool, bool, error)
	// IfaceReqs returns the required method set of an interface typedef.
	IfaceReqs func(td *runtime.TypeDef) (map[string]bool, error)
	// ElemOf returns the element typedef of a container typedef ([]T->T,
	// map[K]V->V, chan T->T, *T->T) — used by elided composite literal
	// elements (`{{1,2}}` inside `[][]int`).
	ElemOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// TypeMethods returns the method set of a typedef (declared +
	// promoted; pointer receivers included) — used when an interface
	// satisfaction check must run against a type rather than a value
	// (typed nils in assertions).
	TypeMethods func(td *runtime.TypeDef) (map[string]bool, error)
	// IfaceSigs returns the required methods of an interface typedef
	// that carry a declared signature, as Function shells spelling it
	// in their declaring context. Facade members (MReqs without an
	// AST) are absent and satisfy by name alone. Nil disables
	// signature comparison in interface satisfaction.
	IfaceSigs func(td *runtime.TypeDef) (map[string]*runtime.Function, error)
	// MethodFuncsOf returns the signature-bearing methods callable on
	// a dynamic value — declared and promoted script methods plus
	// synthesized interface members; host reflect methods carry no
	// decl signature, stay absent here and satisfy by name alone.
	MethodFuncsOf func(v runtime.Value) (map[string]*runtime.Function, error)
	// TypeMethodFuncs is MethodFuncsOf's typedef variant — the
	// declared method set of a type rather than a value.
	TypeMethodFuncs func(td *runtime.TypeDef) (map[string]*runtime.Function, error)
	// SigMemo, when set, remembers member signature comparisons across
	// the engine's VMs (see runtime.SigMemo). Nil compares every time.
	SigMemo *runtime.SigMemo
	// Underlying resolves a KindAlias typedef to its underlying typedef.
	Underlying func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// AliasOf resolves a KindAlias typedef to its direct target typedef
	// (one hop — `type A = B` gives B itself, not B's underlying shape).
	AliasOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// FieldTypes returns the declared type of each struct field, parallel
	// to td.Fields (nil entries leave the field zero NIL), so `var s T`
	// materializes typed field zeros like Go.
	FieldTypes func(td *runtime.TypeDef) ([]*runtime.TypeDef, error)
	// ResolveType resolves a type expression in the context of typedef td
	// (its package scope, file imports and generic binds): named types
	// materialize lazily through the index, composite types yield
	// anonymous typedefs. Used for promoted-field lookup, constraint
	// resolution and generalized type inference.
	ResolveType func(td *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error)
	// FieldPath answers where selector field name lands in the declared
	// struct shape of td — the index-path convention of reflect's
	// Field.Index. decided reports the declaration answered (found:
	// a unique field winner with index set; missing: the name is no
	// field); !decided falls back to the value-level member search.
	// Nil keeps the value walk.
	FieldPath func(td *runtime.TypeDef, name string) (index []int, found, decided bool)
	// HostTwin returns the engine's own typedef for the named host type
	// rt — the source declaration when rt's package is interpreted from
	// source (go/ast), the bound typedef when it is host-bound — or nil
	// when the engine has not loaded rt's package.
	HostTwin func(rt reflect.Type) *runtime.TypeDef
	// Linkname resolves a bodiless function carrying a two-arg
	// `//go:linkname local importpath.symbol` doc directive to the
	// target's callable value — stdlib internals (net/http's
	// readMIMEHeader -> net/textproto's real body) wire their
	// implementation this way. A nil hook, a one-arg directive, or an
	// unresolvable target reports false and the declaration keeps its
	// zero-return shim.
	Linkname func(vc runtime.ScriptCaller, fn *runtime.Function) (runtime.Value, bool, error)
	// HostErr boxes a script error value as a host error that keeps the
	// script value (and its Unwrap/Is/As methods) reachable: a script
	// Write's error handed to host fmt.Fprintf, or a script error passed
	// to a host `error` parameter, comes back to the script as itself.
	// Nil falls back to the scriptIface proxy.
	HostErr func(vc runtime.MemberCaller, v runtime.Value) error
}

// VM is a stack machine. Create one as a struct literal with [Hooks] set
// in H, then run code with [VM.Call].
//
// Each VM is confined to one goroutine; the `go` statement forks a fresh
// VM sharing the same process so goroutines genuinely interleave on host
// goroutines. Only package state (globals, the materialization cache,
// channels) is shared between them. A Call from another goroutine — a
// host-retained callback firing off-VM (WaitGroup.Go, time.AfterFunc) — is
// safe: it runs on a child VM of the same process.
type VM struct {
	H Hooks

	// ifaceMemo memoizes interface satisfaction for this VM (one per
	// goroutine, so unshared); ifaceEpoch is the method-set epoch it
	// was filled under.
	ifaceMemo  map[ifaceKey]bool
	ifaceEpoch uint64

	// frames is the live call stack (innermost last); recover() consults it.
	frames []*frame
	// profTick is the Profiler tick this VM last checked in at.
	profTick uint64
	// callMu guards callDepth/callGid: the goroutine id owning the
	// in-flight Call(s). A foreign goroutine's Call spawns instead.
	callMu    sync.Mutex
	callDepth int
	callGid   int64
	// inflight is the script panic currently being propagated, visible to
	// recover() only while a frame's defers are running.
	inflight *runtime.Panic
	// unwindDepth is the frame-stack index where the inflight panic's
	// deferred-call chain roots — the gopanic position in Go's terms.
	// recover() counts real frames pushed at or above it: exactly one
	// means legal. Meaningful only while inflight is non-nil.
	unwindDepth int
	// unwinding lists frames popped by panics, each tagged with the
	// panic that unwound it — kept so runtime.Callers still sees the
	// panicking frames while defers run, like Go's traceback does. A
	// consumed panic's entries are dropped at the transition: Go lists
	// no unwound frames for a dead unwind.
	unwinding []unwoundFrame
	// consumedPanic records the panic recover() just consumed, so
	// unwind can drop its unwound frames even when a superseding panic
	// was in flight (the consumed one differs from the frame's own).
	consumedPanic *runtime.Panic
	// draining counts unwind loops currently running a frame's defers.
	// While one is live a panic unwinding a deferred call has not
	// reached its process boundary yet — the owner still has defers
	// to run — so failProc holds off even though the popped owner no
	// longer counts in len(v.frames).
	draining int
	// pcSites is the registry behind runtime.Callers' opaque uintptr
	// handles: CallerPCs appends a snapshot, CallerFrame resolves one.
	pcSites []runtime.CallSite

	// srcCache maps filename -> source lines for traceback snippets;
	// populated lazily and only on the error path.
	srcCache map[string][]string

	// proc is the process this VM belongs to: every goroutine spawned by
	// `go` shares it. The outermost Call owns it; when that Call returns,
	// done closes and every parked channel op aborts with procExit (like
	// a Go process exiting under live goroutines). A goroutine's panic
	// fails the process the same way, aborting everyone else.
	// Written under callMu, but read without it — a host callback on a
	// foreign goroutine (context.AfterFunc, an http handler) checks it
	// while the owner's ReleaseProc clears it — so it is atomic; read it
	// once into a local for a check-then-use.
	proc      atomic.Pointer[process.Proc]
	procHolds int
	// procDead records that this VM's process ended — a dead process
	// spawns nothing, so a timer or host callback bound to a finished
	// run never starts on a detached process.
	procDead bool
	task     *runtime.Task // this VM's own spawn handle; nil on the root
	// syncCall marks a VM spawned for a synchronous re-entry — a
	// callback the host invokes inside a blocking host call
	// (sync.Once.Do's f) reaching Call from the helper goroutine. Its
	// panic propagates back through the joining Call instead of
	// failing the process the way a real goroutine's would.
	syncCall bool

	// lenB/capB cache the predeclared len/cap builtins for
	// OpLenCallFold's identity probe — resolved lazily on the first
	// fold, since a bare VM's Hooks may carry no Builtin lookup.
	lenB, capB runtime.Value
	lenCapDone bool
}

// assignCell stores into a cell, coercing to the cell's declared type
// first when one was stamped by a `var x T` / `x := T(v)` / typed-param
// coerce. `x = v` then enforces the same assignability as `var x T = v`,
// and a named basic type keeps its tag across plain assignment.
func (v *VM) assignCell(f *frame, c *runtime.Cell, val runtime.Value) {
	if c.ReadOnly {
		f.trap("cannot assign to constant")
	}
	if c.Typ != nil {
		val = v.coerce(f, val, c.Typ)
	} else {
		// an unstamped cell's declared type is the tag its initial value
		// carried — `x := int8(1)` leaves Named{int8} in the cell, and a
		// later `x = v` enforces the same assignability. Bare values
		// answer through their dynamic type.
		var ct *runtime.TypeDef
		if en, ok := c.Elem.(*runtime.Named); ok && en.Typ != nil {
			ct = en.Typ
		} else {
			ct = v.typeOfValue(c.Elem)
		}
		if vn, ok := val.(*runtime.Named); ok && vn.Typ != nil && ct != nil &&
			vn.Typ.Name != "" && ct.Name != "" && ct.Kind != runtime.KindInterface &&
			!sameTypeDef(vn.Typ, ct) {
			f.trap("cannot use value (type %s) as %s in assignment", spelledTyp(vn.Typ), spelledTyp(ct))
		}
		if u, isConst := constPayload(val); isConst {
			// a constant bound into a variable takes the variable's
			// type — `d := Second; d = 5` stays a Duration.
			if ct != nil {
				val = v.materializeConst(f, u, ct)
			} else {
				val = v.materialize(f, val)
			}
		} else if sameBareScalar(c.Elem, val) {
			// a bare scalar into a cell holding the same bare kind: ct is
			// that kind's builtin type, so re-typing is the identity —
			// skip the UConst round trip on the hot `x = x + 1` path.
		} else if u2, isScalar := bareScalarConst(val); isScalar && ct != nil {
			// `x = 300` re-types into the cell's tag — an out-of-range
			// literal traps like gc's compile-time overflow error.
			val = v.materializeConst(f, u2, ct)
		} else if _, isNil := val.(runtime.Nil); isNil {
			// `p = nil` keeps the variable's inferred type — a *T var
			// holds a nil *T, not an untyped nil (Go's zero is typed).
			// The nil keeps the tag so member selects and comparisons
			// still resolve on it.
			if pt := v.pointeeTag(c.Elem); pt != nil {
				val = &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindPointer, Elem: pt}}
			} else if tag := containerTyp(c.Elem); tag != nil && v.nilableTypedef(tag) {
				val = &runtime.TypedNil{Typ: tag}
			}
		}
	}
	if !overwriteArray(c, val) {
		c.Elem = valueCopy(val)
	}
}

// checkAddrBase panics like Go when the address-of target's base is a
// nil pointer (invalid memory address) or a nil slice (index out of
// range — key renders the failed index).
func (v *VM) checkAddrBase(base, key runtime.Value) {
	tn, isNil := asTypedNil(base)
	if !isNil {
		return
	}
	switch v.peelNamed(tn.Typ).Kind {
	case runtime.KindPointer:
		panic(runtime.NilDerefPanic())
	case runtime.KindSlice:
		panic(runtime.BoundsPanic(runtime.Unwrap(key), 0))
	}
}

// assignRef stores a value into a resolved assignment target — the
// phase-2 store of OpSetRefs. The ref carries the target's storage
// shape: IndexRef/FieldRef go through the typed index/field stores so
// element typedefs still coerce; a bare Cell is a variable or pointer
// cell; a Named pointer unwraps in setIndirect.
func (v *VM) assignRef(f *frame, ref, val runtime.Value) {
	switch r := ref.(type) {
	case *runtime.IndexRef:
		v.setIndex(f, r.Base, r.Key, val)
		return
	case *runtime.FieldRef:
		v.setField(f, r.Base, r.Name, val)
		return
	case *runtime.Cell:
		if !r.ReadOnly && overwriteArray(r, val) {
			return
		}
		v.assignCell(f, r, val)
		return
	case *runtime.DerefRef:
		loc, ok := runtime.Deref(r.Ptr)
		if !ok {
			// `*m[k].p = v`: the pointer field read crosses the map
			// element's copy — resolve it like an interior write.
			loc, ok = v.refThrough(f, r.Ptr)
		}
		if !ok {
			f.trap("deref of non-pointer %T", r.Ptr)
			return
		}
		// `*p = T{...}` overwrites the pointee struct in place (see
		// overwriteStruct) so implicit-&v receivers keep the store.
		if c, isCell := loc.(*runtime.Cell); isCell && !c.ReadOnly {
			cv := val
			if c.Typ != nil {
				cv = v.coerce(f, val, c.Typ)
			}
			if overwriteStruct(c, cv) {
				return
			}
		}
		v.assignRef(f, loc, val)
		return
	case runtime.Nil:
		return // `_` — the value is discarded
	}
	v.setIndirect(f, ref, val)
}

// setIndirect stores through a pointer-like ref — the OpSetInd body
// (`*p = v`) and the pointer-shaped targets of OpSetRefs.
func (v *VM) setIndirect(f *frame, ref, val runtime.Value) {
	if tn, ok := asTypedNil(ref); ok && tn.Typ.Kind == runtime.KindPointer {
		panic(runtime.NilDerefPanic())
	}
	// a Named pointer unwraps to its cell so the pointee's
	// declared type still constrains the store (`*p = v` on a
	// `var p P` where P is `type P *Sq`, or `*s = v` through a
	// `(*stringValue)(p)` view). The pointer's element typedef
	// wins over the cell's own tag: the cell may be shared with a
	// differently-typed alias (`*string` vs `*stringValue`), and
	// the stored value un-wraps so the other view keeps its tag.
	ur := ref
	var ptrTd *runtime.TypeDef
	for {
		n, isNamed := ur.(*runtime.Named)
		if !isNamed {
			break
		}
		if n.Typ != nil && n.Typ.Kind == runtime.KindPointer {
			ptrTd = n.Typ
		}
		ur = n.V
	}
	if c, ok := ur.(*runtime.Cell); ok {
		if c.ReadOnly {
			f.trap("cannot assign to constant")
		}
		tgt := c.Typ
		if ptrTd != nil {
			if et := v.elemTypedef(f, ptrTd); et != nil {
				tgt = et
			}
		}
		if tgt != nil {
			val = v.coerce(f, val, tgt)
		}
		if ptrTd != nil {
			val = runtime.Unwrap(val)
			// A converted pointer only re-views the storage — the cell
			// keeps its own declared tag, so `*(*uint)(&w) = 7` leaves
			// `var w W` a W (its method set still resolves) instead of
			// a bare uint. Cells holding an untagged element (the bare
			// `s := "a"` of PtrConvShared) keep the bare store, and
			// interface/typed-nil elements are left as the coerce wrote
			// them — Tag would wrap a nil where a plain `x = nil`
			// stores a TypedNil.
			if en, ok := c.Elem.(*runtime.Named); ok && tagIsNamed(en.Typ) &&
				en.Typ.Kind != runtime.KindInterface {
				if _, isNil := val.(*runtime.TypedNil); isNil {
					val = &runtime.TypedNil{Typ: en.Typ}
				} else {
					val = runtime.Tag(en.Typ, val)
				}
			}
		}
	}
	// `*p = T{...}` overwrites the pointee in place: a pointer
	// receiver bound through an implicit `&v` (v.M(), x.f.M()) wraps
	// the variable's struct in a fresh cell, so rebinding that cell
	// would drop the store; copying the fields keeps the write visible
	// to every holder of the struct, like Go's memory overwrite.
	if overwriteStruct(ur, val) || overwriteArray(ur, val) {
		return
	}
	if !runtime.SetRef(ref, val) {
		f.trap("indirect store to non-pointer %T", ref)
	}
}

// overwriteStruct stores val into the struct a `*p` target already
// holds, field by field, when both are structs of the same type. It
// reports false (leaving the store to SetRef) for any other shape.
func overwriteStruct(ref, val runtime.Value) bool {
	if dr, ok := ref.(*runtime.DerefRef); ok {
		loc, ok := runtime.Deref(dr.Ptr)
		if !ok {
			return false
		}
		ref = loc
	}
	c, ok := ref.(*runtime.Cell)
	if !ok {
		return false
	}
	if overwriteArray(c, val) {
		return true
	}
	dst, ok := c.Elem.(*runtime.Struct)
	if !ok {
		return false
	}
	src, ok := runtime.Copy(val).(*runtime.Struct)
	if !ok || src.Def != dst.Def || len(src.Fields) != len(dst.Fields) {
		return false
	}
	copy(dst.Fields, src.Fields)
	return true
}

// overwriteArray stores val into the fixed array a cell already holds,
// element by element, when both are equal-length arrays — the array
// counterpart of overwriteStruct. The cell's backing is shared with
// slice views (`s := x[:]`) and (*[N]T)(s) conversion results, so
// rebinding it would detach those aliases where Go's memory overwrite
// keeps them (`*p = arr` on `p := &x`, `q := (*[N]T)(s); *q = arr`,
// and plain `x = arr` alike).
func overwriteArray(ref, val runtime.Value) bool {
	if dr, ok := ref.(*runtime.DerefRef); ok {
		loc, ok := runtime.Deref(dr.Ptr)
		if !ok {
			return false
		}
		ref = loc
	}
	c, ok := ref.(*runtime.Cell)
	if !ok {
		return false
	}
	return overwriteArrayIn(c.Elem, val)
}

// overwriteArrayIn copies val's elements into old when both are arrays
// of the same length — the in-place store behind overwriteArray, also
// used for struct fields (`s.a = b` with `p := s.a[:]` live). Virtual
// slices carry no backing Elems, so there is nothing to overwrite.
func overwriteArrayIn(old, val runtime.Value) bool {
	da, ok := runtime.Unwrap(old).(*runtime.Slice)
	if !ok || !runtime.ArrayTypedef(da.Typ) || da.Virtual() {
		return false
	}
	sa, ok := runtime.Unwrap(runtime.Copy(val)).(*runtime.Slice)
	if !ok || sa.Virtual() || !runtime.ArrayTypedef(sa.Typ) || len(sa.Elems) != len(da.Elems) {
		return false
	}
	overwriteArrayElems(da, sa)
	return true
}

// overwriteArrayElems copies one array's elements into another's
// backing. Array-typed elements recurse: `x[0][:]` slices into the
// inner array's backing, and a wholesale `copy(dst.Elems, src.Elems)`
// would swap the inner *Slice header so those views keep reading the
// stale backing — Go's memory overwrite writes through every level.
func overwriteArrayElems(dst, src *runtime.Slice) {
	for i := range dst.Elems {
		di, dok := runtime.Unwrap(dst.Elems[i]).(*runtime.Slice)
		si, sok := runtime.Unwrap(src.Elems[i]).(*runtime.Slice)
		if dok && sok && isArrayTyp(di.Typ) && isArrayTyp(si.Typ) && len(di.Elems) == len(si.Elems) {
			overwriteArrayElems(di, si)
			continue
		}
		dst.Elems[i] = src.Elems[i]
	}
}

// Member implements [runtime.VMCaller].
func (v *VM) Member(base runtime.Value, name string) (m runtime.Value, ok bool) {
	// selectMember over a dummy frame, reporting failure instead of
	// trapping so host intrinsics can probe for optional members.
	defer func() {
		if r := recover(); r != nil {
			m, ok = nil, false
		}
	}()
	fr := &frame{fn: &runtime.Function{Name: "<member>"}}
	return v.selectMember(fr, base, name), true
}

// MethodSetOf implements [runtime.VMCaller]. The set is nil when [Hooks]
// provide no method-set lookup or the lookup fails.
func (v *VM) MethodSetOf(x runtime.Value) (map[string]bool, bool) {
	if v.H.MethodSetOf != nil {
		if set, unsure, err := v.H.MethodSetOf(x); err == nil {
			return set, unsure
		}
	}
	if v.H.MethodsOf == nil {
		return nil, false
	}
	set, err := v.H.MethodsOf(x)
	if err != nil {
		return nil, true // unsure: don't gate on a failed lookup
	}
	return set, false
}

// Package implements [runtime.VMCaller]: the package of the innermost
// running frame, i.e. the builtin's caller.
func (v *VM) Package() *runtime.Package {
	if n := len(v.frames); n > 0 {
		if fn := v.frames[n-1].fn; fn != nil {
			return fn.Pkg
		}
	}
	return nil
}

// TypeOf implements [runtime.VMCaller].
func (v *VM) TypeOf(x runtime.Value) *runtime.TypeDef {
	return v.typeOfValue(x)
}

// ResolveType implements [runtime.VMCaller] by delegating to
// [Hooks].ResolveType; it fails when that hook is nil.
func (v *VM) ResolveType(td *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if v.H.ResolveType == nil {
		return nil, fmt.Errorf("cannot resolve type %T", x)
	}
	return v.H.ResolveType(td, x)
}

// FieldTypes implements [runtime.VMCaller] by delegating to
// [Hooks].FieldTypes; it fails when that hook is nil.
func (v *VM) FieldTypes(td *runtime.TypeDef) ([]*runtime.TypeDef, error) {
	if v.H.FieldTypes == nil {
		return nil, fmt.Errorf("cannot resolve field types")
	}
	return v.H.FieldTypes(td)
}

// ElemOf implements [runtime.VMCaller] by delegating to [Hooks].ElemOf;
// it fails when that hook is nil.
func (v *VM) ElemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if v.H.ElemOf == nil {
		return nil, fmt.Errorf("cannot resolve element type")
	}
	return v.H.ElemOf(td)
}

// Copy implements [runtime.VMCaller]. A constant operand is materialized
// to a concrete value.
func (v *VM) Copy(x runtime.Value) runtime.Value {
	// A riding constant (T(c), which stays a constant inside expressions)
	// materializes at this assignment boundary so stored elements carry
	// concrete values.
	if u, ok := constPayload(x); ok {
		f := v.topFrame()
		if n, isN := x.(*runtime.Named); isN {
			return valueCopy(v.materializeConst(f, u, n.Typ))
		}
		return valueCopy(v.materialize(f, u))
	}
	return valueCopy(x)
}

// resolveGlobal resolves a name in file scope order: imports, package
// globals (including lazily materialized decls), dot imports, builtins.
func (v *VM) resolveGlobal(f *frame, name string) runtime.Value {
	mv, err := v.resolveGlobalE(f, name)
	if err != nil {
		f.trap("%s", err)
	}
	return mv
}

// globalSlot is a cached OpGlobal resolution, valid while the
// package's Globals generation is unchanged. A package var caches its
// cell (read live); everything else caches the resolved value.
type globalSlot struct {
	env  *runtime.Env
	gen  uint64
	cell *runtime.Cell
	val  runtime.Value
}

// resolveGlobalSite resolves name like resolveGlobal and, when the
// resolution only depends on state that bumps the package's Globals
// generation, records it in site.
func (v *VM) resolveGlobalSite(f *frame, name string, site *bytecode.GlobalSite) runtime.Value {
	env := f.fn.Pkg.Globals
	// read the generation first: a change during resolution (a lazy
	// materialization, another goroutine's Set) leaves the slot stale
	// on arrival, so the next read resolves again.
	gen := env.Gen()
	val, cell, cacheable, err := v.resolveGlobalKind(f, name)
	if err != nil {
		f.trap("%s", err)
	}
	if cacheable {
		site.Cache.Store(&globalSlot{env: env, gen: gen, cell: cell, val: val})
	}
	return val
}

// resolveGlobalE is resolveGlobal without the trap: failures return as
// errors so callers (e.g. SpecialContext.Resolve) can report them.
func (v *VM) resolveGlobalE(f *frame, name string) (runtime.Value, error) {
	val, _, _, err := v.resolveGlobalKind(f, name)
	return val, err
}

// resolveGlobalKind is resolveGlobalE that also reports whether the
// result may be cached per site (see globalSlot) and, for a package
// var, the cell it was read from. File scopes, imports and the package
// index are covered by Env.Touch at their edit sites; dot-imported
// names read another package's env, so they are never cached.
func (v *VM) resolveGlobalKind(f *frame, name string) (val runtime.Value, cell *runtime.Cell, cacheable bool, err error) {
	pkg := f.fn.Pkg
	file := fileOf(f, pkg)
	// 1. file imports
	if file != nil {
		if ref, ok := pkg.Scopes[file][name]; ok {
			return ref, nil, true, nil
		}
	}
	// 2. package globals / lazy members
	if gv, ok := pkg.Globals.Get(name); ok {
		if c, isCell := gv.(*runtime.Cell); isCell {
			return c.Elem, c, true, nil
		}
		return gv, nil, true, nil
	}
	if pkg.Index != nil {
		if d, ok := pkg.Index.Lookup(name); ok {
			mv, err := v.H.Materialize(pkg, d)
			if err != nil {
				return nil, nil, false, fmt.Errorf("materialize %s: %s", name, err)
			}
			pkg.Globals.Set(name, mv)
			return mv, nil, false, nil
		}
	}
	// 2.5 unnamed imports whose package name differs from the path's
	// last element: Scopes keys on the path basename, so `foo.V` misses
	// when the package clause says `package realname`. Learn the real
	// name lazily by materializing the package.
	if file != nil {
		var loadErr error
		var loadPath string
		for _, ref := range pkg.Imports[file] {
			if ref.Alias != "" {
				continue
			}
			p, err := ref.Materialize()
			if err != nil {
				// a denied or broken import explains the failure better
				// than a bare "undefined" once every candidate is missed.
				if loadErr == nil {
					loadErr, loadPath = err, ref.Path
				}
				continue
			}
			if p != nil && p.Name == name {
				return ref, nil, true, nil
			}
		}
		if loadErr != nil {
			return nil, nil, false, fmt.Errorf("import %s: %w", loadPath, loadErr)
		}
	}
	// 3. dot imports: index without initializing, then initialize the package
	// only when the requested name exists there.
	if file != nil {
		var imported *runtime.Package
		for _, ref := range pkg.Imports[file] {
			if ref.Alias != "." {
				continue
			}
			if !token.IsExported(name) && !ref.AllNames {
				continue
			}
			p, err := ref.Materialize()
			if err != nil {
				return nil, nil, false, fmt.Errorf("dot import %s: %s", ref.Path, err)
			}
			_, inGlobals := p.Globals.Get(name)
			inIndex := false
			if p.Index != nil {
				_, inIndex = p.Index.Lookup(name)
			}
			if !inGlobals && !inIndex {
				continue
			}
			if imported != nil {
				return nil, nil, false, fmt.Errorf("ambiguous dot-imported name: %s", name)
			}
			imported = p
		}
		if imported != nil {
			mv, err := v.memberOf(imported, name)
			if err != nil {
				return nil, nil, false, fmt.Errorf("dot import %s: %s", imported.Path, err)
			}
			if c, isCell := mv.(*runtime.Cell); isCell {
				return c.Elem, nil, false, nil
			}
			if _, cached := imported.Globals.Get(name); !cached {
				imported.Globals.Set(name, mv) // see selectMember's ImportRef case
			}
			return mv, nil, false, nil
		}
	}
	// 4. builtins
	if bv, ok := v.H.Builtin(name); ok {
		// a dot import could still gain the name later (another
		// package's env, invisible to this package's generation).
		cacheable = true
		if file != nil {
			for _, ref := range pkg.Imports[file] {
				if ref.Alias == "." {
					cacheable = false
					break
				}
			}
		}
		return bv, nil, cacheable, nil
	}
	return nil, nil, false, fmt.Errorf("undefined: %s", name)
}

// importGlobalCell resolves &pkg.name to the package global's storage
// cell, or nil when the member is not a cell-backed variable (a host
// binding), leaving the caller's generic ref path in charge.
func (v *VM) importGlobalCell(f *frame, b *runtime.ImportRef, name string) *runtime.Cell {
	if !token.IsExported(name) && !b.AllNames {
		f.trap("cannot refer to unexported name %s.%s", b.Path, name)
	}
	p, err := b.Materialize()
	if err != nil {
		f.trap("import %s: %s", b.Path, err)
	}
	mv, err := v.memberOf(p, name)
	if err != nil {
		f.trap("%s", err)
	}
	c, _ := mv.(*runtime.Cell)
	return c
}

// runInit executes a package __init__ function as a nested call on this VM.
func (v *VM) runInit(fn *runtime.Function) error {
	_, err := v.call(fn, nil, nil, nil)
	return err
}

// zeroValue returns the Go zero value of a typedef: runtime.Zero plus a
// recursive fill of struct fields by their declared types via the
// FieldTypes hook (`var s Sq` -> Sq{Side: 0}, matching Go). Fields whose
// types don't resolve stay NIL. Ancestor typedefs are pinned so a
// recursive struct shape bottoms out instead of diverging.
func (v *VM) zeroValue(f *frame, td *runtime.TypeDef) runtime.Value {
	return v.zeroSeen(f, td, map[*runtime.TypeDef]bool{})
}

func (v *VM) zeroSeen(f *frame, td *runtime.TypeDef, seen map[*runtime.TypeDef]bool) runtime.Value {
	if td == nil || seen[td] {
		return runtime.NIL
	}
	seen[td] = true
	defer delete(seen, td) // sibling fields may share a type
	orig := td
	// Named basics peel to their underlying typedef so `type S string`
	// zeros as "" and `type A B` chains resolve transitively. The cap
	// keeps a self-referential chain from looping forever.
	td = v.peelNamed(td)
	// a nil interface value is a typed nil of the interface kind: it
	// compares equal to nil and to other nil interfaces, but a typed
	// nil boxed in an interface (*ET)(nil) does NOT equal it.
	if td.Kind == runtime.KindInterface {
		return v.wrapZero(orig, &runtime.IfaceNil{Typ: td})
	}
	// an array typedef materializes a fixed-length slice of element
	// zeros — `var a [3]int` yields [0 0 0], not a nil slice.
	if n, isArr := v.arrayLen(f, td); isArr {
		zv := v.zeroSeen(f, v.elemTypedef(f, td), seen)
		el := make([]runtime.Value, n)
		for i := range el {
			el[i] = valueCopy(zv)
		}
		return v.wrapZero(orig, &runtime.Slice{Elems: el, Typ: td})
	}
	z := runtime.Zero(td)
	s, ok := z.(*runtime.Struct)
	if ok && v.H.FieldTypes != nil {
		fts, err := v.H.FieldTypes(td)
		if err != nil {
			if f != nil {
				f.trap("zero of %s: %s", tdName(td), err)
			}
			return v.wrapZero(orig, z)
		}
		for i := range s.Fields {
			if i < len(fts) && fts[i] != nil {
				s.Fields[i] = v.zeroSeen(f, fts[i], seen)
			}
		}
	}
	return v.wrapZero(orig, z)
}

// wrapZero restores the declared identity on a zero value built from the
// peeled underlying typedef: a declared named basic type gets a Named tag
// (`var x MyInt` reads as MyInt, not int64), while a nilable zero re-tags
// its TypedNil to the declared name (`var p P2` where P2's underlying is
// a pointer type). The sized builtins tag by the same rule coerceConcrete
// uses, so `var u uint8` and `var i int64` keep their declared width.
func (v *VM) wrapZero(td *runtime.TypeDef, z runtime.Value) runtime.Value {
	if td.Kind != runtime.KindNamedBasic ||
		!(declaredType(td) || sizedIntName(td.Name) ||
			td.Name == "int64" || td.Name == "float32") {
		return z
	}
	if _, ok := z.(*runtime.TypedNil); ok {
		return &runtime.TypedNil{Typ: td}
	}
	if _, ok := z.(runtime.Nil); ok {
		return z
	}
	return runtime.Tag(td, z)
}

// Zero implements [runtime.VMCaller].
func (v *VM) Zero(td *runtime.TypeDef) runtime.Value {
	return v.zeroSeen(nil, td, map[*runtime.TypeDef]bool{})
}

// topFrame returns the innermost running frame — the caller whose locals
// may hold a function-local typedef — or nil outside a call.
func (v *VM) topFrame() *frame {
	if n := len(v.frames); n > 0 {
		return v.frames[n-1]
	}
	return nil
}

// ElemZero implements [runtime.VMCaller]: the zero value make() fills a
// new slice with.
func (v *VM) ElemZero(td *runtime.TypeDef) runtime.Value {
	et := v.elemTypedef(v.topFrame(), td)
	if et == nil {
		return runtime.NIL
	}
	return v.zeroSeen(nil, et, map[*runtime.TypeDef]bool{})
}

// ArrayLenOf implements [runtime.VMCaller].
func (v *VM) ArrayLenOf(td *runtime.TypeDef) (int64, bool) {
	return v.arrayLen(v.topFrame(), td)
}
