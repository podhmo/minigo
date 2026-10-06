// Package vm is the minigo stack-machine interpreter. One VM call executes
// one frame; nested calls recurse through VM.Call. Script panics and traps
// both unwind via Go panic and are converted to errors at the call boundary.
package vm

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/token"
	"io"
	"math"
	"os"
	"reflect"
	goruntime "runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Hooks are the engine-provided services the VM needs.
type Hooks struct {
	// Builtin resolves a predeclared name (len, append, int, ...).
	Builtin func(name string) (runtime.Value, bool)
	// Materialize builds the runtime value for a package-level decl
	// (function, type, var, const) on first access.
	Materialize func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)
	// CompileExpr compiles a single expression into a chunk — used by
	// OpEvalAST (the migration bridge; expressions see globals, file
	// imports and builtins but not the caller's locals).
	CompileExpr func(pkg *runtime.Package, file *syntax.File, e ast.Expr) (*bytecode.Chunk, error)
	// CompileScopedExpr compiles an expression with a caller-scope overlay
	// (name -> caller slot/upval index) — used by special-form Eval.
	CompileScopedExpr func(pkg *runtime.Package, file *syntax.File, e ast.Expr, locals, upvals map[string]int) (*bytecode.Chunk, error)
	// Special resolves a canonical symbol to its special-form handler.
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
	// Underlying resolves a KindAlias typedef to its underlying typedef.
	Underlying func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// AliasOf resolves a KindAlias typedef to its direct target typedef
	// (one hop — `type A = B` gives B itself, not B's underlying shape).
	AliasOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// FieldTypes returns the declared type of each struct field, parallel
	// to td.Fields (nil entries leave the field zero NIL) — used by
	// zeroValue so `var s T` materializes typed field zeros like Go.
	FieldTypes func(td *runtime.TypeDef) ([]*runtime.TypeDef, error)
	// ResolveType resolves a type expression in the context of typedef td
	// (its package scope, file imports and generic binds): named types
	// materialize lazily through the index, composite types yield
	// anonymous typedefs. Used for promoted-field lookup, constraint
	// resolution and generalized type inference.
	ResolveType func(td *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error)
	// Linkname resolves a bodiless function carrying a two-arg
	// `//go:linkname local importpath.symbol` doc directive to the
	// target's callable value — stdlib internals (net/http's
	// readMIMEHeader -> net/textproto's real body) wire their
	// implementation this way. A nil hook, a one-arg directive, or an
	// unresolvable target reports false and the declaration keeps its
	// zero-return shim.
	Linkname func(vc runtime.VMCaller, fn *runtime.Function) (runtime.Value, bool, error)
}

// VM is a stack machine. Each VM is confined to one goroutine; the `go`
// statement forks a fresh VM sharing the same process so goroutines
// genuinely interleave on host goroutines. Only package state (globals,
// the materialization cache, channels) is shared between them.
//
// A Call from a foreign goroutine — a host-retained callback firing
// off-VM (WaitGroup.Go, time.AfterFunc) — does not run here: Call
// detects it by goroutine id and reroutes to a spawned child VM of the
// same process, keeping the frames stack single-owner.
type VM struct {
	H Hooks

	// frames is the live call stack (innermost last); recover() consults it.
	frames []*frame
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
	proc      *proc
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
}

// proc is one interpreter process: the goroutines belonging to a root Call.
type proc struct {
	done     chan struct{}
	doneOnce sync.Once
	mu       sync.Mutex
	fatal    error // first goroutine failure (panic/trap/builtin error)
	// syncCallers are the helper goroutines callReflectFunc runs a
	// blocking host call on. A Call arriving from one is a synchronous
	// re-entry — the host invoking a script callback inside the call
	// (sync.Once.Do's f) — so its panic propagates back through the join
	// like any nested call's; only a genuinely foreign callback
	// (WaitGroup.Go, time.AfterFunc) fails the process the way a Go
	// goroutine's panic crashes the program.
	syncCallers map[int64]struct{}
}

func newProc() *proc { return &proc{done: make(chan struct{})} }

// kill ends the process: parked channel operations abort with procExit.
func (p *proc) kill() { p.doneOnce.Do(func() { close(p.done) }) }

// fail records the process's first fatal error and ends the process.
func (p *proc) fail(err error) {
	p.mu.Lock()
	if p.fatal == nil {
		p.fatal = err
	}
	p.mu.Unlock()
	p.kill()
}

// watchCallFrom marks gid as a synchronous re-entry source — see
// syncCallers — and returns the func that unmarks it.
func (p *proc) watchCallFrom(gid int64) func() {
	p.mu.Lock()
	if p.syncCallers == nil {
		p.syncCallers = map[int64]struct{}{}
	}
	p.syncCallers[gid] = struct{}{}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.syncCallers, gid)
		p.mu.Unlock()
	}
}

// isSyncCaller reports whether gid is a helper running a blocking host
// call — a Call from it re-enters synchronously (see syncCallers).
func (p *proc) isSyncCaller(gid int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.syncCallers[gid]
	return ok
}

func (p *proc) fatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

// procExit is the unwind raised in a parked goroutine when its process
// ends (root Call returned, or a sibling goroutine's panic failed the
// process). It is deliberately not a *runtime.Panic: defers still run
// but recover() cannot see it, and it is swallowed at the spawn
// boundary — the Go analogue is process exit, where other goroutines
// die without a catchable panic.
type procExit struct{}

func (procExit) Error() string { return "minigo: process exited" }

// ExitRequest unwinds out of os.Exit(code): like a real process exit it
// skips every pending defer, cannot be recovered by the script, and
// stops the process's other goroutines. At the outermost Call it either
// ends cleanly (code 0) or reports `exit status N`.
type ExitRequest struct{ Code int }

func (e *ExitRequest) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// IsProcExit reports whether err is the process-exit sentinel — for host
// code collecting Task errors that wants the real failure, not the noise
// of siblings killed alongside it.
func IsProcExit(err error) bool { _, ok := err.(procExit); return ok }

// EnsureProc opens a process scope on this VM when none is open: the
// `go` spawns that occur before the Call boundary joins the scope so
// they die with it. ReleaseProc ends a scope opened by EnsureProc.
func (v *VM) EnsureProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	if v.proc == nil {
		v.proc = newProc()
	}
	v.procHolds++
}

// ReleaseProc ends a scope opened by EnsureProc: the process is killed
// (parked goroutines abort) when the last hold releases.
func (v *VM) ReleaseProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	v.procHolds--
	if v.procHolds <= 0 {
		if v.proc != nil {
			v.proc.kill()
			v.proc = nil
			v.procDead = true
		}
		v.procHolds = 0
	}
}

// doneRV is the process-done channel as a reflect select operand — nil
// (never ready) when this VM has no process.
func (v *VM) doneRV() reflect.Value {
	if v.proc == nil {
		return nilChanValue
	}
	return reflect.ValueOf(v.proc.done)
}

var nilChanValue = reflect.ValueOf((chan struct{})(nil))

// Spawn implements VMCaller.Spawn — the `go` statement's machinery.
func (v *VM) Spawn(fn runtime.Value, args []runtime.Value) *runtime.Task {
	return v.spawn(fn, args, nil, nil, true)
}

// spawn is Spawn carrying the call site's spread element typedef into
// generic inference — `go Sum(n...)` binds the same T=int the OpCall
// path does. The public Spawn signature stays (fn, args). failProc is
// Go's crash rule: a panic in a genuinely concurrent execution fails
// the process, while a synchronous re-entry (a callback the host
// invokes inside a blocking call — sync.Once.Do's f) must instead
// propagate its panic back through the join so the caller's recover
// can run.
func (v *VM) spawn(fn runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef, failProc bool) *runtime.Task {
	v.callMu.Lock()
	if v.proc == nil {
		if v.procDead {
			// the caller's process already ended — Go kills pending
			// timers with the process, so the spawn never starts.
			v.callMu.Unlock()
			t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
			t.Finish(procExit{}, true)
			return t
		}
		// a spawn outside any Call (host-driven VM) gets a detached
		// process: nothing kills it, matching a goroutine that outlives
		// the program it was expected to die with — the hold is
		// deliberately never released.
		v.proc = newProc()
		v.procHolds++
	}
	p := v.proc
	select {
	case <-p.done:
		// the process died between hold and spawn — same refusal: a
		// dead process starts no new work.
		v.callMu.Unlock()
		t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
		t.Finish(procExit{}, true)
		return t
	default:
	}
	v.callMu.Unlock()
	t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
	child := &VM{H: v.H, proc: p, task: t, syncCall: !failProc}
	go func() {
		r, err := child.callBounded(fn, args, statics, spreadTd)
		t.Result = r
		t.Finish(err, IsProcExit(err))
		if err != nil && !IsProcExit(err) && failProc {
			p.fail(err)
		}
	}()
	return t
}

// goroutineID reports the calling goroutine's id, parsed out of
// runtime.Stack — the stdlib exposes no accessor and Call needs one to
// tell a same-goroutine re-entry from a foreign one (a host-retained
// callback firing on another goroutine). 0 means unparseable, which
// Call treats as foreign when the VM is busy — the safe direction.
func goroutineID() int64 {
	var buf [48]byte
	n := goruntime.Stack(buf[:], false)
	s := buf[:n]
	// "goroutine 123 [running]:" — the id sits between the first two
	// spaces of the header line.
	i := bytes.IndexByte(s, ' ')
	if i < 0 {
		return 0
	}
	j := bytes.IndexByte(s[i+1:], ' ')
	if j < 0 {
		return 0
	}
	id, err := strconv.ParseInt(string(s[i+1:i+1+j]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// Task implements VMCaller.Task — nil on the root goroutine.
func (v *VM) Task() *runtime.Task { return v.task }

// maxFrames bounds the call stack; exceeding it traps instead of letting
// unbounded script recursion blow the host goroutine stack (a fatal,
// untraceable crash in Go) — VM frames do consume host stack.
const maxFrames = 10000

type deferredCall struct {
	fn   runtime.Value
	args []runtime.Value
	pos  token.Pos
	// statics holds the call site's per-argument declared typedefs —
	// inference binds T to the argument's static type at deferred call
	// time the same way an immediate OpCall does.
	statics []*runtime.TypeDef
	// spreadTd is the element typedef a trailing `xs...` argument carried
	// into the call — type inference still sees `Sum(n...)`'s []int when
	// the nil slice expanded to zero arguments.
	spreadTd *runtime.TypeDef
}

type frame struct {
	fn     *runtime.Function
	ch     *bytecode.Chunk
	locals []*runtime.Cell
	upvals []*runtime.Cell
	stack  []runtime.Value
	ip     int

	defers   []deferredCall // LIFO
	sentinel bool           // placeholder frame for a deferred builtin — a
	// wrapper in Go's terms: it does not count toward recover()'s
	// one-frame distance rule
	retNamed bool // gather named result slots after defers run
	results  []runtime.Value

	// boundLo/boundHi bound a re-entrant loop run (range-over-func yield):
	// the bounded run executes only while boundLo < ip < boundHi and halts
	// on either edge — the loop's back-edge address is boundLo, its exit
	// boundHi. boundHi == 0 disables bounding (normal execution).
	boundLo int
	boundHi int
}

func (f *frame) push(v runtime.Value) { f.stack = append(f.stack, v) }

func (f *frame) pop() runtime.Value {
	v := f.stack[len(f.stack)-1]
	f.stack = f.stack[:len(f.stack)-1]
	return v
}

func (f *frame) pos() token.Pos {
	if f.ip > 0 {
		return f.ch.Code[f.ip-1].Pos
	}
	return 0
}

func (f *frame) trap(format string, args ...any) {
	panic(&runtime.Trap{Pos: f.pos(), Reason: fmt.Sprintf(format, args...)})
}

// assignCell stores into a cell, coercing to the cell's declared type
// first when one was stamped by a `var x T` / typed-param / named-result
// coerce. `x = v` then enforces the same assignability as `var x T = v`,
// and a named basic type keeps its tag across plain assignment.
func (v *VM) assignCell(f *frame, c *runtime.Cell, val runtime.Value) {
	if c.ReadOnly {
		f.trap("cannot assign to constant")
	}
	if c.Typ != nil {
		val = v.coerce(f, val, c.Typ)
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
	c.Elem = valueCopy(val)
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
		}
	}
	if !runtime.SetRef(ref, val) {
		f.trap("indirect store to non-pointer %T", ref)
	}
}

// Call invokes a function-like value: Function, Closure, BoundMethod,
// BuiltinFunc, TypeDef (conversion), or Cell wrapping any of those.
// It is the engine boundary: script panics and traps unwind as Go panics
// and are converted to errors here. The outermost Call on a VM is its
// process's root: the proc is created lazily and killed on return.
func (v *VM) Call(callee runtime.Value, args []runtime.Value) (result runtime.Value, err error) {
	return v.callBounded(callee, args, nil, nil)
}

// callBounded is Call carrying the call site's spread element typedef
// into generic inference; nil spreadTd is an ordinary call.
func (v *VM) callBounded(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (result runtime.Value, err error) {
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
		failProc := true
		if p := v.proc; p != nil && p.isSyncCaller(gid) {
			failProc = false
		}
		t := v.spawn(callee, args, statics, spreadTd, failProc)
		werr := t.Wait()
		return t.Result, werr
	}
	v.callDepth++

	v.callGid = gid
	v.callMu.Unlock()
	defer func() {
		v.callMu.Lock()
		v.callDepth--
		if v.callDepth == 0 {
			v.callGid = 0
		}
		v.callMu.Unlock()
	}()
	p := v.proc
	if p == nil {
		v.EnsureProc()
		defer v.ReleaseProc()
		p = v.proc
	}
	defer func() {
		if r := recover(); r != nil {
			if ex, isExit := r.(*ExitRequest); isExit {
				v.proc.kill()
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
		if ferr := p.fatalErr(); ferr != nil && (err == nil || IsProcExit(err)) {
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
	if p := v.proc; p != nil {
		select {
		case <-p.done:
			// the process ended while this call was queued — a sibling
			// panic already won; die like Go's exit() rather than
			// running the call's observable effects.
			panic(procExit{})
		default:
		}
	}
	for {
		switch c := callee.(type) {
		case *runtime.BuiltinFunc:
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
			return c.Fn(v, args)
		case *runtime.TypeDef:
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
				return callReflectFunc(fmt.Sprintf("%v", fv.Type()), fv, v, args)
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
	fr, err := v.prepFrame(callee, args, statics, spreadTd)
	if err != nil {
		return nil, err
	}
	v.exec(fr)
	if len(fr.stack) == 0 {
		return runtime.NIL, nil
	}
	return fr.stack[len(fr.stack)-1], nil
}

// Member implements the VMCaller.Member hook: selectMember over a dummy
// frame, reporting failure instead of trapping so host intrinsics can
// probe for optional members (e.g. a script-defined Unwrap).
func (v *VM) Member(base runtime.Value, name string) (m runtime.Value, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			m, ok = nil, false
		}
	}()
	fr := &frame{fn: &runtime.Function{Name: "<member>"}}
	return v.selectMember(fr, base, name), true
}

// MethodSetOf implements VMCaller.MethodSetOf: the names a dynamic value
// offers interface checks. A nil set reports "no engine hook" — callers
// then fall back to Member's existence check.
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

// Package implements VMCaller.Package: the package of the innermost
// running frame, i.e. the builtin's caller.
func (v *VM) Package() *runtime.Package {
	if n := len(v.frames); n > 0 {
		if fn := v.frames[n-1].fn; fn != nil {
			return fn.Pkg
		}
	}
	return nil
}

// TypeOf implements the VMCaller.TypeOf hook: the typedef describing a
// runtime value, for new(expr)'s allocated cell type.
func (v *VM) TypeOf(x runtime.Value) *runtime.TypeDef {
	return v.typeOfValue(x)
}

// Copy implements the VMCaller.Copy hook: Go assignment semantics —
// structs copy by value, slices/maps/pointers share.
func (v *VM) Copy(x runtime.Value) runtime.Value {
	return valueCopy(x)
}

// Recover implements the recover() builtin for VMCaller: it returns the
func (v *VM) Recover() runtime.Value {
	if v.inflight == nil {
		return runtime.NIL
	}
	// Go's rule (gorecover): exactly one non-wrapper frame may sit
	// between the recover() call and the panic's unwind — recover()
	// works when the deferred function calls it directly. `defer
	// recover()` puts zero frames between (the panic's own unwind runs
	// the builtin), and a recover() inside a nested callee puts two or
	// more — both return nil. Sentinel frames are the wrappers and do
	// not count.
	dist := 0
	for i := len(v.frames) - 1; i >= v.unwindDepth && i >= 0; i-- {
		if !v.frames[i].sentinel {
			dist++
		}
	}
	if dist != 1 {
		return runtime.NIL
	}
	{
		val := v.inflight.Value
		v.consumedPanic = v.inflight
		v.inflight = nil
		// a runtime-error payload surfaces as the boxed host error Go's
		// recover() returns — `err.(error)` asserts and `.Error()` calls
		// resolve through the reflection path. The same goes for the
		// plainError family and panic(nil)'s PanicNilError.
		if e, ok := val.(error); ok {
			return &runtime.GoValue{V: e}
		}
		return val
	}
	return runtime.NIL
}

func asError(r any) error {
	switch e := r.(type) {
	case *runtime.Trap:
		return e
	case *runtime.Panic:
		return e
	case error:
		return e
	default:
		return fmt.Errorf("panic: %v", r)
	}
}

// prepFrame builds the frame for callee.
func (v *VM) prepFrame(callee runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (*frame, error) {
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
		args = append([]runtime.Value{c.Recv}, args...)
	default:
		return nil, fmt.Errorf("value of type %T is not callable", callee)
	}
	// a bodiless declaration may carry a //go:linkname directive whose
	// target argument wires it to a real implementation in another
	// package — substitute the resolved target and re-dispatch.
	if fn != nil && fn.Decl != nil && fn.Decl.Body == nil && v.H.Linkname != nil {
		tv, ok, err := v.H.Linkname(v, fn)
		if err != nil {
			return nil, err
		}
		if ok {
			return v.prepFrame(tv, args, statics, spreadTd)
		}
	}
	// generic function called without instantiation (Id(40)): infer the
	// unbound type arguments from the runtime argument types. A method of
	// an instantiated generic type arrives partly bound — the receiver's
	// own type binds stay, only the method's own type params infer.
	if fn != nil && len(fn.TParams) > 0 && hasUnbound(fn.TParams, fn.Binds) {
		inferred, err := v.inferBinds(fn, args, statics, spreadTd)
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
	fr := &frame{fn: fn, ch: ch, upvals: upvals}
	fr.locals = make([]*runtime.Cell, ch.NLocals)
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
				rest = &runtime.Slice{Elems: append([]runtime.Value{}, args[i:]...)}
			}
			fr.locals[i] = &runtime.Cell{Elem: rest}
			break
		}
		fr.locals[i] = &runtime.Cell{Elem: valueCopy(a)}
	}
	for i := n; i < ch.NLocals; i++ {
		fr.locals[i] = &runtime.Cell{Elem: runtime.NIL}
	}
	return fr, nil
}

// exec runs a frame to completion: the instruction loop first, then the
// frame's defers during unwind — mirroring Go, where deferred calls run on
// normal return and during panic unwinding alike. A script *Panic is
// catchable by recover() inside a deferred function; a *Trap bypasses
// recover and re-panics after defers drain. A raw host panic (a Go runtime
// error or a panic inside a builtin) is lifted into a script *Panic so it
// records frames and can be recovered, like Go's own runtime errors.
func (v *VM) exec(f *frame) {
	v.frames = append(v.frames, f)
	defer func() {
		r := recover()
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

// asScriptPanic wraps a raw host panic in *runtime.Panic so unwinding can
// attribute it to script frames and deferred recover() can catch it —
// matching Go, where runtime errors (index out of range, nil deref, divide
// by zero) are recoverable panics. The original value is kept boxed so
// recover() hands scripts the real error, not its rendered text, and the
// host goroutine stack is captured while the panicking frames are still
// live, so a panic inside a builtin/host handler shows where it died.
// Trap and Panic pass through unchanged.
func asScriptPanic(r any) any {
	switch r.(type) {
	// procExit and ExitRequest pass through like Trap: a process-exit
	// unwind must not become a recoverable script panic.
	case nil, *runtime.Trap, *runtime.Panic, procExit, *ExitRequest:
		return r
	default:
		return &runtime.Panic{Value: &runtime.GoValue{V: r}, GoStack: string(debug.Stack())}
	}
}

// unwoundFrame is a frame an in-flight panic unwound, tagged with that
// panic so unwind can drop the entries of a panic that dies (recovered
// or superseded) without disturbing another live unwind's frames.
type unwoundFrame struct {
	f  *frame
	pn *runtime.Panic
}

// dropUnwound removes every unwound frame belonging to p — a nil p
// drops nothing.
func (v *VM) dropUnwound(p *runtime.Panic) {
	if p == nil {
		return
	}
	kept := v.unwinding[:0]
	for _, e := range v.unwinding {
		if e.pn != p {
			kept = append(kept, e)
		}
	}
	v.unwinding = kept
}

// unwind runs the frame's defers and resolves the outcome of r, the value
// caught during frame teardown (nil on normal return).
func (v *VM) unwind(f *frame, r any) {
	if r != nil {
		v.trace(f, r)
	}
	var p *runtime.Panic
	if sp, ok := r.(*runtime.Panic); ok {
		p = sp
	}
	// v.inflight is visible to recover() only while this frame's defers run.
	// os.Exit skips the defers on every frame like a real process exit.
	// A frame completing normally (r == nil) owns neither: it stays on the
	// frame stack while its defers run, a real frame between their
	// recover() calls and the panic still unwinding below — the way a
	// `defer recover()` inside a deferred call catches the panic that
	// invoked it.
	saved := v.inflight
	savedD := v.unwindDepth
	// dpos is the frame's logical position while its defers drain — the
	// boundary a panic raised inside one of them unwinds to.
	dpos := len(v.frames)
	if r != nil {
		v.inflight = p
		v.unwindDepth = dpos
	} else {
		v.frames = append(v.frames, f)
		defer v.framesPop()
	}
	if _, isExit := r.(*ExitRequest); !isExit {
		// A panic consumed partway through the drain hands the rest of
		// the list to the outer context — Go's recovery lands on the
		// frame's deferreturn, where `defer recover()` sees the panic
		// that was unwinding below (recover1.go test6).
		for len(f.defers) > 0 {
			if p != nil && v.inflight == nil {
				v.inflight = saved
				v.unwindDepth = savedD
				// the consumed panic's unwound frames die with it — Go
				// lists only live frames once an unwind is recovered.
				// Drop the frame's own panic too: it is dead whether it
				// was consumed itself or superseded mid-drain.
				v.dropUnwound(p)
				v.dropUnwound(v.consumedPanic)
				v.consumedPanic = nil
				v.frames = append(v.frames, f)
				defer v.framesPop()
				p = nil
			}
			v.runOneDefer(f)
		}
	}
	if r != nil {
		if p != nil || (v.inflight != saved && v.inflight != nil) {
			// the drain left a panic to propagate — either the original
			// still unwinding or one a deferred call raised. The latter
			// must be picked up even after a mid-drain recovery set p
			// to nil: a panic raised in a later deferred call would
			// otherwise be dropped (and leak into inflight for an
			// unrelated recover() to find).
			p = v.inflight
			v.inflight = saved
			v.unwindDepth = savedD
		}
		// consumed mid-drain with no new panic: the transition already
		// restored the outer panic state.
	} else if v.inflight != saved && v.inflight != nil {
		// a deferred call panicked during a normal drain — propagate it
		// as this frame's panic. A consumed outer panic leaves inflight
		// nil and dies with it. Restore the outer panic state like the
		// r!=nil path: runOneDefer installs the deferred panic on
		// inflight, and propagating it upward while inflight still holds
		// it makes every enclosing unwind read it as the *outer* panic —
		// a try-style recover then restores the consumed panic into
		// inflight, leaking it into an unrelated later recover().
		p = v.inflight
		v.inflight = saved
		v.unwindDepth = savedD
	}
	if r != nil && p == nil {
		// the panic died here (recovered, or it was a Trap swallowed
		// at a boundary) — its unwound frames die with it; entries of
		// a still-live outer unwind stay.
		if sp, ok := r.(*runtime.Panic); ok {
			v.dropUnwound(sp)
		}
		v.dropUnwound(v.consumedPanic)
		v.consumedPanic = nil
	}
	switch {
	case p != nil:
		v.failProc(p)
		panic(p) // still panicking, or a deferred call panicked
	case r != nil:
		if _, ok := r.(*runtime.Panic); !ok {
			v.failProc(r)
			panic(r) // Trap/host panic: recover() must not swallow it
		}
		fallthrough // script panic recovered by a deferred function
	default:
		f.stack = []runtime.Value{f.finalResult()}
	}
}

// failProc ends the process when an unrecovered panic or trap reaches a
// goroutine's root frame — the instant Go exits on. Killing here, inside
// the unwind that just decided the panic is fatal, beats waiting for the
// spawn boundary's error path: a sibling a dying defer unblocked (wg.Done
// mid-unwind) is already dead when it resumes instead of printing past
// the crash. procExit and ExitRequest are excluded — the process is
// already ending on its own terms, and recording procExit as the fatal
// would mask the real failure on the root call.
func (v *VM) failProc(r any) {
	if len(v.frames) != 0 || v.proc == nil || v.syncCall {
		return
	}
	switch r.(type) {
	case *runtime.Panic, *runtime.Trap:
		v.proc.fail(asError(r))
	}
}

// finalResult computes the frame's return value after its defers ran, so a
// deferred function can still mutate named results (Go semantics). Named
// slots are gathered whenever the function declares named results — even on
// panic unwind, where no OpReturn ever ran (a recover()ing defer can set
// them). Unnamed results come from the values OpReturn collected.
func (f *frame) finalResult() runtime.Value {
	results := f.results
	if len(f.ch.NamedSlots) > 0 {
		results = make([]runtime.Value, len(f.ch.NamedSlots))
		for i, s := range f.ch.NamedSlots {
			results[i] = f.locals[s].Elem
		}
	}
	switch len(results) {
	case 0:
		return runtime.NIL
	case 1:
		return results[0]
	default:
		return &runtime.Tuple{Elems: results}
	}
}

// trace appends a traceback entry (PR #3's `File "...", line N, in f`
// plus the source line under it) to an unwinding Trap/Panic.
func (v *VM) trace(f *frame, r any) {
	pos := f.pos()
	if !pos.IsValid() && f.fn.Decl != nil {
		// the frame failed before its first instruction (e.g. argument
		// coercion) — point at the function's declaration instead
		pos = f.fn.Decl.Pos()
	}
	entry := v.frameLine(f, pos, v.frameName(f))
	switch e := r.(type) {
	case *runtime.Trap:
		e.Frames = append(e.Frames, entry)
	case *runtime.Panic:
		e.Frames = append(e.Frames, entry)
	}
}

// frameName renders f's function for a traceback entry — `Id[int]` for an
// instantiated generic, `T.M` for methods.
func (v *VM) frameName(f *frame) string {
	name := f.fn.Name
	if len(f.fn.TParams) > 0 {
		args := make([]string, len(f.fn.TParams))
		for i, tp := range f.fn.TParams {
			args[i] = "?"
			if td, ok := f.fn.Binds[tp].(*runtime.TypeDef); ok {
				args[i] = td.Name
			}
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name + "()"
}

// frameLine formats one traceback entry — `File "<file>", line <n>, in
// <name>` followed by the source text under it when available.
func (v *VM) frameLine(f *frame, pos token.Pos, name string) string {
	if !pos.IsValid() || f.fn.Pkg == nil || f.fn.Pkg.Fset == nil {
		return name
	}
	p := f.fn.Pkg.Fset.Position(pos)
	entry := fmt.Sprintf("File %q, line %d, in %s", p.Filename, p.Line, name)
	if l := v.sourceLine(f.fn.Pkg, p.Filename, p.Line); l != "" {
		entry += "\n\t\t" + l
	}
	return entry
}

// sourceLine returns the trimmed text of filename:line for traceback
// snippets. In-memory sources (REPL, generated files) come from
// syntax.File.Src; others are read from disk, but only when filename is a
// file the package actually loaded — never an arbitrary path — so an
// error message cannot leak text outside what the script already ran.
// Failures are silent — a traceback must never itself fail.
func (v *VM) sourceLine(pkg *runtime.Package, filename string, line int) string {
	if pkg == nil || pkg.FileByName == nil {
		return ""
	}
	sf := pkg.FileByName[filename]
	if sf == nil {
		return ""
	}
	if sf.Src != nil {
		return nthLine(sf.Src, line)
	}
	if v.srcCache == nil {
		v.srcCache = map[string][]string{}
	}
	lines, ok := v.srcCache[filename]
	if !ok {
		data, err := os.ReadFile(filename)
		if err != nil {
			v.srcCache[filename] = nil
			return ""
		}
		lines = strings.Split(string(data), "\n")
		v.srcCache[filename] = lines
	}
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// nthLine returns src's line-th line, trimmed; "" when out of range.
func nthLine(src []byte, line int) string {
	if line < 1 {
		return ""
	}
	for i, l := range strings.Split(string(src), "\n") {
		if i == line-1 {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// runOneDefer invokes the innermost pending deferred call of f.
func (v *VM) runOneDefer(f *frame) {
	d := f.defers[len(f.defers)-1]
	f.defers = f.defers[:len(f.defers)-1]
	// depth is the slot this deferred call's frame occupies — the boundary
	// a panic it raises unwinds to. While the owner drains after a normal
	// return the owner is still on the stack (depth > dpos); while its
	// unwind drains, depth == dpos. A recover() inside a later deferred
	// call counts frames above depth — the owner below does not count.
	depth := len(v.frames)
	defer func() {
		r := asScriptPanic(recover())
		if r == nil {
			return
		}
		// a panic raised by the deferred call has no link back to
		// the frame that registered it — record the defer site as a
		// synthetic entry so the traceback shows who deferred it.
		entry := v.frameLine(f, d.pos, v.frameName(f)+" (deferred call)")
		switch e := r.(type) {
		case *runtime.Panic:
			e.Frames = append(e.Frames, entry)
			v.inflight = e
			v.unwindDepth = depth
		case *runtime.Trap:
			e.Frames = append(e.Frames, entry)
			panic(r)
		case procExit:
			// process teardown: a deferred call that parks again
			// exits immediately — keep draining the remaining
			// defers (Goexit drains them too)
		default:
			panic(r)
		}
	}()
	v.invokeDeferred(d)
}

// invokeDeferred runs one deferred call. Callees without a bytecode frame
// (BuiltinFunc, TypeDef conversion, Cell-wrapped values) still run at
// teardown; a sentinel frame is pushed for them so recover() counts the
// distance correctly: the sentinel marks the deferred call's own wrapper
// slot without counting as a real frame — `defer recover()` sees zero
// frames and returns nil, while `defer recover()` inside a deferred
// function sees exactly one and recovers, matching Go.
func (v *VM) invokeDeferred(d deferredCall) {
	callee := d.fn
	for {
		switch c := callee.(type) {
		case *runtime.BuiltinFunc:
			v.pushDeferredSentinel(c.Name)
			defer v.framesPop()
			if _, err := c.Fn(v, d.args); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		case *runtime.TypeDef:
			v.pushDeferredSentinel(c.Name)
			defer v.framesPop()
			if _, err := v.convert(c, firstArg(d.args)); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		case *runtime.Named:
			callee = runtime.Unwrap(callee)
			continue
		case *runtime.GoValue:
			// a deferred host func — context.WithCancel's CancelFunc
			// boxes run the same way a direct Call does, through
			// reflect, on a sentinel frame so recover() distances hold.
			fv := reflect.ValueOf(c.V)
			if !fv.IsValid() || fv.Kind() != reflect.Func {
				break
			}
			v.pushDeferredSentinel(fv.Type().String())
			defer v.framesPop()
			if _, err := callReflectFunc(fv.Type().String(), fv, v, d.args); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		default:
			if dv, ok := runtime.Deref(callee); ok {
				callee = dv
				continue
			}
		}
		break
	}
	// calling a nil function value panics like a nil deref in Go — at
	// the deferred call's invocation, not when `defer` registered it.
	if callee == nil || callee == runtime.NIL {
		panic(runtime.NilDerefPanic())
	}
	if _, ok := asTypedNil(callee); ok {
		panic(runtime.NilDerefPanic())
	}
	fr, err := v.prepFrame(callee, d.args, d.statics, d.spreadTd)
	if err != nil {
		panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
	}
	v.exec(fr)
}

// pushDeferredSentinel records a deferred host call on the frame stack so
// Recover() measures the right distance to the panic's unwind. It is not
// a real frame: no chunk, no locals — it occupies the deferred call's
// slot without counting as a frame between.
func (v *VM) pushDeferredSentinel(name string) {
	v.frames = append(v.frames, &frame{fn: &runtime.Function{Name: name}, sentinel: true})
}

func (v *VM) framesPop() { v.frames = v.frames[:len(v.frames)-1] }

func firstArg(args []runtime.Value) runtime.Value {
	if len(args) == 0 {
		return runtime.NIL
	}
	return args[0]
}

func (v *VM) loop(f *frame) {
	code := f.ch.Code
	consts := f.ch.Consts
	for f.ip < len(code) && (f.boundHi == 0 || (f.ip > f.boundLo && f.ip < f.boundHi)) {
		ins := code[f.ip]
		f.ip++
		switch ins.Op {
		case bytecode.OpNop:
		case bytecode.OpConst:
			f.push(consts[ins.A])
		case bytecode.OpNil:
			f.push(runtime.NIL)
		case bytecode.OpDup:
			f.push(f.stack[len(f.stack)-1])
		case bytecode.OpDup2:
			n := len(f.stack)
			a, b := f.stack[n-2], f.stack[n-1]
			f.push(a)
			f.push(b)
		case bytecode.OpFieldRef:
			base := f.pop()
			if ins.B == 0 {
				// Address-of target pins what the operand evaluated to: a
				// cell holding a struct is the variable's storage (its
				// address is stable), but a pointer variable's operand
				// evaluated to its pointee — snapshot it, so `fp := &p.f`
				// doesn't follow a reseated p.
				if c, ok := base.(*runtime.Cell); ok {
					e := c.Elem
					for {
						if n, isNamed := e.(*runtime.Named); isNamed {
							e = n.V
							continue
						}
						break
					}
					if _, isStruct := e.(*runtime.Struct); !isStruct {
						base = c.Elem
					}
				}
				// the nil check fires now; B=1 marks a store target, where
				// Go checks at store time so the RHS evaluates first.
				v.checkAddrBase(base, nil)
			}
			f.push(&runtime.FieldRef{Base: base, Name: consts[ins.A].(string)})
		case bytecode.OpIndexRef:
			key := f.pop()
			base := f.pop()
			if ins.B == 0 {
				v.checkAddrBase(base, key)
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
			n := len(f.stack)
			f.stack[n-1], f.stack[n-2] = f.stack[n-2], f.stack[n-1]
		case bytecode.OpRot3:
			n := len(f.stack)
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
				x = materialize(f, x)
			}
			f.locals[ins.A] = &runtime.Cell{Elem: valueCopy(x), ReadOnly: ins.B != 0}
		case bytecode.OpRenewVar:
			f.locals[ins.A] = &runtime.Cell{Elem: f.locals[ins.A].Elem}
		case bytecode.OpLocal:
			f.push(f.locals[ins.A].Elem)
		case bytecode.OpLocalTyp:
			if td := f.locals[ins.A].Typ; td != nil {
				f.push(td)
			} else {
				f.push(runtime.NIL)
			}
		case bytecode.OpSetLocal:
			v.assignCell(f, f.locals[ins.A], f.pop())
		case bytecode.OpLocalRef:
			f.push(f.locals[ins.A])
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
				x = materialize(f, x)
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
			f.push(v.selectMember(f, base, consts[ins.A].(string)))
		case bytecode.OpSetField:
			val := f.pop()
			base := f.pop()
			v.setField(f, base, consts[ins.A].(string), val)
		case bytecode.OpIndex:
			idx := f.pop()
			base := f.pop()
			f.push(v.index(f, base, idx))
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
			} else if td, ok := x.(*runtime.TypeDef); ok {
				// `(*T)` in a method-expression position evaluates to the
				// pointer typedef so selectMember can bind pointer methods.
				f.push(&runtime.TypeDef{Kind: runtime.KindPointer, Anon: &ast.StarExpr{X: typeExprFor(td)}, Pkg: td.Pkg, File: td.File})
			} else if dv, ok := runtime.Deref(x); ok {
				// a pointer value typed *Declared dereferences to the
				// declared pointee type — `*s` on `(*T)(p)` reads as T,
				// so method calls and `:=`-inferred vars keep the tag.
				if n, isN := x.(*runtime.Named); isN && n.Typ != nil && n.Typ.Kind == runtime.KindPointer {
					if et := v.elemTypedef(f, n.Typ); et != nil {
						dv = v.coerce(f, dv, et)
					}
				}
				f.push(dv)
			} else if tn, ok := asTypedNil(x); ok {
				// *p on a nil pointer panics; on other nilables it's invalid
				if tn.Typ.Kind == runtime.KindPointer {
					panic(runtime.NilDerefPanic())
				}
				f.trap("deref of non-pointer %T", x)
			} else {
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
			for i := ntargs - 1; i >= 0; i-- {
				targs[i] = f.pop()
			}
			base := f.pop()
			f.push(v.instantiate(f, base, targs, ins.Pos))
		case bytecode.OpFoldArrayLen:
			// [td, len] on the stack: evaluate-at-use constants like
			// `[n]int`/`[len(a)]*T` fold into the typedef's AST so type
			// identity spells the concrete `[3]*T` like Go. The op
			// folds the next un-folded len node — runtime.ArrayLenNodes
			// is the same DFS walk the compiler emitted the evals in,
			// and folded nodes drop out so nodes[0] is always next.
			lv := f.pop()
			td := f.pop().(*runtime.TypeDef)
			if n, ok := lenConstInt(lv); ok {
				if nodes := runtime.ArrayLenNodes(td.Anon); len(nodes) > 0 {
					nodes[0].Len = &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)}
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
				f.trap("%s", err)
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
			c := f.locals[ins.A]
			c.Elem = v.coerce(f, c.Elem, td)
			c.Typ = td // future stores into this cell coerce the same way
		case bytecode.OpCoerceTop:
			td := typedefOf(f.pop())
			if td == nil {
				f.trap("declared type is not a type")
			}
			f.stack[len(f.stack)-1] = v.coerce(f, f.stack[len(f.stack)-1], td)
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
			sym := consts[ins.A].(runtime.SymbolID)
			q := consts[ins.B].(*runtime.QuotedCall)
			if v.H.Special == nil {
				f.trap("OpSpecialCall: no special-form registry")
			}
			h, ok := v.H.Special(sym)
			if !ok {
				f.trap("unregistered special form %s.%s", sym.PackagePath, sym.Name)
			}
			res, err := h(&specialCtx{v: v, f: f, q: q}, q)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			f.push(res)
		case bytecode.OpBox:
			x := f.pop()
			// the box cell takes the pointee's declared tag so `*p = v`
			// coerces like a store into `var p T`.
			f.push(&runtime.Cell{Elem: x, Typ: declaredTag(x)})
		case bytecode.OpCall:
			args, statics, spreadTd := v.popArgs(f, int(ins.A), int(ins.B), ins.Pos)
			fn := f.pop()
			r, err := v.call(fn, args, statics, spreadTd)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			f.push(r)
		case bytecode.OpDefer:
			// callee + args are evaluated now (Go semantics); the call itself
			// runs at frame teardown, LIFO.
			args, statics, spreadTd := v.popArgs(f, int(ins.A), int(ins.B), ins.Pos)
			fn := f.pop()
			f.defers = append(f.defers, deferredCall{fn: fn, args: args, pos: ins.Pos, statics: statics, spreadTd: spreadTd})
		case bytecode.OpGo:
			// `go f(x)` spawns a real goroutine in this process: callee and
			// args are evaluated now; the call runs concurrently.
			args, statics, spreadTd := v.popArgs(f, int(ins.A), int(ins.B), ins.Pos)
			fn := f.pop()
			v.spawn(fn, args, statics, spreadTd, true)
		case bytecode.OpEvalAST:
			frag := consts[ins.A].(*bytecode.ASTFragment)
			if v.H.CompileExpr == nil {
				f.trap("OpEvalAST: no CompileExpr hook")
			}
			ch, err := v.H.CompileExpr(f.fn.Pkg, frag.File, frag.Expr)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			r, err := v.call(&runtime.Function{Pkg: f.fn.Pkg, File: frag.File, Name: "<eval>", Chunk: ch}, nil, nil, nil)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			f.push(r)
		case bytecode.OpPack:
			n := int(ins.A)
			el := make([]runtime.Value, n)
			for i := n - 1; i >= 0; i-- {
				el[i] = f.pop()
			}
			f.push(&runtime.Tuple{Elems: el})
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
			if len(proto.Chunk.Upvals) == 0 {
				// a capture-free literal evaluates to the proto itself —
				// Go hoists it to a static func value, so repeated evals
				// share one object instead of allocating a Closure.
				f.push(proto)
				continue
			}
			cl := &runtime.Closure{Fn: proto}
			for _, d := range proto.Chunk.Upvals {
				if d.FromParentUpval {
					cl.Upvals = append(cl.Upvals, f.upvals[d.Index])
				} else {
					cl.Upvals = append(cl.Upvals, f.locals[d.Index])
				}
			}
			f.push(cl)
		case bytecode.OpBinary:
			b := f.pop()
			a := f.pop()
			f.push(binaryOp(f, bytecode.BinOp(ins.A), a, b))
		case bytecode.OpUnary:
			a := f.pop()
			f.push(unaryOp(f, bytecode.UnOp(ins.A), a))
		case bytecode.OpJump:
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
			base := f.stack[len(f.stack)-1]
			if n, ok := v.lenIdxFold(f, base); ok {
				f.stack = f.stack[:len(f.stack)-2]
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
			top := f.stack[len(f.stack)-1]
			if n, ok := v.lenDerefFold(f, top); ok {
				f.stack = f.stack[:len(f.stack)-2]
				f.push(n)
				f.ip = int(ins.A)
			} else if ins.B == 0 {
				f.stack = f.stack[:len(f.stack)-1]
			}
		case bytecode.OpIter:
			f.push(v.newIterator(f, materialize(f, f.pop())))
		case bytecode.OpRangeNext:
			it := f.locals[ins.B].Elem.(*runtime.Iterator)
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
				f.ip = int(ins.A)
			}
		case bytecode.OpSend:
			val := f.pop()
			chv := f.pop()
			chRV, et := v.chanOf(f, chv)
			if et != nil {
				val = v.coerce(f, val, et)
			} else {
				val = materialize(f, val)
			}
			sv, err := v.chanSendValue(val, chRV.Type().Elem())
			if err != nil {
				f.trap("cannot send on %s: %s", chRV.Type(), err)
			}
			v.chanSend(chRV, sv)
		case bytecode.OpRecv:
			val, _ := v.chanRecv(f, f.pop())
			f.push(val)
		case bytecode.OpRecvOK:
			val, ok := v.chanRecv(f, f.pop())
			f.push(&runtime.Tuple{Elems: []runtime.Value{val, ok}})
		case bytecode.OpSelArm:
			if ins.B == 1 {
				val := f.pop()
				chv := f.pop()
				chRV, et := v.chanOf(f, chv)
				if et != nil {
					val = v.coerce(f, val, et)
				} else {
					val = materialize(f, val)
				}
				sv, err := v.chanSendValue(val, chRV.Type().Elem())
				if err != nil {
					f.trap("cannot send on %s: %s", chRV.Type(), err)
				}
				f.push(&runtime.SelArm{Send: true, Case: reflect.SelectCase{Dir: reflect.SelectSend, Chan: chRV, Send: sv}})
			} else {
				chRV, et := v.chanOf(f, f.pop())
				f.push(&runtime.SelArm{NRecv: int(ins.A), ETyp: et, Case: reflect.SelectCase{Dir: reflect.SelectRecv, Chan: chRV}})
			}
		case bytecode.OpSelWait:
			// arms were pushed in source order; the jump table follows this
			// instruction: one OpJump per case, then the default's OpJump
			n := int(ins.A)
			arms := make([]*runtime.SelArm, n)
			for i := n - 1; i >= 0; i-- {
				a, ok := f.pop().(*runtime.SelArm)
				if !ok {
					f.trap("select arm is %T", f.stack[len(f.stack)-1])
				}
				arms[i] = a
			}
			table := f.ip
			cases := make([]reflect.SelectCase, 0, n+2)
			for _, a := range arms {
				cases = append(cases, a.Case)
			}
			doneIdx := len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: v.doneRV()})
			defIdx := -1
			if ins.B == 1 {
				defIdx = len(cases)
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
			}
			chosen, rv, open := reflect.Select(cases)
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
						f.push(v.chanZero(f, a, open, rv))
					default:
						f.push(&runtime.Tuple{Elems: []runtime.Value{v.chanZero(f, a, open, rv), open}})
					}
				}
				f.ip = table + chosen
			}
		case bytecode.OpPanic:
			panic(&runtime.Panic{Value: f.pop()})
		case bytecode.OpTrap:
			panic(&runtime.Trap{Pos: ins.Pos, Reason: fmt.Sprint(consts[ins.A])})
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
					f.locals[f.ch.NamedSlots[i]].Elem = f.pop()
				}
				f.retNamed = true
			default:
				f.results = make([]runtime.Value, n)
				for i := n - 1; i >= 0; i-- {
					f.results[i] = f.pop()
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

// resolveGlobal resolves a name in file scope order: imports, package
// globals (including lazily materialized decls), dot imports, builtins.
func (v *VM) resolveGlobal(f *frame, name string) runtime.Value {
	mv, err := v.resolveGlobalE(f, name)
	if err != nil {
		f.trap("%s", err)
	}
	return mv
}

// resolveGlobalE is resolveGlobal without the trap: failures return as
// errors so callers (e.g. SpecialContext.Resolve) can report them.
func (v *VM) resolveGlobalE(f *frame, name string) (runtime.Value, error) {
	pkg := f.fn.Pkg
	file := fileOf(f, pkg)
	// 1. file imports
	if file != nil {
		if ref, ok := pkg.Scopes[file][name]; ok {
			return ref, nil
		}
	}
	// 2. package globals / lazy members
	if gv, ok := pkg.Globals.Get(name); ok {
		if c, isCell := gv.(*runtime.Cell); isCell {
			return c.Elem, nil
		}
		return gv, nil
	}
	if pkg.Index != nil {
		if d, ok := lookupDecl(pkg, name); ok {
			mv, err := v.H.Materialize(pkg, d)
			if err != nil {
				return nil, fmt.Errorf("materialize %s: %s", name, err)
			}
			pkg.Globals.Set(name, mv)
			return mv, nil
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
				return ref, nil
			}
		}
		if loadErr != nil {
			return nil, fmt.Errorf("import %s: %w", loadPath, loadErr)
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
				return nil, fmt.Errorf("dot import %s: %s", ref.Path, err)
			}
			_, inGlobals := p.Globals.Get(name)
			inIndex := false
			if p.Index != nil {
				_, inIndex = lookupDecl(p, name)
			}
			if !inGlobals && !inIndex {
				continue
			}
			if imported != nil {
				return nil, fmt.Errorf("ambiguous dot-imported name: %s", name)
			}
			imported = p
		}
		if imported != nil {
			mv, err := v.memberOf(imported, name)
			if err != nil {
				return nil, fmt.Errorf("dot import %s: %s", imported.Path, err)
			}
			if c, isCell := mv.(*runtime.Cell); isCell {
				return c.Elem, nil
			}
			imported.Globals.Set(name, mv)
			return mv, nil
		}
	}
	// 4. builtins
	if bv, ok := v.H.Builtin(name); ok {
		return bv, nil
	}
	return nil, fmt.Errorf("undefined: %s", name)
}

func lookupDecl(pkg *runtime.Package, name string) (*index.Decl, bool) {
	if d, ok := pkg.Index.Funcs[name]; ok {
		return d, true
	}
	if td, ok := pkg.Index.Types[name]; ok && td.Decl != nil {
		return td.Decl, true
	}
	if d, ok := pkg.Index.Consts[name]; ok {
		return d, true
	}
	if d, ok := pkg.Index.Vars[name]; ok {
		return d, true
	}
	return nil, false
}

// selectMember implements base.name for import refs, packages, structs,
// typedefs, and cells.
func (v *VM) selectMember(f *frame, base runtime.Value, name string) runtime.Value {
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
			f.trap("%s", err)
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			mv = c.Elem
		} else {
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
			if _, ok := v.hostMember(b, name); ok {
				hint := "use the inspect.* accessors"
				switch name {
				case "Path", "Name", "Dir", "State", "Standard":
					hint = fmt.Sprintf("use inspect.%s(pkg)", name)
				}
				f.trap("%s (inspect-layer package metadata — %s)", err, hint)
			}
			f.trap("%s", err)
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			mv = c.Elem
		}
		return mv
	case *runtime.Struct:
		return v.structMember(f, b, name, b)
	case *runtime.Named:
		return v.namedMember(f, b, name, b)
	case *runtime.Cell:
		// a cell can hold another ref (`p := &s` stores a ref value) —
		// select on the stored ref so a pointer receiver writes through
		// to the pointee, not into p's cell.
		switch b.Elem.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			return v.selectMember(f, b.Elem, name)
		}
		switch e := b.Elem.(type) {
		case *runtime.Struct:
			return v.structMember(f, e, name, b)
		case *runtime.Named:
			return v.namedMember(f, e, name, b)
		case *runtime.TypedNil:
			return v.memberOfType(f, e.Typ, name, e, false)
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
			return v.selectMember(f, e, name)
		case *runtime.Package:
			// an inspect-layer package stored in a var
			return v.selectMember(f, e, name)
		default:
			f.trap("select %s on cell of %T", name, b.Elem)
		}
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		dv, ok := runtime.Deref(base)
		recv := base
		if !ok {
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
			return v.structMember(f, t, name, recv)
		case *runtime.Named:
			return v.namedMember(f, t, name, recv)
		case *runtime.Slice:
			return v.typedMember(f, t.Typ, name, recv, "slice")
		case *runtime.Map:
			return v.typedMember(f, t.Typ, name, recv, "map")
		case *runtime.Chan:
			return v.typedMember(f, t.Typ, name, recv, "chan")
		}
		// pointer boxes ([]*T), nils, host values, packages — dispatch
		// on the element value itself, like an unindexed select.
		return v.selectMember(f, dv, name)
	case *runtime.TypeDef:
		if _, isPtr := b.Anon.(*ast.StarExpr); isPtr {
			// `(*T).M` — a pointer method expression sees the full
			// method set (value and pointer receivers alike).
			if et := v.elemTypedef(f, b); et != nil {
				if m, ok := et.Methods[name]; ok {
					if m.PtrRecv {
						return m // receiver param *T binds the Cell argument
					}
					return v.methodExprDeref(b, m)
				}
				// promoted through an embedded field — re-select on the
				// actual receiver argument (`(*U).Sum` reaches I's value).
				if len(et.EmbedSpecs) > 0 || et.Kind == runtime.KindInterface {
					return v.methodExprThunk(b, name)
				}
			}
			f.trap("type %s has no method %s", tdName(b), name)
		}
		if m, ok := b.Methods[name]; ok {
			if m.PtrRecv {
				f.trap("invalid method expression %s.%s (needs pointer receiver)", tdName(b), name)
			}
			return m // method expression: T.M(recv, ...)
		}
		// `U.Sum` — a promoted method through an embedded field — or
		// `I.m` — an interface requirement — dispatches on the concrete
		// receiver argument, like Go's selector lowering.
		if len(b.EmbedSpecs) > 0 || b.Kind == runtime.KindInterface {
			return v.methodExprThunk(b, name)
		}
		f.trap("type %s has no method %s", b.Name, name)
	case runtime.Nil:
		// selecting a member on a nil interface value is a nil-pointer
		// dereference in Go — a script panic, not a trap.
		panic(runtime.NilDerefPanic())
	case *runtime.IfaceNil:
		if b.Typ == nil || b.Typ.Kind == runtime.KindInterface {
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
		return v.selectMember(f, &runtime.GoValue{V: b}, name)
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
		if mv, ok := v.hostMember(b.V, name); ok {
			return mv
		}
		f.trap("no member %s on host value %T", name, b.V)
	default:
		f.trap("select %s on %T", name, base)
	}
	return nil
}

// hostMember resolves a field or method on a host value: fields first,
// then the method set — Go forbids a field and method sharing a name.
// (nil, false) reports that neither exists.
func (v *VM) hostMember(hv any, name string) (runtime.Value, bool) {
	if fv, ok := hostField(hv, name); ok {
		return goValueOf(fv), true
	}
	m := reflect.ValueOf(hv).MethodByName(name)
	if !m.IsValid() {
		return nil, false
	}
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return callReflectFunc(name, m, vc, args)
	}}
	// the bound method value spells the receiver-less signature
	// (`func() int` for Len) under %T.
	bf.Target = m.Interface()
	// the method value's own PC is a thunk (reflect.methodValueCall)
	// — the declared method's Func is the only handle that still
	// points at the real code, so keep it for inspect.
	if tm, ok := reflect.TypeOf(hv).MethodByName(name); ok {
		tm := tm
		bf.Method = &tm
	}
	return bf, true
}

// hostNilMethod binds a host-backed method to a nil *T receiver: Go
// dispatches pointer methods on nil receivers, so `var b *bytes.Buffer;
// b.String()` calls String on (*bytes.Buffer)(nil) and the method's own
// nil handling decides the outcome (Buffer.String reports "<nil>").
// td is the anonymous *T typedef; a declared pointer typedef
// (`type P *T`) carries no promoted set, matching Go. The receiver
// reflect type is *U where U is the element typedef's boxed value type
// — a HostNew that returns a pointer already is the *T box.
func (v *VM) hostNilMethod(td *runtime.TypeDef, name string) (runtime.Value, bool) {
	if td == nil || td.Spec != nil || td.Kind != runtime.KindPointer {
		return nil, false
	}
	if v.H.ElemOf == nil {
		return nil, false
	}
	et, err := v.H.ElemOf(td)
	if err != nil || et == nil || et.HostNew == nil {
		return nil, false
	}
	rt := reflect.TypeOf(et.HostNew())
	if rt.Kind() != reflect.Pointer {
		rt = reflect.PointerTo(rt)
	}
	m := reflect.Zero(rt).MethodByName(name)
	if !m.IsValid() {
		return nil, false
	}
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return callReflectFunc(name, m, vc, args)
	}}
	bf.Target = m.Interface()
	if tm, ok := rt.MethodByName(name); ok {
		tm := tm
		bf.Method = &tm
	}
	return bf, true
}

// sizedIntTyp names the builtin typedef for a sized int kind — the
// reflect kind string spells the Go name exactly ("int8", "uint32").
func sizedIntTyp(k reflect.Kind) *runtime.TypeDef {
	return &runtime.TypeDef{Name: k.String(), Kind: runtime.KindNamedBasic}
}

// goValueOf adapts a reflect result to a runtime value: script-native types
// pass through, everything else stays boxed as a host GoValue. Value is
// `any`, so the pass-through list names the concrete runtime types.
func goValueOf(rv reflect.Value) runtime.Value {
	if !rv.IsValid() {
		return runtime.NIL
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			// a nil pointer-ish host value reads as nil to scripts —
			// boxing it would make `v == nil` comparisons lie
			return runtime.NIL
		}
	}
	x := rv.Interface()
	switch v := x.(type) {
	case nil:
		return runtime.NIL
	case int:
		return int64(v)
	case int64:
		// a concrete int64 keeps its width so %T spells `int64`; a
		// payload unwrapped from an interface is a script int, spelled
		// `int` (both store int64 — only rv's own kind can tell them
		// apart).
		if rv.Kind() == reflect.Interface {
			return v
		}
		return runtime.Tag(sizedIntTyp(reflect.Int64), v)
	case int8, int16, int32:
		// sized ints keep their declared width so %T spells them
		// like Go (int32 also covers rune — an alias). The tag reads
		// the payload's kind: rv may be interface-shaped while x is
		// the concrete payload.
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), pv.Int())
	case uint, uint8, uint16, uint32, uintptr:
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), int64(pv.Uint()))
	case uint64:
		if v <= math.MaxInt64 {
			if rv.Kind() == reflect.Interface {
				return int64(v)
			}
			return runtime.Tag(sizedIntTyp(reflect.Uint64), int64(v))
		}
		return &runtime.GoValue{V: x}
	case string:
		return v
	case bool:
		return v
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(&runtime.TypeDef{Name: "float32", Kind: runtime.KindNamedBasic}, float64(v))
	case float64:
		return v
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return v
	case []byte:
		// []byte unmarshals to a slice of int64s so `string(b)` and
		// indexing behave like Go source suggests. A nil slice is a
		// typed nil so `b == nil` reads like Go.
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("byte")}
		}
		el := make([]runtime.Value, len(v))
		for i, b := range v {
			el[i] = namedBasicElem("byte", int64(b))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []string:
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("string")}
		}
		el := make([]runtime.Value, len(v))
		for i, s := range v {
			el[i] = s
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("string")}
	case []any:
		// a `chan any` send deep-hosts script containers (see deepHost);
		// the receive rehydrates the flat slice shape so indexing works.
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(v))
		for i, e := range v {
			el[i] = goValueOf(reflect.ValueOf(e))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("any")}
	case map[any]any:
		if v == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}}
		for k, e := range v {
			m.Insert(goValueOf(reflect.ValueOf(k)), goValueOf(reflect.ValueOf(e)))
		}
		return m
	case error:
		// errors stay boxed — Error() and Unwrap() dispatch via reflection;
		// errors.Is/As unwrap through goNative.
		return &runtime.GoValue{V: x}
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.TypedNil, *runtime.IfaceNil,
		*runtime.Named:
		return v
	default:
		// an unnamed host slice/array ([N]T, []T) unboxes element-wise so
		// indexing and range work; a named slice type keeps its box to
		// preserve method dispatch. A nil slice is a typed nil so
		// `v == nil` reads like Go.
		switch rv.Kind() {
		case reflect.Slice:
			if rv.Type().Name() == "" {
				if rv.IsNil() {
					return &runtime.TypedNil{Typ: anonSliceTyp(elemTypeName(rv.Type().Elem()))}
				}
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = goValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: anonSliceTyp(elemTypeName(rv.Type().Elem()))}
			}
		case reflect.Array:
			if rv.Type().Name() == "" {
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = goValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: anonArrayTyp(rv.Len(), elemTypeName(rv.Type().Elem()))}
			}
		}
		return &runtime.GoValue{V: x}
	}
}

// elemTypeName names a reflect type for typedef spelling — Name() when
// it has one, the reflect spelling otherwise (struct{...}, []string).
func elemTypeName(t reflect.Type) string {
	if n := t.Name(); n != "" {
		return n
	}
	return t.String()
}

// hostField finds an exported field by name on a host value: pointer and
// interface chains are dereferenced to the struct underneath. nil or
// non-struct roots and unexported fields report not-found.
func hostField(v any, name string) (reflect.Value, bool) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	fv := rv.FieldByName(name)
	if !fv.IsValid() || !fv.CanInterface() {
		return reflect.Value{}, false
	}
	return fv, true
}

// initHostLiteral writes keyed composite-literal fields onto a
// host-created value (`&sync.Pool{New: f}`): the pointer chain is
// dereferenced to a settable struct, each key must name an exported
// field, and the value marshals to the field's type (a script func for
// New, via toReflectValue's adaptFunc).
func (v *VM) initHostLiteral(f *frame, td *runtime.TypeDef, hv any, raw []runtime.Value) {
	rv := reflect.ValueOf(hv)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		f.trap("cannot initialize host type %s with fields", td.Name)
	}
	for i := 0; i < len(raw); i += 2 {
		name, ok := raw[i].(string)
		if !ok {
			f.trap("struct literal key %T", raw[i])
		}
		fv := rv.FieldByName(name)
		if !fv.IsValid() {
			f.trap("%s has no field %s", td.Name, name)
		}
		if !fv.CanSet() {
			f.trap("cannot set unexported field %s of host type %s", name, td.Name)
		}
		val, err := toReflectValue(raw[i+1], fv.Type(), v)
		if err != nil {
			f.trap("%s.%s: %s", td.Name, name, err)
		}
		fv.Set(val)
	}
}

// initHostPositional fills a host-backed struct literal `T{v1, v2, ...}`:
// Go requires exactly NumField values, each assignable to the field in
// declaration order (unicode.RangeTable entries arrive this way).
func (v *VM) initHostPositional(f *frame, td *runtime.TypeDef, hv any, raw []runtime.Value) {
	rv := reflect.ValueOf(hv)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		f.trap("cannot initialize host type %s with fields", td.Name)
	}
	if len(raw) != rv.NumField() {
		f.trap("wrong number of fields in literal of host type %s: %d != %d", td.Name, len(raw), rv.NumField())
	}
	for i, e := range raw {
		fv := rv.Field(i)
		if !fv.CanSet() {
			f.trap("implicit assignment to unexported field %s of host type %s", rv.Type().Field(i).Name, td.Name)
		}
		val, err := toReflectValue(e, fv.Type(), v)
		if err != nil {
			f.trap("%s.%s: %s", td.Name, rv.Type().Field(i).Name, err)
		}
		fv.Set(val)
	}
}

// toReflectValue marshals a runtime value to a reflect.Value of the
// requested type: empty interfaces carry the value verbatim (a *Cell stays
// a pointer, slices/maps cross unconverted), func types wrap the callable
// so the host can invoke it back on the calling VM (vc), named/cell
// wrappers unwrap, slices and maps convert element-wise, scalars assign or
// convert.
func toReflectValue(v runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if u, ok := v.(*runtime.UConst); ok {
		mv, err := materializeDefault(u)
		if err != nil {
			return reflect.Value{}, err
		}
		v = mv
	}
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
		if v == nil || v == runtime.NIL {
			return reflect.Zero(t), nil
		}
		// a host func taking `any` can only consume the value through
		// reflect, so script containers marshal to real host values —
		// slices as []any, maps as map[any]any. Pointer-like values stay
		// verbatim: their address-ness is the point. callReflectFunc
		// copies a converted slice's elements back after the call.
		v = deepHost(v)
		if v == nil || v == runtime.NIL {
			return reflect.Zero(t), nil
		}
		av := reflect.ValueOf(v)
		if !av.IsValid() {
			return reflect.Zero(t), nil
		}
		out := reflect.New(t).Elem()
		out.Set(av)
		return out, nil
	}
	if t.Kind() == reflect.Func {
		return adaptFunc(v, t, vc)
	}
	// holder keeps the pointer-like node (Cell/FieldRef/IndexRef) the
	// final value was loaded through: when the parameter wants a pointer
	// the callee can write through, a fresh host pointer is built here
	// and callReflectFunc copies the pointee back into holder.
	// orig keeps the arg as passed — a `&w` cell, a field ref — so the
	// interface adaptation below sees the arg's method set under Go's
	// receiver rule (a pointer's set includes pointer receivers).
	var holder runtime.Value
	orig := v
	for {
		if n, ok := v.(*runtime.Named); ok {
			v = n.V
			continue
		}
		switch v.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			holder = v // last ref wins — the deepest pointer chain link
		}
		if dv, ok := runtime.Deref(v); ok {
			v = dv
			continue
		}
		break
	}
	var av reflect.Value
	switch x := v.(type) {
	case nil, runtime.Nil:
		return reflect.Zero(t), nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		return reflect.Zero(t), nil
	case *runtime.GoValue:
		av = reflect.ValueOf(x.V)
	case *runtime.Slice:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return reflect.Value{}, fmt.Errorf("cannot convert script slice to %s", t)
		}
		out := reflect.New(t).Elem()
		// byte slices marshal in one pass — the element loop below
		// builds a reflect.Value per byte otherwise (issue #362).
		if t.Elem().Kind() == reflect.Uint8 {
			if bs, ok := scriptBytes(x); ok {
				if t.Kind() == reflect.Slice {
					av = reflect.ValueOf(bs)
				} else {
					reflect.Copy(out, reflect.ValueOf(bs))
					av = out
				}
				break
			}
		}
		if t.Kind() == reflect.Slice {
			out.Set(reflect.MakeSlice(t, len(x.Elems), len(x.Elems)))
		}
		for i, e := range x.Elems {
			if i >= out.Len() {
				break
			}
			ev, err := toReflectValue(e, t.Elem(), vc)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(ev)
		}
		av = out
	case *runtime.Map:
		if t.Kind() != reflect.Map {
			return reflect.Value{}, fmt.Errorf("cannot convert script map to %s", t)
		}
		out := reflect.MakeMapWithSize(t, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			kv, err := toReflectValue(k, t.Key(), vc)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("map key: %w", err)
			}
			ev, err := toReflectValue(e, t.Elem(), vc)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("map elem: %w", err)
			}
			out.SetMapIndex(kv, ev)
		}
		av = out
	case string, bool, int64, float64:
		av = reflect.ValueOf(x)
	default:
		av = reflect.ValueOf(x)
	}
	if !av.IsValid() {
		return reflect.Zero(t), nil
	}
	if av.Type().AssignableTo(t) {
		return av, nil
	}
	// a host-typed composite literal boxes `*T`; placing it inside a
	// `T` field/element (e.g. []unicode.Range16{{...}}) needs the pointee.
	if av.Kind() == reflect.Pointer && !av.IsNil() && av.Type().Elem().AssignableTo(t) {
		return av.Elem(), nil
	}
	if av.Type().ConvertibleTo(t) {
		return av.Convert(t), nil
	}
	// `&b` / pointer-var args cross as the pointee value — rebuild a real
	// host pointer when the parameter wants one (a *T param or an
	// interface the pointer type implements, like io.Writer). The temp
	// points at a copy; callReflectFunc writes it back to holder.
	if holder != nil {
		if t.Kind() == reflect.Pointer && av.Type().ConvertibleTo(t.Elem()) {
			pv := reflect.New(t.Elem())
			pv.Elem().Set(av.Convert(t.Elem()))
			return pv, nil
		}
		if pt := reflect.PointerTo(av.Type()); pt.AssignableTo(t) {
			pv := reflect.New(av.Type())
			pv.Elem().Set(av)
			return pv, nil
		}
	}
	// a script value that declares the interface's methods adapts to a
	// live proxy: host code calling back into script — bufio.Reset on an
	// interpreted chunkWriter, or io.Copy fed an interpreted body — can
	// reach the script methods through it.
	if t.Kind() == reflect.Interface && t.NumMethod() > 0 {
		if pv, ok := adaptIface(orig, t, vc); ok {
			return pv, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", av.Type(), t)
}

// adaptIface reports a scriptIface-family proxy for v when v declares
// every method interface t requires. Pure reflect cannot fabricate
// interface impls, so the adapter's static method set must contain t's
// — and the proxy variant is picked by whether the script declares the
// OPTIONAL-probe methods (io.WriterTo/io.ReaderFrom, asserted by
// io.Copy and friends): a Read-only script value adapted to io.Reader
// must NOT satisfy WriterTo, or io.Copy calls a method that does not
// exist and returns its error instead of copying through Read.
func adaptIface(v runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, bool) {
	if vc == nil {
		return reflect.Value{}, false
	}
	// script containers only — a struct value or a pointer box (cell,
	// field/index ref) or named tag carrying one. The IfaceMember probes
	// below apply the receiver rule to whatever box arrived.
	switch v.(type) {
	case *runtime.Struct, *runtime.Named,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
	default:
		return reflect.Value{}, false
	}
	// v is the arg as passed — a bare *Struct or a pointer box wrapping
	// it. Go stores the pointer when the caller passes &s, so the
	// adapter's declared surface must come from that box's method set.
	si := &scriptIface{vc: vc, recv: v}
	// the probe answers "does the arg offer this method" — the value's
	// method set under Go's receiver rule, so a bare struct no longer
	// advertises a WriteTo it could never satisfy.
	_, hasWT := runtime.IfaceMember(vc, v, "WriteTo")
	_, hasRF := runtime.IfaceMember(vc, v, "ReadFrom")
	var proxy any = si
	switch {
	case hasWT && hasRF:
		proxy = scriptIfaceWTRF{si}
	case hasWT:
		proxy = scriptIfaceWT{si}
	case hasRF:
		proxy = scriptIfaceRF{si}
	}
	st := reflect.TypeOf(proxy)
	if !st.Implements(t) {
		return reflect.Value{}, false
	}
	for i := 0; i < t.NumMethod(); i++ {
		if _, ok := runtime.IfaceMember(vc, v, t.Method(i).Name); !ok {
			return reflect.Value{}, false
		}
	}
	out := reflect.New(t).Elem()
	out.Set(reflect.ValueOf(proxy))
	return out, true
}

// scriptIfaceWT/scriptIfaceRF/scriptIfaceWTRF carry the io extension
// methods a script value may declare: embedding *scriptIface promotes
// its methods and the variant adds only the probe methods the script
// actually has, so the concrete method set stays honest.
type (
	scriptIfaceWT   struct{ *scriptIface }
	scriptIfaceRF   struct{ *scriptIface }
	scriptIfaceWTRF struct{ *scriptIface }
)

func (s scriptIfaceWT) WriteTo(w io.Writer) (int64, error)   { return s.writeTo(w) }
func (s scriptIfaceRF) ReadFrom(r io.Reader) (int64, error)  { return s.readFrom(r) }
func (s scriptIfaceWTRF) WriteTo(w io.Writer) (int64, error) { return s.writeTo(w) }
func (s scriptIfaceWTRF) ReadFrom(r io.Reader) (int64, error) {
	return s.readFrom(r)
}

// scriptIface forwards host-interface calls into script: the script
// value's Read/Write/Close/... methods run through vc, so an
// interpreted type can serve as an io.Writer or error to bound host
// helpers. Its method set omits the io extension methods WriteTo and
// ReadFrom — host code probes those opportunistically (io.Copy asserts
// them on its io.Reader/io.Writer args), so they only appear on the
// scriptIfaceW* variants adaptIface picks when the script declares them.
type scriptIface struct {
	vc   runtime.VMCaller
	recv runtime.Value
}

func (s *scriptIface) call(name string, args ...runtime.Value) ([]runtime.Value, error) {
	m, ok := s.vc.Member(s.recv, name)
	if !ok || m == nil || m == runtime.NIL {
		return nil, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	r, err := s.vc.Call(m, args)
	if err != nil {
		return nil, err
	}
	if t, ok := r.(*runtime.Tuple); ok {
		return t.Elems, nil
	}
	return []runtime.Value{r}, nil
}

// errOut extracts the error slot of a script (x, err) pair: host error
// sentinels (io.EOF) arrive as GoValues and pass through verbatim so
// identity comparisons host-side still hold.
func errOut(rs []runtime.Value, i int) error {
	if i >= len(rs) {
		return nil
	}
	switch e := runtime.Unwrap(rs[i]).(type) {
	case nil, runtime.Nil:
		return nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		return nil
	case *runtime.GoValue:
		if err, ok := e.V.(error); ok {
			return err
		}
		return fmt.Errorf("%v", e.V)
	default:
		return fmt.Errorf("%v", rs[i])
	}
}

func intOut(rs []runtime.Value, i int) int64 {
	if i >= len(rs) {
		return 0
	}
	switch n := runtime.Unwrap(rs[i]).(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// byteSliceArg boxes a host []byte as a script slice of byte-tagged
// elements — the shape a script `[]byte` parameter receives.
func byteSliceArg(p []byte) *runtime.Slice {
	el := make([]runtime.Value, len(p))
	for i, b := range p {
		el[i] = runtime.Tag(runtime.BasicTypedef("byte"), int64(b))
	}
	return &runtime.Slice{Elems: el, Typ: &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("byte")}}}
}

// backBytes mirrors a script callee's writes to the boxed slice back
// into the host buffer — the []byte crossing is element-copied, so
// fills the callee performed would otherwise be invisible to the host.
func backBytes(dst []byte, src runtime.Value, n int) {
	sl, ok := src.(*runtime.Slice)
	if !ok {
		return
	}
	for i := 0; i < n && i < len(sl.Elems) && i < len(dst); i++ {
		if b, ok := runtime.Unwrap(sl.Elems[i]).(int64); ok {
			dst[i] = byte(b)
		}
	}
}

func (s *scriptIface) Write(p []byte) (int, error) {
	pv := byteSliceArg(p)
	rs, err := s.call("Write", pv)
	n := int(intOut(rs, 0))
	backBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, errOut(rs, 1)
}

func (s *scriptIface) Read(p []byte) (int, error) {
	pv := byteSliceArg(p)
	rs, err := s.call("Read", pv)
	n := int(intOut(rs, 0))
	backBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, errOut(rs, 1)
}

func (s *scriptIface) Close() error {
	rs, err := s.call("Close")
	if err != nil {
		return err
	}
	return errOut(rs, 0)
}

func (s *scriptIface) WriteString(str string) (int, error) {
	rs, err := s.call("WriteString", str)
	if err != nil {
		return 0, err
	}
	return int(intOut(rs, 0)), errOut(rs, 1)
}

func (s *scriptIface) ReadByte() (byte, error) {
	rs, err := s.call("ReadByte")
	if err != nil {
		return 0, err
	}
	return byte(intOut(rs, 0)), errOut(rs, 1)
}

func (s *scriptIface) WriteByte(b byte) error {
	rs, err := s.call("WriteByte", runtime.Tag(runtime.BasicTypedef("byte"), int64(b)))
	if err != nil {
		return err
	}
	return errOut(rs, 0)
}

func (s *scriptIface) ReadRune() (rune, int, error) {
	rs, err := s.call("ReadRune")
	if err != nil {
		return 0, 0, err
	}
	return rune(intOut(rs, 0)), int(intOut(rs, 1)), errOut(rs, 2)
}

func (s *scriptIface) Seek(offset int64, whence int) (int64, error) {
	rs, err := s.call("Seek", offset, int64(whence))
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(rs, 1)
}

func (s *scriptIface) Flush() error {
	rs, err := s.call("Flush")
	if err != nil {
		return err
	}
	return errOut(rs, 0)
}

func (s *scriptIface) Error() string {
	rs, err := s.call("Error")
	if err != nil || len(rs) == 0 {
		return "script error"
	}
	if str, ok := runtime.Unwrap(rs[0]).(string); ok {
		return str
	}
	return "script error"
}

func (s *scriptIface) String() string {
	rs, err := s.call("String")
	if err != nil || len(rs) == 0 {
		return ""
	}
	if str, ok := runtime.Unwrap(rs[0]).(string); ok {
		return str
	}
	return ""
}

func (s *scriptIface) Len() int {
	rs, err := s.call("Len")
	if err != nil {
		return 0
	}
	return int(intOut(rs, 0))
}

func (s *scriptIface) Less(i, j int) bool {
	rs, err := s.call("Less", int64(i), int64(j))
	if err != nil || len(rs) == 0 {
		return false
	}
	b, _ := runtime.Unwrap(rs[0]).(bool)
	return b
}

func (s *scriptIface) Swap(i, j int) {
	s.call("Swap", int64(i), int64(j))
}

// writeTo/readFrom back the io extension methods — they are
// deliberately unexported so *scriptIface's method set never claims an
// io.WriterTo/io.ReaderFrom the script value does not declare (only the
// scriptIfaceW* wrapper types surface them).
func (s *scriptIface) writeTo(w io.Writer) (int64, error) {
	rs, err := s.call("WriteTo", &runtime.GoValue{V: w})
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(rs, 1)
}

func (s *scriptIface) readFrom(r io.Reader) (int64, error) {
	rs, err := s.call("ReadFrom", &runtime.GoValue{V: r})
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(rs, 1)
}

// deepHost converts a script value for an `any` parameter: containers
// become real host values (Slice → []any, Map → map[any]any) so the
// callee can reflect over them; Named and GoValue unwrap; typed nils
// read as nil. Everything else — cells, funcs, structs — stays verbatim.
func deepHost(v runtime.Value) runtime.Value {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		return nil
	case *runtime.UConst:
		mv, err := materializeDefault(x)
		if err != nil {
			panic(&runtime.Panic{Value: &runtime.GoValue{V: err}})
		}
		return deepHost(mv)
	case *runtime.Named:
		return deepHost(x.V)
	case *runtime.GoValue:
		return x.V
	case *runtime.Slice:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = deepHost(e)
		}
		return out
	case *runtime.Map:
		out := make(map[any]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			out[deepHost(k)] = deepHost(e)
		}
		return out
	}
	return v
}

// scriptBytes reads a script slice's elements as bytes in one pass —
// marshaling element-wise would box a reflect.Value per byte. Elements
// must all unwrap to an in-range int64; anything else reports false so
// the caller falls back to the generic loop (issue #362).
func scriptBytes(x *runtime.Slice) ([]byte, bool) {
	bs := make([]byte, len(x.Elems))
	for i, e := range x.Elems {
		n, ok := runtime.Unwrap(e).(int64)
		if !ok || n < 0 || n > 255 {
			return nil, false
		}
		bs[i] = byte(n)
	}
	return bs, true
}

// argBack pairs an argument with the host value it marshaled to so a
// callee's writes through it can mirror back into the script value.
type argBack struct {
	arg runtime.Value
	rv  reflect.Value
}

// sliceArg resolves an argument to the script slice it carries, through
// Named tags and one level of pointer-like deref.
func sliceArg(a runtime.Value) *runtime.Slice {
	u := runtime.Unwrap(a)
	if dv, ok := runtime.Deref(u); ok {
		u = runtime.Unwrap(dv)
	}
	s, _ := u.(*runtime.Slice)
	return s
}

// refArg resolves an argument to the pointer-like node (Cell/FieldRef/
// IndexRef) nearest its leaf value — the write-back target for a
// pointer crossed by address — plus the leaf it resolved to.
func refArg(a runtime.Value) (ref runtime.Value, leaf runtime.Value) {
	u := a
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		switch u.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			ref = u
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	return ref, u
}

// blockingHostMethods names host methods that can park indefinitely —
// their m.Call runs on a helper goroutine selected against proc.done, so
// process death unwinds the script frame with procExit even though the
// real call keeps running (the helper leaks — the documented host-park
// limit). Matching is by method name: Wait/Lock/RLock cover the sync
// primitives; Do covers sync.Once (its argument can itself block).
var blockingHostMethods = map[string]bool{
	"Wait": true, "WaitTimeout": true,
	"Lock": true, "RLock": true, "LockTimeout": true, "TryLockTimeout": true,
	"Do": true,
}

// procDoneOf reports the caller VM's proc-done channel; non-*VM callers
// (host-side VMCaller implementations) have no process to watch.
func procDoneOf(vc runtime.VMCaller) <-chan struct{} {
	if v, ok := vc.(*VM); ok && v.proc != nil {
		return v.proc.done
	}
	return nil
}

// callReflectFunc invokes a host func reflect.Value with script args:
// arity checks, variadic/CallSlice handling, and result marshaling —
// shared by host method values and callable GoValue funcs.
func callReflectFunc(name string, m reflect.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	mt := m.Type()
	nin := mt.NumIn()
	if !mt.IsVariadic() && len(args) != nin {
		return nil, fmt.Errorf("%s needs %d args, got %d", name, nin, len(args))
	}
	if mt.IsVariadic() && len(args) < nin-1 {
		return nil, fmt.Errorf("%s needs at least %d args, got %d", name, nin-1, len(args))
	}
	// a trailing slice assignable to the variadic parameter calls
	// through CallSlice — `f(xs...)`-style forwarding on natives.
	in := make([]reflect.Value, len(args))
	var backs []argBack
	useSlice := false
	for i, a := range args {
		pt := mt.In(min(i, nin-1))
		if mt.IsVariadic() && i >= nin-1 {
			if i == nin-1 && len(args) == nin {
				if rv, err := toReflectValue(a, pt, vc); err == nil {
					in[i] = rv
					useSlice = true
					backs = append(backs, argBack{arg: a, rv: rv})
					continue
				}
			}
			pt = pt.Elem()
		}
		rv, err := toReflectValue(a, pt, vc)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
		}
		in[i] = rv
		backs = append(backs, argBack{arg: a, rv: rv})
	}
	var out []reflect.Value
	watched := false
	if blockingHostMethods[name] {
		if done := procDoneOf(vc); done != nil {
			watched = true
			// a method that can park indefinitely must not outlive its
			// process: run it on a helper goroutine and select on
			// proc.done, so a sibling's death unwinds this frame with
			// procExit instead of hanging the process (mu held by a
			// dead goroutine never releases its Lock waiters). The
			// helper keeps running the real call — a leaked goroutine,
			// the same documented limit as any host park.
			type callRes struct {
				out []reflect.Value
				p   any
			}
			resCh := make(chan callRes, 1)
			go func() {
				// register before m.Call can invoke a script callback:
				// a Call arriving from this goroutine re-enters the VM
				// synchronously, so its panic propagates back through the
				// join rather than failing the process.
				if v, ok := vc.(*VM); ok && v.proc != nil {
					defer v.proc.watchCallFrom(goroutineID())()
				}
				defer func() {
					if r := recover(); r != nil {
						resCh <- callRes{p: r}
					}
				}()
				if useSlice {
					resCh <- callRes{out: m.CallSlice(in)}
				} else {
					resCh <- callRes{out: m.Call(in)}
				}
			}()
			select {
			case <-done:
				panic(procExit{})
			case r := <-resCh:
				if r.p != nil {
					panic(r.p)
				}
				out = r.out
			}
		}
	}
	if !watched {
		if useSlice {
			out = m.CallSlice(in)
		} else {
			out = m.Call(in)
		}
	}
	// callee writes propagate back: a script slice crossed as a fresh
	// host slice shares nothing (Reader.Read's buffer), and an
	// addressable arg crossed as a fresh host pointer (a *T param or an
	// interface the pointer implements) needs its pointee mirrored.
	for _, wb := range backs {
		handled := false
		if ss := sliceArg(wb.arg); ss != nil {
			rv := wb.rv
			if rv.Kind() == reflect.Interface {
				rv = rv.Elem()
			}
			if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
				// a []byte arg commits in one pass — goValueOf per
				// element would box and tag every byte again (#362).
				// Unaddressable arrays can't view as Bytes — they keep
				// the generic element loop.
				if rv.Type().Elem().Kind() == reflect.Uint8 && (rv.Kind() == reflect.Slice || rv.CanAddr()) {
					bs := rv.Bytes()
					td := sizedIntTyp(reflect.Uint8)
					for i := 0; i < len(bs) && i < len(ss.Elems); i++ {
						ss.Elems[i] = runtime.Tag(td, int64(bs[i]))
					}
					handled = true
					continue
				}
				for i := 0; i < rv.Len() && i < len(ss.Elems); i++ {
					ss.Elems[i] = goValueOf(rv.Index(i))
				}
				handled = true
			}
		}
		if handled {
			continue
		}
		ref, leaf := refArg(wb.arg)
		if ref == nil || wb.rv.Kind() != reflect.Pointer {
			continue
		}
		// the arg's own pointer crossing directly isn't a temp —
		// copying its pointee back would un-box the cell.
		if gv, ok := leaf.(*runtime.GoValue); ok {
			if pv := reflect.ValueOf(gv.V); pv.IsValid() && pv.Kind() == reflect.Pointer && pv.Pointer() == wb.rv.Pointer() {
				continue
			}
		}
		val := goValueOf(wb.rv.Elem())
		if cell, ok := ref.(*runtime.Cell); ok {
			if n, ok := cell.Elem.(*runtime.Named); ok {
				// a declared cell keeps its tag through the round trip
				vv := runtime.Unwrap(val)
				if iv, ok := vv.(int64); ok {
					vv = maskInt(iv, sizedNameOf(n.Typ))
				}
				val = runtime.Tag(n.Typ, vv)
			}
		}
		runtime.SetRef(ref, val)
	}
	switch len(out) {
	case 0:
		return runtime.NIL, nil
	case 1:
		return goValueOf(out[0]), nil
	default:
		el := make([]runtime.Value, len(out))
		for i, o := range out {
			el[i] = goValueOf(o)
		}
		return &runtime.Tuple{Elems: el}, nil
	}
}

// adaptFunc wraps a script callable as a host-typed func so methods taking
// a func parameter (sync.Once.Do, sort callbacks, WalkDir-style visitors)
// can invoke it: calls run back on vc — the VM the host call is executing
// on, which is the right goroutine for synchronous host callbacks. A host
// that retains the func and calls it later from another goroutine
// (sync.WaitGroup.Go, time.AfterFunc) lands on vc.Call from a foreign
// goroutine, which reroutes to a spawned child VM — see Call.
func adaptFunc(x runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if vc == nil {
		return reflect.Value{}, fmt.Errorf("cannot adapt %T to %s off-VM", x, t)
	}
	switch x.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.Named:
	default:
		return reflect.Value{}, fmt.Errorf("cannot use %T as %s", x, t)
	}
	fv := reflect.MakeFunc(t, func(in []reflect.Value) []reflect.Value {
		sargs := make([]runtime.Value, len(in))
		for i, a := range in {
			sargs[i] = goValueOf(a)
		}
		r, err := vc.Call(x, sargs)
		if err != nil {
			panic(err)
		}
		nout := t.NumOut()
		var rs []runtime.Value
		if tup, ok := r.(*runtime.Tuple); ok {
			rs = tup.Elems
		} else {
			rs = []runtime.Value{r}
		}
		out := make([]reflect.Value, nout)
		for i := range out {
			var rv reflect.Value
			var err error
			if i < len(rs) {
				rv, err = toReflectValue(rs[i], t.Out(i), vc)
			} else {
				rv, err = toReflectValue(runtime.NIL, t.Out(i), vc)
			}
			if err != nil {
				panic(err)
			}
			out[i] = rv
		}
		return out
	})
	return fv, nil
}

// methodExprThunk builds a T.M/(*T).M method-expression value for a
// method reached through an embedded field or an interface
// requirement: `U.Sum`/`(*U).Sum`/`I.m` compile to a func(recv,
// args...) that re-selects the member on its receiver argument, so
// embedded and interface dispatch run on the concrete value like Go's
// selector lowering.
func (v *VM) methodExprThunk(td *runtime.TypeDef, name string) runtime.Value {
	return &runtime.BuiltinFunc{
		Name: tdName(td) + "." + name,
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
			if in, ok := args[0].(*runtime.IfaceNil); ok && (in.Typ == nil || in.Typ.Kind == runtime.KindInterface) {
				panic(runtime.NilDerefPanic())
			}
			m, ok := vm.Member(args[0], name)
			if !ok {
				return nil, fmt.Errorf("type %s has no method %s", tdName(td), name)
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
		Name: tdName(td) + "." + m.Name,
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

func (v *VM) structMember(f *frame, s *runtime.Struct, name string, recv runtime.Value) runtime.Value {
	def := s.Def
	for i, fn := range def.Fields {
		if fn == name {
			return s.Fields[i]
		}
	}
	if m, ok := def.Methods[name]; ok {
		if err := m.EnsureCompiled(); err != nil {
			f.trap("%s", err)
		}
		r := recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
		} else {
			// value receiver operates on a copy
			if dv, ok := runtime.Deref(r); ok {
				r = dv
			}
			r = valueCopy(r)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}
	}
	// promoted member: fields, methods, interface-stored values and
	// host members all compete at promotion depth, breadth-first like
	// Go (the same walk composite-literal keys use).
	if pr, ok := v.promotedMember(f, s, name, true); ok {
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
			f.trap("%s", err)
		}
		r := pr.recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
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
	f.trap("%s has no field or method %s", def.Name, name)
	return nil
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
func (v *VM) namedMember(f *frame, n *runtime.Named, name string, recv runtime.Value) runtime.Value {
	// an anonymous *Declared typedef exposes the pointee's method set —
	// `(*T)(p)` keeps T's methods callable on the conversion result.
	td := n.Typ
	for td != nil && td.Kind == runtime.KindPointer && td.Spec == nil && td.Methods[name] == nil {
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			break
		}
		td = et
	}
	if m, ok := td.Methods[name]; ok {
		if err := m.EnsureCompiled(); err != nil {
			f.trap("%s", err)
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
		return &runtime.BoundMethod{Recv: r, Fn: m}
	}
	// fields live on the underlying struct value — promoted fields of the
	// underlying type are stored as fields on it, but promoted METHODS of
	// the underlying type are not part of the named type's method set. A
	// pointer-underlying declaration (`type P *Sq`) dereferences first.
	sv := n.V
	if dv, ok := runtime.Deref(n.V); ok {
		sv = dv
	}
	if gv, isGo := sv.(*runtime.GoValue); isGo {
		// a host-boxed payload (a tagged host composite literal — its
		// Named tag only names the declared type) resolves fields and
		// methods on the box itself, e.g. p.Get() on &sync.Pool{...}.
		if mv, ok := v.hostMember(gv.V, name); ok {
			return mv
		}
		f.trap("no member %s on host value %T", name, gv.V)
	}
	if s, isStruct := sv.(*runtime.Struct); isStruct {
		for i, fn := range s.Def.Fields {
			if fn == name {
				return s.Fields[i]
			}
		}
		if pr, ok := v.promotedMember(f, s, name, true); ok {
			if pr.host != nil {
				return v.selectMember(f, pr.host, name)
			}
			if pr.st != nil {
				return pr.st.Fields[pr.idx]
			}
			// a defined type does not inherit promoted methods or
			// interface specs — fall through to the no-member trap.
		}
	}
	f.trap("%s has no field or method %s", tdName(n.Typ), name)
	return nil
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
		dv, ok := runtime.Deref(base)
		if !ok {
			break
		}
		base = dv
	}
	switch b := base.(type) {
	case *runtime.Struct:
		for i, fn := range b.Def.Fields {
			if fn == name {
				// the field's declared type constrains the write —
				// `s.f = nil` on a *T field stores a typed nil.
				var ft *runtime.TypeDef
				if fts := v.fieldTypedefs(b.Def); i < len(fts) {
					ft = fts[i]
				}
				b.Fields[i] = v.coerce(f, val, ft)
				return
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
				f.trap("%s has no field %s", b.Def.Name, name)
			}
			var ft *runtime.TypeDef
			if fts := v.fieldTypedefs(pr.st.Def); pr.idx < len(fts) {
				ft = fts[pr.idx]
			}
			pr.st.Fields[pr.idx] = v.coerce(f, val, ft)
			return
		}
		f.trap("%s has no field %s", b.Def.Name, name)
	case *runtime.GoValue:
		// host struct behind a pointer: `cmd.Dir = "sub"` writes through
		// reflection so intrinsics like os/exec stay wired to scripts.
		rv := reflect.ValueOf(b.V)
		for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
			if rv.IsNil() {
				f.trap("set field %s on nil host %T", name, b.V)
			}
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			f.trap("set field %s on host value %T", name, b.V)
		}
		fv := rv.FieldByName(name)
		if !fv.IsValid() || !fv.CanSet() {
			f.trap("host value %T has no settable field %s", b.V, name)
		}
		nv, err := toReflectValue(val, fv.Type(), v)
		if err != nil {
			f.trap("set field %s: %s", name, err)
		}
		fv.Set(nv)
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
	idx = runtime.Unwrap(materialize(f, idx)) // named key/index types hash as their value
	base = materialize(f, base)               // `const s = "x"; s[0]` indexes a UConst
	switch b := base.(type) {
	case *runtime.Named:
		return v.index(f, b.V, idx)
	case *runtime.IfaceNil:
		return v.index(f, &runtime.TypedNil{Typ: b.Typ}, idx)
	case *runtime.TypedNil:
		switch b.Typ.Kind {
		case runtime.KindMap:
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
		i, ok := idx.(int64)
		if !ok {
			f.trap("slice index is %T", idx)
		}
		if b.N > 0 {
			// a virtual zero-size slice vends its shared element value.
			if i < 0 || i >= b.N {
				panic(runtime.BoundsPanic(i, int(b.N)))
			}
			return v.elemRead(f, b.Typ, b.Zero)
		}
		return v.elemRead(f, b.Typ, b.Elems[i])
	case *runtime.Map:
		val, found := b.Get(idx)
		if !found {
			val = v.mapZero(f, b.Typ)
		}
		return v.elemRead(f, b.Typ, val)
	case string:
		i, ok := idx.(int64)
		if !ok {
			f.trap("string index is %T", idx)
		}
		return runtime.Tag(v.builtinTypedef("uint8"), int64(b[i]))
	default:
		f.trap("index on %T", base)
		return nil
	}
	return nil
}

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
//   - m+recv: a method — recv is the embedded field value it binds to
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
	host runtime.Value
}

// methCand is a promoted method found at one BFS level: the declared
// function plus the embedded field value it binds to.
type methCand struct {
	m    *runtime.Function
	recv runtime.Value
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
	if v.H.ResolveType == nil || v.H.IfaceReqs == nil {
		return nil, false
	}
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
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
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
						if reqs, err := v.H.IfaceReqs(embTd); err == nil && reqs[name] {
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
					inner := hostMemberInner(embTd.HostNew(), name, embTd == raw)
					if inner < 0 {
						continue
					}
					abs := depth + 1 + inner
					recv := st.Fields[idx]
					if ptr && hostNilEmbed(recv) && !v.hostNilCallable(embTd.HostNew(), name) {
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
				if m, ok := mtd.Methods[name]; ok && idx < len(st.Fields) {
					methCands = append(methCands, methCand{m, st.Fields[idx]})
				}
				if len(embTd.Fields) == 0 {
					// non-struct embeds (interfaces, basics) promote
					// nothing else.
					continue
				}
				j := -1
				for fi, fn := range embTd.Fields {
					if fn == name {
						j = fi
						break
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
						// exists deeper, and at which depth.
						if d, c := v.embedTypeDepth(embTd, name, map[*runtime.TypeDef]bool{}); d > 0 {
							abs := depth + 1 + d
							if nilDepth == 0 || abs < nilDepth {
								nilDepth, nilPaths = abs, c
							} else if abs == nilDepth {
								nilPaths += c
							}
						}
						if d, c := v.embedTypeMethodDepth(embTd, name, map[*runtime.TypeDef]bool{}); d > 0 {
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
			if total > 1 {
				f.trap("ambiguous selector %s", name)
			}
			switch {
			case len(thisHost) == 1:
				return &promoted{host: thisHost[0]}, true
			case len(methCands) == 1:
				return &promoted{m: methCands[0].m, recv: methCands[0].recv}, true
			case len(ifaceCands) == 1:
				return &promoted{recv: ifaceCands[0]}, true
			default:
				return &promoted{st: hits[0].st, idx: hits[0].idx}, true
			}
		}
		if nilDepth > 0 && nilDepth <= depth+1 {
			if nilPaths > 1 {
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
	if nilDepth > 0 {
		if nilPaths > 1 {
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
func hostMemberInner(zero any, name string, methods bool) int {
	t := reflect.TypeOf(zero)
	if t == nil {
		return -1
	}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() == reflect.Struct {
		if sf, ok := st.FieldByName(name); ok && sf.PkgPath == "" {
			return len(sf.Index) - 1 // 0 = a direct field
		}
	}
	if methods {
		if _, ok := t.MethodByName(name); ok {
			return hostMethodInner(t, name)
		}
	}
	return -1
}

// hostMethodInner counts the embed levels inside the host type that
// name's method is promoted through — 0 when it is declared on t's own
// method set. t is the slot's declared type: a *T slot contributes the
// methods of *T, including pointer receivers of embedded values.
func hostMethodInner(t reflect.Type, name string) int {
	return methodInner(t, name, map[reflect.Type]bool{})
}

// methodInner: name is in t's method set — how deeply inside t it is
// promoted. Each anonymous field contributes its own type's method set
// — *E when the chain went through a pointer, an interface as itself —
// and the shallowest path wins. reflect cannot tell a method declared
// on t from one promoted through an embed, so a name found in a
// child's set counts via that child; contributed types are visited
// once per path so a recursively-embedded host type (struct{ *T })
// terminates instead of overflowing the stack.
func methodInner(t reflect.Type, name string, seen map[reflect.Type]bool) int {
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return 0 // non-struct types declare their methods
	}
	if seen[t] {
		return -1
	}
	seen[t] = true
	defer delete(seen, t)
	underPtr := t.Kind() == reflect.Pointer
	best := -1
	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if !sf.Anonymous {
			continue
		}
		ct := sf.Type
		if ct.Kind() != reflect.Pointer && ct.Kind() != reflect.Interface && underPtr {
			// a value embed contributes *E's method set when the
			// embedding chain went through a pointer.
			ct = reflect.PointerTo(ct)
		}
		if _, ok := ct.MethodByName(name); !ok {
			continue
		}
		if d := methodInner(ct, name, seen); d >= 0 && (best < 0 || d+1 < best) {
			best = d + 1
		}
	}
	if best >= 0 {
		return best
	}
	return 0
}

// hostNilCallable reports whether name is a pointer-receiver method on
// the boxed type — the only member callable through a nil stored
// pointer: Go passes the nil receiver straight to the method, while
// fields and value-receiver methods must dereference it first.
func (v *VM) hostNilCallable(zero any, name string) bool {
	pt := reflect.TypeOf(zero)
	if pt.Kind() != reflect.Pointer {
		return false
	}
	if _, ok := pt.MethodByName(name); !ok {
		return false // a field — reads and writes dereference
	}
	if _, ok := pt.Elem().MethodByName(name); ok {
		return false // value receiver — evaluating it dereferences
	}
	return true
}

// hostNilEmbed reports whether an embedded host-typed field's stored
// value is nil — a TypedNil/IfaceNil slot or a nil pointer box — so a
// member resolved through it panics on the implicit dereference.
func hostNilEmbed(v runtime.Value) bool {
	v = runtime.Unwrap(v)
	if dv, ok := runtime.Deref(v); ok {
		v = dv
	}
	switch x := v.(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	case *runtime.GoValue:
		rv := reflect.ValueOf(x.V)
		switch rv.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			return rv.IsNil()
		}
	}
	return false
}

// embedTypeDepth answers for the type what a value search cannot when an
// embedded pointer is nil: whether `name` is promoted through td's own
// embedded fields, the shallowest relative depth at which it resolves
// (1 = a direct field of a type embedded in td), and how many distinct
// paths reach it at that depth. Depth 0 means unreachable.
func (v *VM) embedTypeDepth(td *runtime.TypeDef, name string, seen map[*runtime.TypeDef]bool) (int, int) {
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
			if inner := hostMemberInner(et.HostNew(), name, et == raw); inner >= 0 {
				d = 1 + inner
			}
		} else {
			for _, fn := range et.Fields {
				if fn == name {
					d = 1
					break
				}
			}
			if d == 0 {
				if sd, sc := v.embedTypeDepth(et, name, seen); sd > 0 {
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
func (v *VM) embedTypeMethodDepth(td *runtime.TypeDef, name string, seen map[*runtime.TypeDef]bool) (int, int) {
	if td == nil || seen[td] || v.H.ResolveType == nil || v.H.IfaceReqs == nil {
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
		switch {
		case et.Kind == runtime.KindInterface:
			// an embedded interface promises name through the
			// stored value — it exists at this depth.
			if reqs, err := v.H.IfaceReqs(et); err == nil && reqs[name] {
				d = 1
			}
		case et.HostNew != nil:
			if inner := hostMemberInner(et.HostNew(), name, et == raw); inner >= 0 {
				d = 1 + inner
			}
		default:
			// only the embed's own methods — a defined type does not
			// inherit the underlying type's methods — except through
			// an alias, which IS its target type.
			mtd := raw
			if raw.Kind == runtime.KindAlias {
				mtd = et
			}
			if _, ok := mtd.Methods[name]; ok {
				d = 1
			}
		}
		if d == 0 {
			if sd, sc := v.embedTypeMethodDepth(et, name, seen); sd > 0 {
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
	x := u.Anon
	if x == nil && u.Spec != nil {
		x = u.Spec.Type
	}
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
	for _, cells := range [2][]*runtime.Cell{f.locals, f.upvals} {
		for _, c := range cells {
			if c == nil {
				continue
			}
			if td, ok := c.Elem.(*runtime.TypeDef); ok && td.Name == name {
				return td
			}
		}
	}
	return nil
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
	idx = runtime.Unwrap(idx)
	if n, ok := base.(*runtime.Named); ok {
		return v.indexOK(f, n.V, idx)
	}
	if in, ok := base.(*runtime.IfaceNil); ok {
		return v.indexOK(f, &runtime.TypedNil{Typ: in.Typ}, idx)
	}
	if tn, ok := base.(*runtime.TypedNil); ok {
		if tn.Typ.Kind == runtime.KindMap {
			return &runtime.Tuple{Elems: []runtime.Value{v.mapZero(f, tn.Typ), false}}
		}
		return &runtime.Tuple{Elems: []runtime.Value{v.index(f, base, idx), true}}
	}
	if m, ok := base.(*runtime.Map); ok {
		val, found := m.Get(idx)
		if !found {
			val = v.mapZero(f, m.Typ)
		}
		return &runtime.Tuple{Elems: []runtime.Value{val, found}}
	}
	return &runtime.Tuple{Elems: []runtime.Value{v.index(f, base, idx), true}}
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
	idx = runtime.Unwrap(materialize(f, idx))
	switch b := base.(type) {
	case *runtime.IfaceNil:
		v.setIndex(f, &runtime.TypedNil{Typ: b.Typ}, idx, val)
	case *runtime.TypedNil:
		switch b.Typ.Kind {
		case runtime.KindMap:
			panic(runtime.NilMapAssignPanic())
		case runtime.KindSlice:
			panic(runtime.BoundsPanic(runtime.Unwrap(idx), 0))
		case runtime.KindPointer:
			panic(runtime.NilDerefPanic())
		default:
			f.trap("index assign on nil %s", tdName(b.Typ))
		}
	case *runtime.Slice:
		i, ok := idx.(int64)
		if !ok {
			f.trap("slice index is %T", idx)
		}
		// the slice's declared element type constrains the write.
		if et := v.elemTypedef(f, b.Typ); et != nil {
			val = v.coerce(f, val, et)
		}
		if b.N > 0 {
			// a virtual zero-size element can't be observed — the type
			// has a single value — but the bounds check still applies.
			if i < 0 || i >= b.N {
				panic(runtime.BoundsPanic(i, int(b.N)))
			}
			return
		}
		b.Elems[i] = val
	case *runtime.Map:
		if idx != nil && !reflect.TypeOf(idx).Comparable() {
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
		x := v.index(f, bx, b.Key)
		if m := b.Map(); m != nil && !runtime.SharedElem(x) {
			// Go rejects interior writes on a non-reference map
			// element at compile time (m[k] is a copy) — trap
			// instead of mutating the stored value.
			f.trap("index assign on %T", base)
		}
		v.setIndex(f, x, idx, val)
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
		v.setIndex(f, x, idx, val)
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
			x, found := m.Get(b.Key)
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
			x, found := cb.Get(b.Key)
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
		return runtime.Deref(base)
	}
}

func (v *VM) slice(f *frame, base, lo, hi, max runtime.Value) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		return v.slice(f, dv, lo, hi, max)
	}
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
		l, h := bounds(f, lo, hi, 0)
		m := maxBound(f, max, 0)
		if l != 0 || h != 0 || m != 0 {
			panic(runtime.RuntimePanic(nilSliceBoundsReason(l, h, m, three)))
		}
		return b
	case *runtime.Slice:
		if b.N > 0 {
			// a virtual zero-size slice bounds-checks against its
			// logical length; the sub-slice stays virtual.
			l, h := bounds(f, lo, hi, b.N)
			m := b.CapN
			if three {
				m = maxBound(f, max, b.CapN)
			}
			return &runtime.Slice{N: h - l, CapN: m - l, Zero: b.Zero, Typ: sliceTypOf(b.Typ)}
		}
		l, h := bounds(f, lo, hi, int64(len(b.Elems)))
		if three {
			m := maxBound(f, max, int64(cap(b.Elems)))
			return &runtime.Slice{Elems: b.Elems[l:h:m], Typ: sliceTypOf(b.Typ)}
		}
		return &runtime.Slice{Elems: b.Elems[l:h], Typ: sliceTypOf(b.Typ)}
	case string:
		if three {
			// a 3-index slice on a string is a compile reject in Go —
			// loud-fail like the other compile-time checks.
			f.trap("cannot slice a string with 3 indices")
		}
		l, h := bounds(f, lo, hi, int64(len(b)))
		return b[l:h]
	default:
		f.trap("slice on %T", base)
		return nil
	}
}

// nilSliceBoundsReason renders Go's boundsError text for a failed slice
// operation on a nil slice — a live *runtime.Slice panics inside Go's
// own indexing, which already spells the full message, so only the
// nil path needs the formats reproduced (cap is 0 throughout).
func nilSliceBoundsReason(l, h, m int64, three bool) string {
	const p = "slice bounds out of range"
	if three {
		switch {
		case m < 0:
			return fmt.Sprintf("%s [::%d]", p, m)
		case m > 0:
			return fmt.Sprintf("%s [::%d] with capacity 0", p, m)
		case h < 0 || h > m:
			return fmt.Sprintf("%s [:%d:%d]", p, h, m)
		default:
			return fmt.Sprintf("%s [%d:%d:%d]", p, l, h, m)
		}
	}
	switch {
	case h < 0:
		return fmt.Sprintf("%s [:%d]", p, h)
	case h > 0:
		return fmt.Sprintf("%s [:%d] with capacity 0", p, h)
	case l < 0:
		return fmt.Sprintf("%s [%d:]", p, l)
	default:
		return fmt.Sprintf("%s [%d:0]", p, l)
	}
}

func bounds(f *frame, lo, hi runtime.Value, n int64) (int64, int64) {
	l := int64(0)
	h := n
	if lv, ok := runtime.Unwrap(materialize(f, lo)).(int64); ok {
		l = lv
	}
	if hv, ok := runtime.Unwrap(materialize(f, hi)).(int64); ok {
		h = hv
	}
	return l, h
}

// maxBound reads a 3-index slice's max operand; the full-expression form
// `a[low:high:]` uses the container's capacity. The slice operator itself
// (`elems[l:h:m]`) enforces low <= high <= max <= cap with Go's panic.
func maxBound(f *frame, max runtime.Value, capN int64) int64 {
	if mv, ok := runtime.Unwrap(materialize(f, max)).(int64); ok {
		return mv
	}
	return capN
}

// litKeyIndex resolves a composite-literal key to its int index: a
// *runtime.ImplicitIndex positional continues the running index `last`;
// named constants unwrap to int64. ok=false for anything else.
func litKeyIndex(f *frame, k runtime.Value, last int64) (int64, bool) {
	if _, isImp := k.(*runtime.ImplicitIndex); isImp {
		return last + 1, true
	}
	k = materialize(f, k)
	if nk, ok := k.(*runtime.Named); ok {
		k = nk.V
	}
	iv, ok := k.(int64)
	return iv, ok
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
					ival, ok := litKeyIndex(f, raw[i*2], last)
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
				ival, ok := litKeyIndex(f, raw[i*2], last)
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
				// fold it into the AST so the typedef spells the
				// concrete [N]T like Go's inferred length.
				at.Len = &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(len(s.Elems))}
			}
		}
		return s
	case runtime.KindMap:
		m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}, Typ: td}
		et := v.elemTypedef(f, td)
		for i := 0; i < n; i++ {
			if _, isImp := raw[i*2].(*runtime.ImplicitIndex); isImp {
				f.trap("positional element in keyed map literal")
			}
			k := runtime.Unwrap(materialize(f, raw[i*2]))
			m.Insert(k, v.coerce(f, raw[i*2+1], et))
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
					f.trap("%s", err)
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
					f.trap("%s has no field %s", tdName(td), name)
				}
			}
		} else {
			fts := v.fieldTypedefs(etd)
			for i := 0; i < n && i < len(s.Fields); i++ {
				var ft *runtime.TypeDef
				if i < len(fts) {
					ft = fts[i]
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
		if fn == name {
			var ft *runtime.TypeDef
			if fi < len(fts) {
				ft = fts[fi]
			}
			s.Fields[fi] = v.coerce(f, val, ft)
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

// channels — real blocking semantics. Channels are host `chan Value`s:
// sends and receives block exactly as in Go, buffer capacity is honored,
// and close wakes parked receivers. Every blocking op also selects on the
// owning process's done channel so a dead process releases goroutines
// parked in it (they unwind with procExit, which recover() cannot see).

var nilChanRV = reflect.ValueOf((chan runtime.Value)(nil))

// memberOf resolves a package member through MemberV so that a lazy
// package initializer triggered from inside this VM runs on THIS VM — a
// spawned goroutine's first touch of a package must not execute __init__
// on the engine's root VM.
func (v *VM) memberOf(p *runtime.Package, name string) (runtime.Value, error) {
	return p.MemberV(name, v.H.Materialize, v.runInit)
}

// runInit executes a package __init__ function as a nested call on this VM.
func (v *VM) runInit(fn *runtime.Function) error {
	_, err := v.call(fn, nil, nil, nil)
	return err
}

// chanOf resolves a channel value to its reflect channel plus the declared
// element typedef (send coercion / closed-receive zeros). Host channels
// boxed as *GoValue (e.g. `time.After`'s return) participate too.
func (v *VM) chanOf(f *frame, x runtime.Value) (reflect.Value, *runtime.TypeDef) {
	switch c := x.(type) {
	case *runtime.Chan:
		return reflect.ValueOf(c.C), v.elemTypedef(f, c.Typ)
	case *runtime.Cell:
		return v.chanOf(f, c.Elem)
	case *runtime.Named:
		return v.chanOf(f, c.V)
	case *runtime.IfaceNil:
		return v.chanOf(f, &runtime.TypedNil{Typ: c.Typ})
	case *runtime.TypedNil:
		if c.Typ != nil && c.Typ.Kind == runtime.KindChan {
			// a nil channel never becomes ready — it parks the op
			// (blocking forever at the root, as Go's deadlock does)
			return nilChanRV, v.elemTypedef(f, c.Typ)
		}
		f.trap("channel operation on %T", x)
	case *runtime.GoValue:
		if rv := reflect.ValueOf(c.V); rv.IsValid() && rv.Kind() == reflect.Chan {
			return rv, nil
		}
		f.trap("channel operation on non-channel host value %T", c.V)
	default:
		f.trap("channel operation on %T", x)
	}
	return reflect.Value{}, nil
}

// chanSendValue marshals a value sent on a channel: `chan any` carries
// the script value verbatim so a container keeps its identity on the
// receive side (`got["k"] = 8` writes the original map); concrete
// element types marshal like a host call argument.
func (v *VM) chanSendValue(val runtime.Value, elemT reflect.Type) (reflect.Value, error) {
	if elemT.Kind() == reflect.Interface && elemT.NumMethod() == 0 {
		out := reflect.New(elemT).Elem()
		if val != nil && val != runtime.NIL {
			out.Set(reflect.ValueOf(val))
		}
		return out, nil
	}
	return toReflectValue(val, elemT, v)
}

// chanSend sends sv on chRV, blocking as in Go — including panicking on a
// closed channel (the host panic surfaces as a script panic).
func (v *VM) chanSend(chRV, sv reflect.Value) {
	chosen, _, _ := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: chRV, Send: sv},
		{Dir: reflect.SelectRecv, Chan: v.doneRV()},
	})
	if chosen == 1 {
		panic(procExit{})
	}
}

// chanRecv receives one value from the channel denoted by chv, blocking
// as in Go: closed-and-empty reports (zero, false).
func (v *VM) chanRecv(f *frame, chv runtime.Value) (runtime.Value, bool) {
	chRV, et := v.chanOf(f, chv)
	return v.chanRecvRV(f, chRV, et)
}

// chanRecvRV is chanRecv on an already-resolved reflect channel — shared
// by OpRecv/OpRecvOK and channel-range iterators.
func (v *VM) chanRecvRV(f *frame, chRV reflect.Value, et *runtime.TypeDef) (runtime.Value, bool) {
	chosen, rv, open := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: chRV},
		{Dir: reflect.SelectRecv, Chan: v.doneRV()},
	})
	if chosen == 1 {
		panic(procExit{})
	}
	if !open {
		if et != nil {
			return v.zeroValue(f, et), false
		}
		return runtime.NIL, false
	}
	return goValueOf(rv), true
}

// chanZero is the OpSelWait payload for a receive arm: the received value,
// or the element type's zero when the channel was closed.
func (v *VM) chanZero(f *frame, a *runtime.SelArm, open bool, rv reflect.Value) runtime.Value {
	if open {
		return goValueOf(rv)
	}
	if a.ETyp != nil {
		return v.zeroValue(f, a.ETyp)
	}
	return runtime.NIL
}

// iterators

func (v *VM) newIterator(f *frame, coll runtime.Value) *runtime.Iterator {
	switch c := coll.(type) {
	case *runtime.Cell:
		return v.newIterator(f, c.Elem)
	case *runtime.Named:
		return v.newIterator(f, c.V)
	case *runtime.Slice:
		if c.N > 0 {
			// a virtual zero-size slice iterates its logical length,
			// vending the shared element value.
			return &runtime.Iterator{Kind: 's', Limit: int(c.N), Zero: c.Zero}
		}
		return &runtime.Iterator{Kind: 's', Elems: c.Elems}
	case *runtime.Map:
		// Snapshot the key ORDER only — Elems carries the display keys
		// and Keys their canonical forms — while pairs resolve live in
		// iterNext. Deleting the current or a reached key must not skip
		// the next (Delete shifts m.Order in place), and an entry
		// removed before its turn is not produced, like Go.
		return &runtime.Iterator{Kind: 'm', M: c, Elems: append([]runtime.Value(nil), c.Order...), Keys: append([]runtime.Value(nil), c.Keys...)}
	case *runtime.Chan:
		return &runtime.Iterator{Kind: 'c', ChRV: reflect.ValueOf(c.C), ETyp: v.elemTypedef(f, c.Typ)}
	case *runtime.GoValue:
		if rv := reflect.ValueOf(c.V); rv.IsValid() && rv.Kind() == reflect.Chan {
			return &runtime.Iterator{Kind: 'c', ChRV: rv}
		}
		f.trap("range over %T", coll)
		return nil
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		// iter.Seq/Seq2-style producer: the whole loop runs inside the
		// first OpRangeNext via yield — see driveFuncIter.
		return &runtime.Iterator{Kind: 'f', Fn: c}
	case int64:
		return &runtime.Iterator{Kind: 'i', Limit: int(c)}
	case string:
		return &runtime.Iterator{Kind: 'x', String: c}
	case *runtime.TypedNil, *runtime.IfaceNil:
		if tn, ok := coll.(*runtime.TypedNil); ok && tn.Typ != nil {
			if tn.Typ.Kind == runtime.KindChan {
				// a nil channel range blocks forever, as in Go
				return &runtime.Iterator{Kind: 'c', ChRV: nilChanRV, ETyp: v.elemTypedef(f, tn.Typ)}
			}
			// `for i := range p` over a nil *[N]T yields 0..N-1 — only the
			// element read would dereference the nil pointer.
			if at := runtime.PtrArrayType(tn.Typ); at != nil {
				if n, ok := v.arrayLen(f, &runtime.TypeDef{Anon: at, Pkg: tn.Typ.Pkg, File: tn.Typ.File}); ok {
					return &runtime.Iterator{Kind: 'i', Limit: int(n), NilArr: true}
				}
			}
		}
		// range over a nil slice/map iterates zero times
		return &runtime.Iterator{Kind: 's'}
	case nil, runtime.Nil:
		return &runtime.Iterator{Kind: 's'}
	default:
		f.trap("range over %T", coll)
		return nil
	}
}

// iterNext pushes nvars values (key/index, elem) and returns false when done.
// elemRead reports whether a non-blank element var binds the iteration
// value — a nil *[N]T iterator may yield indices without it, but a real
// element binding dereferences the nil pointer like Go does.
func (v *VM) iterNext(f *frame, it *runtime.Iterator, nvars int, elemRead bool) bool {
	push := func(key, val runtime.Value) {
		if nvars == 2 {
			f.push(key)
			f.push(val)
			return
		}
		f.push(key) // single-var range yields index/key
	}
	switch it.Kind {
	case 's':
		if it.Zero != nil {
			// a virtual zero-size-element slice: Limit counts the
			// elements and each pair vends the shared value.
			if it.Idx >= it.Limit {
				return false
			}
			push(int64(it.Idx), it.Zero)
			it.Idx++
			return true
		}
		if it.Idx >= len(it.Elems) {
			return false
		}
		push(int64(it.Idx), it.Elems[it.Idx])
		it.Idx++
		return true
	case 'm':
		for it.Idx < len(it.Keys) {
			ck, key := it.Keys[it.Idx], it.Elems[it.Idx]
			it.Idx++
			val, ok := it.M.Pairs[ck]
			if !ok {
				continue // removed before being reached: not produced
			}
			push(key, val)
			return true
		}
		return false
	case 'i':
		if it.Idx >= it.Limit {
			return false
		}
		if it.NilArr && nvars == 2 && elemRead {
			// `for i, v := range p` on a nil *[N]T reads p[i] — a nil
			// pointer dereference on the first iteration. `for i, _ :=`
			// binds nothing and iterates the static indices like Go.
			panic(runtime.NilDerefPanic())
		}
		push(int64(it.Idx), int64(it.Idx))
		it.Idx++
		return true
	case 'x':
		if it.Idx >= len(it.String) {
			return false
		}
		r, size := utf8.DecodeRuneInString(it.String[it.Idx:])
		push(int64(it.Idx), int64(r))
		it.Idx += size
		return true
	case 'c':
		// range over a channel receives until the channel closes, blocking
		// on each element as in Go — and like Go it allows at most one
		// iteration variable (there is no index/key).
		if nvars == 2 {
			f.trap("range over channel allows at most one iteration variable")
		}
		val, ok := v.chanRecvRV(f, it.ChRV, it.ETyp)
		if !ok {
			return false
		}
		push(val, val)
		return true
	}
	return false
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

// driveFuncIter runs a range-over-func loop (iter.Seq/Seq2 semantics) to
// completion: the producer function is invoked once with a yield builtin,
// and each yield call pushes its arguments as the loop values and re-runs
// the loop body inside the same frame — the push model of Go 1.23
// iterators on top of the pull-model VM, no coroutine needed.
//
// yield reports the body's exit back to the producer: it returns true
// when the body reached the loop's back-edge (next iteration), false when
// the body left the loop (break/goto/return), and panics when called
// again after that — matching the runtime panic for an iterator that
// continues past a false yield. A panic inside the body propagates
// through the producer's frames (its defers run, recover() may catch it)
// and then through the caller frame, as in Go.
func (v *VM) driveFuncIter(f *frame, it *runtime.Iterator, nvars, top, end int) {
	if it.Started {
		// The loop head was re-entered (e.g. a goto back into it); the
		// producer already ran to completion, so the loop is over.
		return
	}
	it.Started = true
	bodyStart := top + 1
	stackMark := len(f.stack)
	defer func() {
		// A panic recovered inside the producer can leave the body's
		// operand pushes behind — restore the pre-loop depth.
		f.stack = f.stack[:stackMark]
	}()
	yield := &runtime.BuiltinFunc{
		Name: "yield",
		Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if it.Exited {
				panic(runtime.RuntimePanic("range function continued iteration after function for loop body returned false"))
			}
			if nvars > 0 && len(args) < nvars {
				return nil, fmt.Errorf("yield must be called with %d argument(s), got %d", nvars, len(args))
			}
			// a producer may yield more values than the loop binds —
			// `for m := range seq2` keeps only the first, like Go.
			for i := 0; i < nvars && i < len(args); i++ {
				f.push(args[i])
			}
			completed := false
			defer func() {
				if !completed {
					it.Exited = true // body died on panic — loop is over
				}
			}()
			f.ip = bodyStart
			v.runBounded(f, top, end)
			completed = true
			if f.ip == top {
				return true, nil // back-edge reached: next iteration
			}
			it.Exited = true
			return false, nil // body left the loop: producer must stop
		},
	}
	if _, err := v.call(it.Fn, []runtime.Value{yield}, nil, nil); err != nil {
		panic(&runtime.Trap{Pos: f.pos(), Reason: err.Error(), Err: err})
	}
}

// arithmetic

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
		return truthy(x.V)
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

// materialize turns an untyped constant into its default-typed value at
// a value boundary ('a' -> rune, `1<<100` -> trap like Go's compile-time
// "constant overflows int"); any other value passes through.
func materialize(f *frame, x runtime.Value) runtime.Value {
	u, ok := x.(*runtime.UConst)
	if !ok {
		return x
	}
	r, err := materializeDefault(u)
	if err != nil {
		f.trap("%s", err)
	}
	return r
}

// materializeDefault converts an untyped constant to its Go default
// type: bool, string, rune->int32, int, float64, or complex128.
func materializeDefault(u *runtime.UConst) (runtime.Value, error) {
	switch u.V.Kind() {
	case constant.Bool:
		return constant.BoolVal(u.V), nil
	case constant.String:
		return constant.StringVal(u.V), nil
	case constant.Int:
		if u.Rune {
			if i, ok := constant.Int64Val(u.V); ok {
				return runtime.Tag(&runtime.TypeDef{Name: "rune", Kind: runtime.KindNamedBasic}, i), nil
			}
			return nil, fmt.Errorf("constant %s overflows rune", u.V)
		}
		if i, ok := constant.Int64Val(u.V); ok {
			return i, nil
		}
		if uv, ok := constant.Uint64Val(u.V); ok && uv <= math.MaxInt64 {
			return int64(uv), nil
		}
		return nil, fmt.Errorf("constant %s overflows int", u.V)
	case constant.Float:
		fv, _ := constant.Float64Val(u.V)
		if math.IsInf(fv, 0) {
			return nil, fmt.Errorf("constant %s overflows float64", u.V)
		}
		return runtime.CanonConstZero(fv), nil
	case constant.Complex:
		cv := constComplexVal(u.V)
		return &runtime.GoValue{V: cv}, nil
	}
	return nil, fmt.Errorf("cannot materialize constant %s", u.V)
}

// materializeConst converts an untyped constant for a declared target —
// Go's representability check: `const B = 1<<100` is legal but
// `var x int = B` fails with "constant ... overflows int".
func (v *VM) materializeConst(f *frame, u *runtime.UConst, td *runtime.TypeDef) runtime.Value {
	r, err := v.materializeConstErr(u, td)
	if err != nil {
		f.trap("%s", err)
	}
	return r
}

func (v *VM) materializeConstErr(u *runtime.UConst, td *runtime.TypeDef) (runtime.Value, error) {
	utd := v.peelNamed(td)
	if utd == nil || utd.Kind == runtime.KindInterface {
		// `any`/`error` slots take the constant's default type.
		return materializeDefault(u)
	}
	name := basicNameOf(utd)
	var x runtime.Value
	switch name {
	case "int", "int8", "int16", "int32", "int64", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		// an integer-valued float constant is int-assignable (`var i
		// int = 1e3`); a fractional one is Go's "cannot use 1.5 as int".
		if u.V.Kind() == constant.Float {
			if _, ok := toIntConst(u.V); !ok {
				return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
			}
		}
		if u.V.Kind() != constant.Int && u.V.Kind() != constant.Float {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		i, ok := fitsIntConst(u.V, name)
		if !ok {
			return nil, fmt.Errorf("constant %s overflows %s", u.V, name)
		}
		x = i
	case "float32":
		fv, ok := constFloat(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		f32, _ := constant.Float32Val(u.V)
		if math.IsInf(float64(f32), 0) || math.IsInf(fv, 0) {
			return nil, fmt.Errorf("constant %s overflows float32", u.V)
		}
		x = float64(runtime.CanonConstZero(float64(f32)))
	case "float64":
		fv, ok := constFloat(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		if math.IsInf(fv, 0) {
			return nil, fmt.Errorf("constant %s overflows float64", u.V)
		}
		x = fv
	case "complex64":
		cv, ok := constComplex(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		x = &runtime.GoValue{V: complex64(cv)}
	case "complex128":
		cv, ok := constComplex(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		x = &runtime.GoValue{V: cv}
	case "string":
		switch u.V.Kind() {
		case constant.String:
			x = constant.StringVal(u.V)
		case constant.Int:
			// int-to-string produces the rune (Go vet would flag it, the
			// conversion itself is legal).
			i, _ := constant.Int64Val(u.V)
			x = string(rune(i))
		default:
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
	case "bool":
		if u.V.Kind() != constant.Bool {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		x = constant.BoolVal(u.V)
	default:
		// not a basic-name target — the constant converts through its
		// default type first: []byte("s") is []byte(string).
		dx, err := materializeDefault(u)
		if err != nil {
			return nil, err
		}
		return v.convert(td, dx)
	}
	// the tag rule from coerceConcrete applies equally: a converted const
	// keeps the declared name (and int64/float32/complex64 tag even
	// spelled bare).
	if td != nil && (declaredType(td) || sizedIntName(td.Name) || td.Name == "int64" ||
		td.Name == "float32" || td.Name == "complex64") {
		return runtime.Tag(td, x), nil
	}
	return x, nil
}

// constFloat reads a numeric constant as float64; a complex constant
// with an imaginary part is not a float.
func constFloat(cv constant.Value) (float64, bool) {
	switch cv.Kind() {
	case constant.Int, constant.Float:
	default:
		return 0, false
	}
	// Float64Val's second result is exactness, not representability —
	// Go rounds an exact-but-wide constant into the type (`float64 =
	// 1e100 >> 1000` compiles, losing precision). Overflow instead
	// reports through IsInf at the caller.
	f, _ := constant.Float64Val(cv)
	return runtime.CanonConstZero(f), true
}

func constComplex(cv constant.Value) (complex128, bool) {
	switch cv.Kind() {
	case constant.Int, constant.Float, constant.Complex:
	default:
		return 0, false
	}
	return constComplexVal(cv), true
}

// constComplexVal reads a numeric constant's real/imag parts at
// float64 precision — the same conversion materialization performs.
func constComplexVal(cv constant.Value) complex128 {
	re, _ := constant.Float64Val(constant.Real(cv))
	im, _ := constant.Float64Val(constant.Imag(cv))
	return complex(runtime.CanonConstZero(re), runtime.CanonConstZero(im))
}

// toIntConst converts an integer-valued Float constant to Int kind —
// go/constant's ToInt panics on a fractional input, so it returns ok
// instead.
func toIntConst(cv constant.Value) (i constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			i, ok = nil, false
		}
	}()
	return constant.ToInt(cv), true
}

// fitsIntConst reports whether a constant is representable as the
// named Go int type — Go's constant-to-type conversion check; an
// integer-valued Float constant is int-convertible.
func fitsIntConst(cv constant.Value, name string) (int64, bool) {
	if cv.Kind() == constant.Float {
		ti, ok := toIntConst(cv)
		if !ok {
			return 0, false
		}
		cv = ti
	}
	i, iok := constant.Int64Val(cv)
	switch name {
	case "int", "int64":
		return i, iok
	case "int8":
		return i, iok && i >= -128 && i <= 127
	case "int16":
		return i, iok && i >= -32768 && i <= 32767
	case "int32", "rune":
		return i, iok && i >= -2147483648 && i <= 2147483647
	case "uint", "uint64", "uintptr":
		if iok {
			return i, i >= 0
		}
		u, uok := constant.Uint64Val(cv)
		return int64(u), uok
	case "uint8", "byte":
		return i, iok && i >= 0 && i <= 255
	case "uint16":
		return i, iok && i >= 0 && i <= 65535
	case "uint32":
		return i, iok && i >= 0 && i <= 4294967295
	}
	return 0, false
}

// constBinary folds an op over two untyped constants in the go/constant
// domain. ok is false when the op isn't constant-foldable (&& || keep
// runtime short-circuit semantics) or constant evaluation itself
// rejected the operands — the caller then materializes and applies
// runtime semantics.
func constBinary(op bytecode.BinOp, ua, ub *runtime.UConst) (res runtime.Value, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := binOpToken(op)
	if !ok {
		return nil, false
	}
	switch tok {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		// the stack holds runtime values, not constant.Value — a
		// constant.MakeBool here reads as a non-bool truthy in
		// `if x != c` (default case → always true).
		return constant.Compare(ua.V, tok, ub.V), true
	case token.SHL, token.SHR:
		lv := ua.V
		if lv.Kind() == constant.Float {
			// `1e100 >> 1000` is a valid untyped constant expression:
			// the float constant is integer-valued, so the shift
			// computes in the exact integer domain.
			lv = constant.ToInt(lv)
		}
		rv := ub.V
		if rv.Kind() == constant.Float {
			rv = constant.ToInt(rv)
		}
		s, ok := constant.Uint64Val(rv)
		if !ok {
			return nil, false
		}
		return &runtime.UConst{V: constant.Shift(lv, tok, uint(s)), Rune: ua.Rune || ub.Rune}, true
	case token.QUO:
		if ua.V.Kind() == constant.Int && ub.V.Kind() == constant.Int {
			// integer constants divide truncated (7/2 is 3), like
			// constValue's QUO_ASSIGN rule.
			return &runtime.UConst{V: constant.BinaryOp(ua.V, token.QUO_ASSIGN, ub.V), Rune: ua.Rune || ub.Rune}, true
		}
	}
	return &runtime.UConst{V: constant.BinaryOp(ua.V, tok, ub.V), Rune: ua.Rune || ub.Rune}, true
}

// binOpToken maps a bytecode binary op to its go/token spelling —
// inverse of compile's binOpOf, for constant-domain evaluation.
func binOpToken(op bytecode.BinOp) (token.Token, bool) {
	switch op {
	case bytecode.BinAdd:
		return token.ADD, true
	case bytecode.BinSub:
		return token.SUB, true
	case bytecode.BinMul:
		return token.MUL, true
	case bytecode.BinQuo:
		return token.QUO, true
	case bytecode.BinRem:
		return token.REM, true
	case bytecode.BinAnd:
		return token.AND, true
	case bytecode.BinOr:
		return token.OR, true
	case bytecode.BinXor:
		return token.XOR, true
	case bytecode.BinAndNot:
		return token.AND_NOT, true
	case bytecode.BinShl:
		return token.SHL, true
	case bytecode.BinShr:
		return token.SHR, true
	case bytecode.BinEql:
		return token.EQL, true
	case bytecode.BinNeq:
		return token.NEQ, true
	case bytecode.BinLss:
		return token.LSS, true
	case bytecode.BinLeq:
		return token.LEQ, true
	case bytecode.BinGtr:
		return token.GTR, true
	case bytecode.BinGeq:
		return token.GEQ, true
	}
	return 0, false
}

// asComplex reads a value in the complex domain: a boxed complex of
// either width, or a bare numeric operand that promotes to complex128.
// width is 64 for a complex64 value, else 128.
func asComplex(x runtime.Value) (cv complex128, width int, ok bool) {
	if n, isN := x.(*runtime.Named); isN {
		x = n.V
	}
	if v, isG := x.(*runtime.GoValue); isG {
		switch c := v.V.(type) {
		case complex64:
			return complex128(c), 64, true
		case complex128:
			return c, 128, true
		}
	}
	return 0, 0, false
}

// complexOperand reads a value into the complex domain for arithmetic:
// a real complex value keeps its width; a bare int64/float64 promotes
// with width 0 — untyped, so it joins either side (`c64 + 1` works).
func complexOperand(x runtime.Value) (complex128, int, bool) {
	if cv, w, ok := asComplex(x); ok {
		return cv, w, true
	}
	switch v := x.(type) {
	case int64:
		return complex(float64(v), 0), 0, true
	case float64:
		return complex(v, 0), 0, true
	}
	return 0, 0, false
}

// complexResult boxes a complex result at the operand's width — the
// narrowest side wins like Go's complex64/complex128 promotion rules,
// and a promoted constant (width 0) takes the other side's width.
func complexResult(cv complex128, wa, wb int) runtime.Value {
	if wa == 64 || wb == 64 {
		return &runtime.GoValue{V: complex64(cv)}
	}
	return &runtime.GoValue{V: cv}
}

// constOf lifts a value back into the constant domain: a UConst is
// already there; a bare literal value re-wraps as one. A Named value
// stays out — it is typed, not a constant. A GoValue boxing a basic
// numeric (folded literals wider than int64 land there) lifts too.
func constOf(x runtime.Value) (*runtime.UConst, bool) {
	switch v := x.(type) {
	case *runtime.UConst:
		return v, true
	case int64:
		return &runtime.UConst{V: constant.MakeInt64(v)}, true
	case float64:
		return &runtime.UConst{V: constant.MakeFloat64(v)}, true
	case string:
		return &runtime.UConst{V: constant.MakeString(v)}, true
	case bool:
		return &runtime.UConst{V: constant.MakeBool(v)}, true
	case *runtime.GoValue:
		switch n := v.V.(type) {
		case int:
			return &runtime.UConst{V: constant.MakeInt64(int64(n))}, true
		case int64:
			return &runtime.UConst{V: constant.MakeInt64(n)}, true
		case uint64:
			return &runtime.UConst{V: constant.MakeUint64(n)}, true
		case float64:
			return &runtime.UConst{V: constant.MakeFloat64(n)}, true
		case string:
			return &runtime.UConst{V: constant.MakeString(n)}, true
		case bool:
			return &runtime.UConst{V: constant.MakeBool(n)}, true
		}
	}
	return nil, false
}

// isPlainConst reports whether x reads as a compile-time constant for
// mixed folding — bare numerics, strings, and GoValue-boxed basic
// numerics do; typed (Named) values and everything else do not.
func isPlainConst(x runtime.Value) bool {
	switch x := x.(type) {
	case int64, float64, string, bool:
		return true
	case *runtime.GoValue:
		switch x.V.(type) {
		case int, int64, uint64, float64, string, bool:
			return true
		}
	}
	return false
}

// isCompareOp reports whether the op is a value comparison —
// constants in those adopt the operand's type instead of folding.
func isCompareOp(op bytecode.BinOp) bool {
	switch op {
	case bytecode.BinEql, bytecode.BinNeq, bytecode.BinLss,
		bytecode.BinLeq, bytecode.BinGtr, bytecode.BinGeq:
		return true
	}
	return false
}

// scalarConst converts an untyped constant to the runtime type of a
// scalar operand — comparisons first convert, then compare values
// (`f float64 == hugeconst` rounds the constant, not the operand).
func scalarConst(u *runtime.UConst, b runtime.Value) (runtime.Value, bool) {
	switch b.(type) {
	case int64:
		i, ok := fitsIntConst(u.V, "int")
		if !ok {
			return nil, false
		}
		return i, true
	case float64:
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		return fv, true
	case string:
		if u.V.Kind() != constant.String {
			return nil, false
		}
		return constant.StringVal(u.V), true
	case bool:
		if u.V.Kind() != constant.Bool {
			return nil, false
		}
		return constant.BoolVal(u.V), true
	}
	if g, ok := b.(*runtime.GoValue); ok {
		switch g.V.(type) {
		case int, int64:
			i, ok := fitsIntConst(u.V, "int64")
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: i}, true
		case uint64:
			i, ok := fitsIntConst(u.V, "uint64")
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: uint64(i)}, true
		case float64:
			fv, ok := constFloat(u.V)
			if !ok || math.IsInf(fv, 0) {
				return nil, false
			}
			return &runtime.GoValue{V: fv}, true
		case complex64:
			cv, ok := constComplex(u.V)
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: complex64(cv)}, true
		case complex128:
			cv, ok := constComplex(u.V)
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: cv}, true
		}
	}
	return nil, false
}

// adaptConst materializes an untyped constant operand for a binary op.
// When the other operand is a named numeric, the const adopts ITS type
// (Go spec: `x op y` where x is an untyped constant representable as
// T(y) converts x to T(y) — `'a' + uint16var` is uint16 arithmetic);
// otherwise it takes its default type. An unconvertible const keeps
// the default materialization so the mismatch trap reports like Go's
// compile error.
func adaptConst(f *frame, u *runtime.UConst, other runtime.Value) runtime.Value {
	if nb, ok := other.(*runtime.Named); ok {
		if r, ok2 := constToBasic(u, basicNameOf(nb.Typ)); ok2 {
			return runtime.Tag(nb.Typ, r)
		}
	}
	return materialize(f, u)
}

// constToBasic converts a constant to a builtin numeric value by name —
// the operand-type adoption rule's conversion half.
func constToBasic(u *runtime.UConst, name string) (runtime.Value, bool) {
	switch {
	case sizedIntName(name) || name == "int" || name == "int64":
		i, ok := fitsIntConst(u.V, name)
		if !ok {
			return nil, false
		}
		return i, true
	case name == "float32":
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		return float64(float32(fv)), true
	case name == "float64":
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		return fv, true
	}
	return nil, false
}

// ifaceEql is the strict (type, value) pair equality an interface-typed
// switch tag uses: `case 1:` materializes to int and never matches an
// any(float64(1.0)) tag — plain BinEql would coerce it equal. A named
// tag only pairs with the same named type; the nil-ish operands keep
// the interface nil rules from eqlValue.
func ifaceEql(f *frame, a, b runtime.Value) bool {
	a, b = materialize(f, a), materialize(f, b)
	an, aNamed := a.(*runtime.Named)
	bn, bNamed := b.(*runtime.Named)
	if aNamed != bNamed {
		return false
	}
	if aNamed {
		if !sameTypeDef(an.Typ, bn.Typ) {
			return false
		}
		a, b = an.V, bn.V
	}
	switch a.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return eqlValue(a, b)
	}
	switch b.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return eqlValue(a, b)
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	return eqlValue(a, b)
}

func binaryOp(f *frame, op bytecode.BinOp, a, b runtime.Value) runtime.Value {
	// an interface-typed switch tag compares pairs, not coerced values.
	if op == bytecode.BinEqlIface {
		return ifaceEql(f, a, b)
	}
	// untyped constants fold in the arbitrary-precision constant domain
	// while both sides read as constants — a bare int64/float64 operand
	// came from a folded literal and can lift back (`const C = B - 1<<99`
	// computes exactly). With a real value a constant materializes to
	// its default type instead (and can fail to, like Go's compile-time
	// "constant overflows int"). The exception is comparisons: Go
	// converts the constant to the operand's type and compares the
	// resulting values, so `f == 1e100-1` rounds the constant into
	// float64 where an exact-domain compare would report false.
	if ua, isA := a.(*runtime.UConst); isA {
		resolved := false
		if _, isB := b.(*runtime.UConst); isB || isPlainConst(b) {
			switch {
			case !isB && isCompareOp(op):
				if s, ok := scalarConst(ua, b); ok {
					a, resolved = s, true
				}
			case op == bytecode.BinShl || op == bytecode.BinShr:
				// a shift is never constant-folded when one side is a
				// runtime value — shiftOp reads a constant count as the
				// unsigned integer Go requires (`x << (math.MaxUint+0)`),
				// so the operand keeps its UConst form.
				resolved = true
			default:
				ca, _ := constOf(a)
				cb, _ := constOf(b)
				if r, ok := constBinary(op, ca, cb); ok {
					return r
				}
			}
		}
		if !resolved {
			a = adaptConst(f, ua, b)
		}
	}
	if ub, ok := b.(*runtime.UConst); ok {
		resolved := false
		switch {
		case op == bytecode.BinShl || op == bytecode.BinShr:
			// a shift is never constant-folded when one side is a
			// runtime value — shiftOp reads a constant count as the
			// unsigned integer Go requires (`x << (math.MaxUint+0)`),
			// whatever the left operand's shape.
			resolved = true
		case isPlainConst(a):
			if isCompareOp(op) {
				if s, ok := scalarConst(ub, a); ok {
					b, resolved = s, true
				}
			} else {
				ca, _ := constOf(a)
				cb, _ := constOf(b)
				if r, ok2 := constBinary(op, ca, cb); ok2 {
					return r
				}
			}
		}
		if !resolved {
			b = adaptConst(f, ub, a)
		}
	}
	// shifts evaluate in the left operand's signedness — Go types the
	// result by the left side alone, so `^uintptr(0) >> 63` must shift
	// logically, not as int64. They get their own operator.
	if op == bytecode.BinShl || op == bytecode.BinShr {
		return shiftOp(f, op, a, b)
	}
	// named basic values operate on their underlying value; two different
	// declared types in one operation is a type error (Go: `x + y` on
	// MyInt and Other traps), and an arithmetic result keeps the
	// operand's declared tag — comparisons produce an untyped bool, which
	// stays bare. The unwrap happens before the complex branch so a
	// declared complex type is checked too.
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag = n.Typ
		a = n.V
	}
	if n, ok := b.(*runtime.Named); ok {
		if tag != nil && !sameTypeDef(tag, n.Typ) {
			// equality is lawful between differently-typed dynamic
			// values — `any(u64) != any(u32)` is true in Go; the
			// ordered ops and arithmetic stay a type error.
			if isCompareOp(op) && (op == bytecode.BinEql || op == bytecode.BinNeq) {
				return op == bytecode.BinNeq
			}
			f.trap("invalid operation: mismatched types %s and %s", tdName(tag), tdName(n.Typ))
		}
		tag = n.Typ
		b = n.V
	}
	// complex values operate in the complex domain: complex64 wins over
	// complex128, ordered comparisons are a compile reject in Go.
	_, _, aIs := asComplex(a)
	_, _, bIs := asComplex(b)
	if aIs || bIs {
		ca, wa, aok := complexOperand(a)
		cb, wb, bok := complexOperand(b)
		if aok && bok {
			if wa != 0 && wb != 0 && wa != wb {
				f.trap("invalid operation: %s (mismatched types complex%d and complex%d)", op, wa, wb)
			}
			var r runtime.Value
			switch op {
			case bytecode.BinAdd:
				r = complexResult(ca+cb, wa, wb)
			case bytecode.BinSub:
				r = complexResult(ca-cb, wa, wb)
			case bytecode.BinMul:
				r = complexResult(ca*cb, wa, wb)
			case bytecode.BinQuo:
				r = complexResult(ca/cb, wa, wb)
			case bytecode.BinEql:
				r = ca == cb
			case bytecode.BinNeq:
				r = ca != cb
			default:
				f.trap("invalid operation: %s (complex numbers are not ordered)", op)
			}
			if tag != nil {
				if _, isBool := r.(bool); !isBool {
					return runtime.Tag(tag, r)
				}
			}
			return r
		}
	}
	if tag != nil {
		// an unsigned-width declared int evaluates `/`, `%` and ordered
		// comparisons in the uint64 domain — same bits as int64 for the
		// rest, so only those ops differ.
		if uname := sizedNameOf(tag); unsignedName(uname) {
			if ua, aok := uintOperand(a); aok {
				if ub, bok := uintOperand(b); bok {
					res, isInt := uintBinOp(f, op, ua, ub)
					if !isInt {
						return res
					}
					return runtime.Tag(tag, maskInt(int64(res.(uint64)), uname))
				}
			}
		}
		res := binaryOp(f, op, a, b)
		if iv, ok := res.(int64); ok {
			return runtime.Tag(tag, maskInt(iv, sizedNameOf(tag)))
		}
		if fv, ok := res.(float64); ok {
			// a float32-flavored tag narrows the result the way an
			// assignment into a float32 slot does.
			if basicNameOf(tag) == "float32" {
				fv = float64(float32(fv))
			}
			return runtime.Tag(tag, fv)
		}
		if _, ok := res.(string); ok {
			return runtime.Tag(tag, res)
		}
		return res
	}
	// equality works on any comparable pair and must see the boxed
	// dynamic types — unwrapping a host numeric here would turn
	// `any(time.Weekday(4)) == any(int(4))` into a true int64 compare
	// where Go reports false.
	switch op {
	case bytecode.BinEql:
		return eqlValue(a, b)
	case bytecode.BinNeq:
		return !eqlValue(a, b)
	}
	// bound time.* constants and reflect-produced durations arrive as
	// raw time.Duration values — they behave as their int64 underlying
	// in arithmetic and comparisons (2*time.Second, d < timeout), and
	// a result on a duration operand stays a duration (except d/d,
	// which is unitless like Go).
	var dmark, dboth bool
	if d, ok := a.(time.Duration); ok {
		a = int64(d)
		dmark = true
	}
	if d, ok := b.(time.Duration); ok {
		b = int64(d)
		if dmark {
			dboth = true
		}
		dmark = true
	}
	// uint64 literals too wide for int64 stay boxed as GoValue;
	// arithmetic on them computes mod 2^64 in int64 (same bits) and
	// re-boxes so formatting keeps the unsigned domain.
	var ubox bool
	// numTag remembers the named host int an operand was unwrapped from
	// (reflect.Kind and friends) so an arithmetic result can be re-boxed
	// into the same type — `reflect.Int + 1` stays a reflect.Kind.
	var numTag reflect.Type
	if g, ok := a.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			a = int64(u)
			ubox = true
		} else if iv, ok := smallIntOf(g.V); ok {
			// a GoValue carrying an ordered numeric (reflect.Kind and
			// other named host ints) unwraps for arithmetic and
			// comparisons like an ordinary named int.
			a = iv
			numTag = reflect.TypeOf(g.V)
		}
	}
	if g, ok := b.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			b = int64(u)
			ubox = true
		} else if iv, ok := smallIntOf(g.V); ok {
			b = iv
			if bt := reflect.TypeOf(g.V); numTag != nil && bt != numTag {
				// Go rejects arithmetic on differently-named ints
				// (reflect.Kind + time.Weekday) at compile time.
				f.trap("invalid operation: mismatched types %s and %s", numTag, bt)
			} else {
				numTag = bt
			}
		}
	}
	if s, ok := a.(string); ok {
		return stringBinOp(f, op, s, b)
	}
	if isFloat(a) || isFloat(b) {
		return floatBinOp(f, op, toFloat(a), toFloat(b))
	}
	if ai, ok := a.(int64); ok {
		bi, ok := b.(int64)
		if !ok {
			f.trap("unsupported types: %T %s %T", a, op, b)
		}
		if ubox {
			res, isInt := uintBinOp(f, op, uint64(ai), uint64(bi))
			if isInt {
				return &runtime.GoValue{V: res}
			}
			return res
		}
		res := intBinOp(f, op, ai, bi)
		if iv, isI := res.(int64); isI {
			switch {
			case dmark && op == bytecode.BinQuo && dboth:
				return iv // d/d is unitless in Go
			case dmark:
				return time.Duration(iv)
			case numTag != nil:
				// re-box into the unwrapped operand's named host type —
				// `reflect.Int + 1` is a reflect.Kind, not an int.
				rv := reflect.New(numTag).Elem()
				switch numTag.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					rv.SetInt(iv)
				default:
					rv.SetUint(uint64(iv))
				}
				return &runtime.GoValue{V: rv.Interface()}
			}
		}
		return res
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		if !ok {
			f.trap("unsupported types: %T %s %T", a, op, b)
		}
		switch op {
		case bytecode.BinLAnd:
			return ab && bb
		case bytecode.BinLOr:
			return ab || bb
		}
	}
	f.trap("unsupported types: %T %s %T", a, op, b)
	return nil
}

// uintOperand reads an int-domain runtime value as uint64: int64s carry
// two's-complement bits already and GoValue{uint64} holds the wide form.
func uintOperand(v runtime.Value) (uint64, bool) {
	switch x := v.(type) {
	case int64:
		return uint64(x), true
	case *runtime.GoValue:
		if u, ok := x.V.(uint64); ok {
			return u, true
		}
	}
	return 0, false
}

// smallIntOf reads a host numeric that fits the int64 domain — named
// integer kinds like reflect.Kind travel as GoValue and need unwrapping
// before arithmetic/comparison (full-width uint64 stays boxed instead).
func smallIntOf(x any) (int64, bool) {
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uintptr:
		if u := rv.Uint(); u <= math.MaxInt64 {
			return int64(u), true
		}
	}
	return 0, false
}

// uintBinOp is intBinOp evaluated in the uint64 domain: only `/`, `%` and
// the ordered comparisons differ from the signed reading — bit ops and
// add/sub/mul produce identical bits. Integer results arrive as uint64.
func uintBinOp(f *frame, op bytecode.BinOp, a, b uint64) (res runtime.Value, isInt bool) {
	switch op {
	case bytecode.BinAdd:
		return a + b, true
	case bytecode.BinSub:
		return a - b, true
	case bytecode.BinMul:
		return a * b, true
	case bytecode.BinQuo:
		return a / b, true
	case bytecode.BinRem:
		return a % b, true
	case bytecode.BinAnd:
		return a & b, true
	case bytecode.BinOr:
		return a | b, true
	case bytecode.BinXor:
		return a ^ b, true
	case bytecode.BinAndNot:
		return a &^ b, true
	case bytecode.BinLss:
		return a < b, false
	case bytecode.BinLeq:
		return a <= b, false
	case bytecode.BinGtr:
		return a > b, false
	case bytecode.BinGeq:
		return a >= b, false
	case bytecode.BinEql:
		return a == b, false
	case bytecode.BinNeq:
		return a != b, false
	}
	f.trap("uint binary %s", op)
	return nil, false
}

// constShift folds a constant shift; go/constant panics past its
// representable range, which the caller treats as "didn't fold".
func constShift(lv constant.Value, op bytecode.BinOp, count uint64) (res constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := binOpToken(op)
	if !ok {
		return nil, false
	}
	return constant.Shift(lv, tok, uint(count)), true
}

// shiftOp evaluates << and >> in the left operand's signedness: Go types
// the result by the left side alone (the count is always an unsigned
// count), so a uintptr/uint64 value shifts logically while int64 shifts
// arithmetically. A declared-width operand re-tags and re-masks.
func shiftOp(f *frame, op bytecode.BinOp, a, b runtime.Value) runtime.Value {
	var count uint64
	var countOK bool
	if uc, isU := b.(*runtime.UConst); isU {
		// an untyped constant count is legal whenever it is
		// representable as an unsigned integer (`x << (M+0)` with M =
		// math.MaxUint reads as uint64), even past int64 —
		// materializeDefault would overflow-report where Go does not.
		iv := uc.V
		if iv.Kind() != constant.Int {
			// integer-valued Float/Complex constants (`1.`, `1+0i`)
			// count too; non-integral ones fall through to the
			// materialized-count path, which rejects them like Go.
			if ti, ok := toIntConst(iv); ok {
				iv = ti
			}
		}
		if iv.Kind() == constant.Int {
			if s, ok := constant.Uint64Val(iv); ok {
				count, countOK = s, true
			}
		}
	}
	if !countOK {
		b = materialize(f, b)
		count, countOK = shiftCount(b)
	}
	if !countOK {
		f.trap("unsupported shift count %T", b)
	}
	if uc, isU := a.(*runtime.UConst); isU {
		// both sides in the constant domain: Go folds the shift —
		// `const c1 = chuge >> 100` (chuge = 1<<100) is the constant
		// 1. A float/complex LHS reads as its integer value; one that
		// is not integral falls through and materializes.
		lv := uc.V
		if lv.Kind() != constant.Int {
			lv, _ = toIntConst(lv)
		}
		if lv != nil {
			if res, ok := constShift(lv, op, count); ok {
				return &runtime.UConst{V: res, Rune: uc.Rune}
			}
		}
	}
	a = materialize(f, a)
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag, a = n.Typ, n.V
	}
	unsigned := unsignedName(sizedNameOf(tag))
	switch x := a.(type) {
	case int64:
		var r int64
		if unsigned {
			r = int64(shiftUint(op, uint64(x), count))
		} else {
			r = shiftInt(op, x, count)
		}
		if tag != nil {
			return runtime.Tag(tag, maskInt(r, sizedNameOf(tag)))
		}
		return r
	case time.Duration:
		return time.Duration(shiftInt(op, int64(x), count))
	case float64:
		// an untyped constant left operand of a non-constant shift
		// takes the integer interpretation (`x<<(1.<<x)` is int
		// arithmetic in Go); a genuine float64 operand reaching here
		// is a program Go rejects anyway.
		if x == math.Trunc(x) {
			if x >= math.MaxInt64 {
				x = math.MaxInt64
			} else if x <= math.MinInt64 {
				x = math.MinInt64
			}
			return shiftInt(op, int64(x), count)
		}
	case *runtime.GoValue:
		if c, isC := x.V.(complex128); isC {
			re, im := real(c), imag(c)
			if im == 0 && re == math.Trunc(re) {
				if re >= math.MaxInt64 {
					re = math.MaxInt64
				} else if re <= math.MinInt64 {
					re = math.MinInt64
				}
				return shiftInt(op, int64(re), count)
			}
		}
		if u, ok := x.V.(uint64); ok {
			r := shiftUint(op, u, count)
			if tag != nil {
				return runtime.Tag(tag, maskInt(int64(r), sizedNameOf(tag)))
			}
			return &runtime.GoValue{V: r}
		}
	}
	f.trap("unsupported types: %T %s %T", a, op, b)
	return nil
}

// shiftCount reads the right operand of a shift as an unsigned count —
// a negative count panics like Go's runtime error. A Named unsigned tag
// reinterprets the int64 bits (`x << uint(-4)` shifts by 2^64-4, not -4).
func shiftCount(b runtime.Value) (uint64, bool) {
	if n, ok := b.(*runtime.Named); ok {
		if unsignedName(sizedNameOf(n.Typ)) {
			if iv, ok := n.V.(int64); ok {
				return uint64(iv), true
			}
		}
	}
	switch x := runtime.Unwrap(b).(type) {
	case int64:
		if x < 0 {
			panic(runtime.NegativeShiftPanic())
		}
		return uint64(x), true
	case float64:
		// an untyped float constant is a legal count when it is
		// integral (`x << 1.`); a non-integral or negative one is a
		// compile-time rejection in Go, so it keeps trapping here.
		if x != math.Trunc(x) {
			return 0, false
		}
		if x < 0 {
			panic(runtime.NegativeShiftPanic())
		}
		if x >= math.MaxUint64 {
			return math.MaxUint64, true
		}
		return uint64(x), true
	case *runtime.GoValue:
		switch u := x.V.(type) {
		case uint64:
			return u, true
		case uint:
			return uint64(u), true
		case uint8, uint16, uint32, uintptr:
			return reflect.ValueOf(u).Uint(), true
		case complex64:
			return complexShiftCount(complex128(u))
		case complex128:
			return complexShiftCount(u)
		}
	}
	return 0, false
}

// complexShiftCount reads a complex constant as a shift count: a zero
// imaginary part and integral real part make `x << (1+0i)` legal, like
// an integral float constant.
func complexShiftCount(c complex128) (uint64, bool) {
	re, im := real(c), imag(c)
	if im != 0 || re != math.Trunc(re) {
		return 0, false
	}
	if re < 0 {
		panic(runtime.NegativeShiftPanic())
	}
	if re >= math.MaxUint64 {
		return math.MaxUint64, true
	}
	return uint64(re), true
}

// shiftInt shifts int64 with Go's saturation: `x << c` loses bits past
// 64 (zero fill), `x >> c` sign-fills for a negative left operand.
func shiftInt(op bytecode.BinOp, a int64, c uint64) int64 {
	if c >= 64 {
		if op == bytecode.BinShl || a >= 0 {
			return 0
		}
		return -1
	}
	if op == bytecode.BinShl {
		return a << c
	}
	return a >> c
}

func shiftUint(op bytecode.BinOp, a uint64, c uint64) uint64 {
	if c >= 64 {
		return 0
	}
	if op == bytecode.BinShl {
		return a << c
	}
	return a >> c
}

func intBinOp(f *frame, op bytecode.BinOp, a, b int64) runtime.Value {
	switch op {
	case bytecode.BinAdd:
		return a + b
	case bytecode.BinSub:
		return a - b
	case bytecode.BinMul:
		return a * b
	case bytecode.BinQuo:
		return a / b
	case bytecode.BinRem:
		return a % b
	case bytecode.BinAnd:
		return a & b
	case bytecode.BinOr:
		return a | b
	case bytecode.BinXor:
		return a ^ b
	case bytecode.BinAndNot:
		return a &^ b
	case bytecode.BinLss:
		return a < b
	case bytecode.BinLeq:
		return a <= b
	case bytecode.BinGtr:
		return a > b
	case bytecode.BinGeq:
		return a >= b
	}
	f.trap("int binary %s", op)
	return nil
}

func floatBinOp(f *frame, op bytecode.BinOp, a, b float64) runtime.Value {
	switch op {
	case bytecode.BinAdd:
		return a + b
	case bytecode.BinSub:
		return a - b
	case bytecode.BinMul:
		return a * b
	case bytecode.BinQuo:
		return a / b
	case bytecode.BinLss:
		return a < b
	case bytecode.BinLeq:
		return a <= b
	case bytecode.BinGtr:
		return a > b
	case bytecode.BinGeq:
		return a >= b
	}
	f.trap("float binary %s", op)
	return nil
}

func stringBinOp(f *frame, op bytecode.BinOp, a string, b runtime.Value) runtime.Value {
	s, ok := b.(string)
	if !ok {
		f.trap("string binary on %T", b)
	}
	switch op {
	case bytecode.BinAdd:
		return a + s
	case bytecode.BinLss:
		return a < s
	case bytecode.BinLeq:
		return a <= s
	case bytecode.BinGtr:
		return a > s
	case bytecode.BinGeq:
		return a >= s
	}
	f.trap("string binary %s", op)
	return nil
}

// unOpToken maps a bytecode unary op to its go/token spelling.
func unOpToken(op bytecode.UnOp) (token.Token, bool) {
	switch op {
	case bytecode.UnPos:
		return token.ADD, true
	case bytecode.UnNeg:
		return token.SUB, true
	case bytecode.UnNot:
		return token.NOT, true
	case bytecode.UnXor:
		return token.XOR, true
	}
	return 0, false
}

// constUnary applies a unary op in the constant domain; ok is false
// when the op doesn't apply to the constant's kind (go/constant
// panics on those — the runtime path below reports them).
func constUnary(op bytecode.UnOp, u *runtime.UConst) (res *runtime.UConst, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := unOpToken(op)
	if !ok {
		return nil, false
	}
	return &runtime.UConst{V: constant.UnaryOp(tok, u.V, 0), Rune: u.Rune}, true
}

func unaryOp(f *frame, op bytecode.UnOp, a runtime.Value) runtime.Value {
	if u, ok := a.(*runtime.UConst); ok {
		// unary ops on constants stay in the constant domain
		if cv, ok2 := constUnary(op, u); ok2 {
			return cv
		}
		a = materialize(f, a)
	}
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag, a = n.Typ, n.V
	}
	// unary results keep the operand's declared type (-x, +x, ^x, !x are
	// all typed T when x is T); a bare result stays bare. A sized-int
	// operand wraps the result to its width — -uint8(5) is 251, not -5.
	retag := func(r runtime.Value) runtime.Value {
		if tag != nil {
			if iv, ok := r.(int64); ok {
				return runtime.Tag(tag, maskInt(iv, sizedNameOf(tag)))
			}
			switch r.(type) {
			case float64, string, bool, *runtime.GoValue:
				return runtime.Tag(tag, r)
			}
		}
		return r
	}
	switch op {
	case bytecode.UnNot:
		return retag(!truthy(a))
	case bytecode.UnPos:
		return retag(a)
	case bytecode.UnNeg:
		switch x := a.(type) {
		case int64:
			return retag(-x)
		case float64:
			return retag(-x)
		case time.Duration:
			return retag(-x)
		case *runtime.GoValue:
			// -u on a wide uint64 wraps mod 2^64, re-boxed so the unsigned
			// domain survives.
			if u, ok := x.V.(uint64); ok {
				return retag(&runtime.GoValue{V: -u})
			}
			switch c := x.V.(type) {
			case complex64:
				return &runtime.GoValue{V: -c}
			case complex128:
				return &runtime.GoValue{V: -c}
			}
		}
		f.trap("unary - on %T", a)
	case bytecode.UnXor:
		if x, ok := a.(int64); ok {
			return retag(^x)
		}
		if g, ok := a.(*runtime.GoValue); ok {
			if u, ok := g.V.(uint64); ok {
				return retag(&runtime.GoValue{V: ^u})
			}
		}
		f.trap("unary ^ on %T", a)
	}
	f.trap("unary %s on %T", op, a)
	return nil
}

func eqlValue(a, b runtime.Value) bool {
	if n, ok := a.(*runtime.Named); ok {
		a = n.V
	}
	if n, ok := b.(*runtime.Named); ok {
		b = n.V
	}
	// raw time.Duration values compare by their int64 nanoseconds.
	if d, ok := a.(time.Duration); ok {
		a = int64(d)
	}
	if d, ok := b.(time.Duration); ok {
		b = int64(d)
	}
	// boxed complex values compare by value across widths — Go rejects
	// complex64 == complex128 (mismatched types), but here both operands
	// already passed the tag check so compare numerically like int/float.
	if ca, _, ok := asComplex(a); ok {
		cb, _, ok2 := asComplex(b)
		return ok2 && ca == cb
	}
	if ai, ok := a.(int64); ok {
		switch bv := b.(type) {
		case int64:
			return ai == bv
		case float64:
			return float64(ai) == bv
		case float32:
			return float64(ai) == float64(bv)
		}
		return false
	}
	if af, ok := a.(float64); ok {
		switch bv := b.(type) {
		case int64:
			return af == float64(bv)
		case float64:
			return af == bv
		case float32:
			return af == float64(bv)
		}
		return false
	}
	// host-returned float32 values (math.Float32frombits, ...) stay raw;
	// compare them numerically so -0 == +0 holds like Go.
	if af, ok := a.(float32); ok {
		switch bv := b.(type) {
		case int64:
			return float64(af) == float64(bv)
		case float64:
			return float64(af) == bv
		case float32:
			return af == bv
		}
		return false
	}
	// two interface values with the same uncomparable dynamic type panic
	// on == — `any([]int(nil)) == any([]int{1})` traps on the *type*,
	// not the values — whatever nil-ness the pair holds.
	if ua := uncomparableDynamicTyp(a); ua != nil {
		if ub := uncomparableDynamicTyp(b); ub != nil && sameTypeDef(ua, ub) {
			panic(runtime.ComparingUncomparablePanic(spelledTyp(ua)))
		}
	}
	if in, ok := a.(*runtime.IfaceNil); ok {
		if in.Typ != nil && in.Typ.Kind == runtime.KindInterface {
			// a nil interface value: equal to nil and to other nil
			// interfaces (the declared interface type is static-only),
			// but never to a typed nil boxed in an interface.
			switch bi := b.(type) {
			case runtime.Nil:
				return true
			case *runtime.IfaceNil:
				return bi.Typ != nil && bi.Typ.Kind == runtime.KindInterface
			}
			return false
		}
		// interface value holding a typed nil: nil only to a same-typed nil
		switch bi := b.(type) {
		case *runtime.IfaceNil:
			return bi.Typ != nil && bi.Typ.Kind != runtime.KindInterface &&
				sameTypeDef(in.Typ, bi.Typ)
		case *runtime.TypedNil:
			return sameTypeDef(in.Typ, bi.Typ)
		}
		return false
	}
	if tn, ok := a.(*runtime.TypedNil); ok {
		switch bi := b.(type) {
		case runtime.Nil:
			return true // a nil pointer/slice/map/chan/func == nil
		case *runtime.TypedNil:
			// two typed nils of different dynamic types are just unequal
			// (Go's interface == compares the (type, value) pair);
			// only the SAME type can panic for being uncomparable.
			if !runtime.TypIdentical(tn.Typ, bi.Typ) {
				return false
			}
			// a typed nil of a slice/map/func type is still uncomparable —
			// Go panics on the TYPE even when the value is nil.
			if uncomparableTyp(tn.Typ) {
				panic(runtime.ComparingUncomparablePanic(spelledTyp(tn.Typ)))
			}
			return true
		case *runtime.IfaceNil:
			return bi.Typ != nil && bi.Typ.Kind != runtime.KindInterface &&
				sameTypeDef(tn.Typ, bi.Typ)
		}
		return false
	}
	if _, ok := a.(runtime.Nil); ok {
		switch bi := b.(type) {
		case runtime.Nil, *runtime.TypedNil:
			return true
		case *runtime.IfaceNil:
			// a nil interface value equals nil; a typed nil boxed in
			// an interface does not.
			return bi.Typ != nil && bi.Typ.Kind == runtime.KindInterface
		}
		return false
	}
	switch av := a.(type) {
	case *runtime.Struct:
		// structs compare field-wise in Go; the defs must name the same
		// type — anonymous struct typedefs with the same field list are
		// the same type (Go's identical-underlying rule).
		bs, ok := b.(*runtime.Struct)
		if !ok || !runtime.TypIdentical(av.Def, bs.Def) || len(av.Fields) != len(bs.Fields) {
			return false
		}
		for i := range av.Fields {
			// an uncomparable field type panics on the struct itself —
			// Go's message names the enclosing type, not the field's.
			if uncomparableValue(av.Fields[i]) {
				panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Def)))
			}
			if !eqlValue(av.Fields[i], bs.Fields[i]) {
				return false
			}
		}
		return true
	case *runtime.Slice:
		if bs, ok := b.(*runtime.Slice); ok {
			// different element types make different dynamic types —
			// any([]int{...}) == any([]string{...}) is false, not a panic.
			if !runtime.TypIdentical(av.Typ, bs.Typ) {
				return false
			}
			// array-typed values compare element-wise; plain slices are
			// uncomparable and the comparison panics, like Go.
			if isArrayTyp(av.Typ) && isArrayTyp(bs.Typ) {
				if len(av.Elems) != len(bs.Elems) {
					return false
				}
				for i := range av.Elems {
					if !eqlValue(av.Elems[i], bs.Elems[i]) {
						return false
					}
				}
				return true
			}
			panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Typ)))
		}
		return false
	case *runtime.Map:
		if bm, ok := b.(*runtime.Map); ok {
			if !runtime.TypIdentical(av.Typ, bm.Typ) {
				return false
			}
			panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Typ)))
		}
		return false
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch b.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			panic(runtime.ComparingUncomparablePanic("func"))
		}
		return false
	case *runtime.Chan:
		if _, ok := b.(*runtime.Chan); ok {
			return a == b // channel identity
		}
		return false
	case *runtime.GoValue:
		// host values compare by underlying identity: two wrappers
		// around the same object (e.g. the io.EOF sentinel) are equal.
		if bg, ok := b.(*runtime.GoValue); ok {
			if av.V == nil || bg.V == nil {
				return av.V == bg.V
			}
			t := reflect.TypeOf(av.V)
			if !t.Comparable() {
				panic(runtime.ComparingUncomparablePanic(t.String()))
			}
			return av.V == bg.V
		}
		return false
	case *runtime.Cell:
		if cb, ok := b.(*runtime.Cell); ok {
			// Go allocates zero-size pointees at runtime.zerobase — two
			// distinct cells still compare equal, like `new([0]int)`.
			if zeroSizeValue(av.Elem) && zeroSizeValue(cb.Elem) {
				return true
			}
			return av == cb
		}
		return false
	case *runtime.IndexRef:
		if br, ok := b.(*runtime.IndexRef); ok {
			// &s[i] compares by the backing element slot, not the ref
			// node — (*[N]T)(s) re-views s's backing array, so
			// &s5[0] == &ss[0] even through distinct slice headers.
			as, bs := av.Slice(), br.Slice()
			ai, aok := av.Key.(int64)
			bi, bok := br.Key.(int64)
			if as != nil && bs != nil && aok && bok &&
				ai >= 0 && ai < int64(len(as.Elems)) &&
				bi >= 0 && bi < int64(len(bs.Elems)) {
				return &as.Elems[ai] == &bs.Elems[bi]
			}
			return refBase(av.Base) == refBase(br.Base) && eqlValue(av.Key, br.Key)
		}
		return false
	}
	return a == b // pointers, strings, bools
}

// refBase unwraps a ref's base to the identity object pointer
// equality means: the container behind any Cell/Named wrappers.
func refBase(v runtime.Value) runtime.Value {
	for {
		if dv, ok := runtime.Deref(v); ok {
			v = dv
			continue
		}
		return v
	}
}

// zeroSizeValue reports whether a value's type occupies no bytes — an
// empty struct, a [0]T array, or a composite of only zero-size fields.
// Go stores all such values at the same address (runtime.zerobase).
func zeroSizeValue(v runtime.Value) bool {
	return runtime.IsZeroSizeValue(v)
}

// isArrayTyp reports whether td keeps a fixed array length — the value
// it types is an array (copying, comparable), not a slice.
func isArrayTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	at, ok := x.(*ast.ArrayType)
	return ok && at.Len != nil
}

// arrayASTOf returns the ArrayType AST behind an array typedef (Anon or
// the named spec's type).
func arrayASTOf(td *runtime.TypeDef) *ast.ArrayType {
	if td == nil {
		return nil
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	at, _ := x.(*ast.ArrayType)
	return at
}

// sliceTypOf: slicing an array-typed value yields the slice type []Elem —
// `a[1:]` on [4]int is []int, not [4]int. Carrying the array typedef
// would make later binds deep-copy it like an array (losing the shared
// backing) and would key it differently as a map key.
func sliceTypOf(td *runtime.TypeDef) *runtime.TypeDef {
	at := arrayASTOf(td)
	if at == nil || at.Len == nil {
		return td
	}
	return &runtime.TypeDef{
		Kind: runtime.KindSlice,
		Elem: td.Elem,
		Anon: &ast.ArrayType{Elt: at.Elt},
	}
}

// uncomparableTyp reports whether a typedef's kind is uncomparable in
// Go — slices, maps and funcs panic on == even when nil.
func uncomparableTyp(td *runtime.TypeDef) bool {
	return td != nil && (td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap || td.Kind == runtime.KindFunc)
}

// uncomparableDynamicTyp returns the value's dynamic typedef when its
// type is uncomparable — a slice, map or nil-of-either — so an ==
// between two interface values carrying the same uncomparable dynamic
// type panics like Go, including nil-of-slice against a live slice.
func uncomparableDynamicTyp(v runtime.Value) *runtime.TypeDef {
	switch x := v.(type) {
	case *runtime.Slice:
		if x.Typ != nil && !isArrayTyp(x.Typ) {
			return x.Typ
		}
	case *runtime.Map:
		return x.Typ
	case *runtime.TypedNil:
		if uncomparableTyp(x.Typ) {
			return x.Typ
		}
	case *runtime.IfaceNil:
		if uncomparableTyp(x.Typ) {
			return x.Typ
		}
	}
	return nil
}

// uncomparableValue reports whether an == over v must panic: slices,
// maps and funcs (live or typed-nil) are uncomparable types.
func uncomparableValue(v runtime.Value) bool {
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Slice:
		return !isArrayTyp(x.Typ)
	case *runtime.Map, *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return true
	case *runtime.TypedNil:
		return uncomparableTyp(x.Typ)
	}
	return false
}

// typeExprFor renders a typedef back to a type expression for synthetic
// wrappers like the `(*T)` of a pointer method expression. Named types
// render as the name so a later ResolveType finds the declaration (and
// its methods) through the package index.
func typeExprFor(td *runtime.TypeDef) ast.Expr {
	if td.Name != "" {
		return ast.NewIdent(td.Name)
	}
	if td.Anon != nil {
		return td.Anon
	}
	return ast.NewIdent("interface{}")
}

// sameTypeDef reports whether two typedefs name the same type — the
// shared runtime.TypIdentical judgment (decl object, or name+package+
// binds for named types, or equal shape spellings for anonymous ones).
func sameTypeDef(a, b *runtime.TypeDef) bool {
	return runtime.TypIdentical(a, b)
}

// lenExprName renders an array-length expression inside a type spelling
// — the `3` of `[3]int`, a named const, or `...`.
func lenExprName(e ast.Expr) string {
	switch l := e.(type) {
	case nil:
		return ""
	case *ast.BasicLit:
		return l.Value
	case *ast.Ident:
		return l.Name
	case *ast.Ellipsis:
		return "..."
	}
	return fmt.Sprintf("%T", e)
}

// typeExprName renders a type AST to a comparable shape string for
// anonymous-type identity (approximation: structural equality by shape,
// not by the go/types identity rules).
func typeExprName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeExprName(t.X)
	case *ast.ArrayType:
		return "[" + lenExprName(t.Len) + "]" + typeExprName(t.Elt)
	case *ast.MapType:
		return "map[" + typeExprName(t.Key) + "]" + typeExprName(t.Value)
	case *ast.ChanType:
		return "chan " + typeExprName(t.Value)
	case *ast.SelectorExpr:
		return typeExprName(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return typeExprName(t.X) + "[" + typeExprName(t.Index) + "]"
	case *ast.IndexListExpr:
		s := typeExprName(t.X) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += typeExprName(x)
		}
		return s + "]"
	case *ast.ParenExpr:
		return typeExprName(t.X)
	case *ast.Ellipsis:
		return "..." + typeExprName(t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}

// tdName is a readable name for a typedef in diagnostics.
func tdName(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil type>"
	}
	if td.Name != "" {
		return td.Name
	}
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	switch td.Kind {
	case runtime.KindInterface:
		return "interface{}"
	case runtime.KindSlice:
		return "slice"
	case runtime.KindMap:
		return "map"
	case runtime.KindFunc:
		return "func"
	case runtime.KindChan:
		return "chan"
	}
	return "type"
}

func isFloat(v runtime.Value) bool {
	_, ok := v.(float64)
	return ok
}

func toFloat(v runtime.Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case *runtime.Named:
		return toFloat(x.V)
	}
	return 0
}

// convert implements T(x) — a call on a *TypeDef.
func (v *VM) convert(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	// nil converts to a typed nil for nilable kinds, NIL for interfaces.
	// Nilability reads through declared chains (`type C B` where B is a
	// slice type takes nil even though C's own kind reads NamedBasic).
	if _, isNil := x.(runtime.Nil); isNil || x == nil {
		utd := td
		k := td.Kind
		if k == runtime.KindNamedBasic || k == runtime.KindAlias {
			if u := v.peelNamed(td); u != nil {
				utd, k = u, u.Kind
			}
		}
		switch k {
		case runtime.KindInterface:
			// nil converts to a nil interface of the declared kind —
			// not bare NIL: `(*ET)(nil) == error(nil)` must be false
			// since a typed nil boxed in an interface is non-nil.
			return &runtime.IfaceNil{Typ: utd}, nil
		case runtime.KindSlice, runtime.KindMap, runtime.KindChan, runtime.KindFunc, runtime.KindPointer:
			return &runtime.TypedNil{Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert nil to %s", tdName(td))
	}
	// a typed nil converts to another nilable type by re-tagging when the
	// underlying shapes match (Go requires identical underlying types).
	// Declared chains (`type C B`) peel like the untyped-nil path so the
	// result carries the target's canonical zero form.
	if tn, ok := asTypedNil(x); ok {
		k := td.Kind
		if k == runtime.KindNamedBasic || k == runtime.KindAlias {
			if u := v.peelNamed(td); u != nil {
				k = u.Kind
			}
		}
		switch k {
		case runtime.KindPointer:
			// (*[N]T)(nilSlice) is a slice conversion, not a re-tag —
			// convertPointer decides by the target's array length.
			if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
				break
			}
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindSlice:
			// [N]T(nilSlice) is a slice-to-array conversion producing
			// the zero array — convertArray decides, not the re-tag.
			if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
				if _, isArr := v.arrayLen(v.topFrame(), td); isArr {
					break
				}
			}
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindMap, runtime.KindChan, runtime.KindFunc:
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindInterface:
			return &runtime.IfaceNil{Typ: tn.Typ}, nil
		}
	}
	// an untyped constant converts by Go's representability rules —
	// int64('a'), float64(1e500)'s overflow, string('a'), complex128(3)
	// all land here.
	if u, ok := x.(*runtime.UConst); ok {
		return v.materializeConstErr(u, td)
	}
	// an interface conversion keeps the dynamic pair: the value
	// satisfies the interface's methods AS its dynamic type —
	// `interface{}(F(3))` stays a main.F, never the unwrapped float64.
	if td.Kind == runtime.KindInterface {
		ok, err := v.ifaceSatisfied(td, x)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
		}
		return x, nil
	}
	// a Named value converts through its underlying value — `string(x)` on
	// a named string value works like the underlying conversion; `T(x)`
	// on the same declared type is a no-op. Nested tags peel too — a
	// `type Tsmallv byte` value carries Named{Tsmallv, Named{byte, v}}.
	// A peeled unsigned tag is remembered: float conversions of an
	// unsigned source read the bits as uint64 (float64(uint64(1<<63))
	// is 2^63, not -2^63).
	srcUnsigned := false
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			break
		}
		if sameTypeDef(n.Typ, td) {
			return x, nil
		}
		if unsignedName(sizedNameOf(n.Typ)) {
			srcUnsigned = true
		}
		x = n.V
	}
	// a host value unboxes so its concrete value converts like a script
	// value of the same shape (a []byte arriving boxed becomes a slice).
	x = unboxGoValue(x)
	switch td.Name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
		var iv int64
		var uv uint64
		switch n := x.(type) {
		case int64:
			iv, uv = n, uint64(n)
		case float64:
			iv, uv = int64(n), uint64(n)
		case string:
			iv = int64([]rune(n)[0]) // int("x") is the first rune's code point
			uv = uint64(iv)
		case *runtime.GoValue:
			// a boxed host integer (a wide uint64 literal, a reflect
			// result) converts by its host kind.
			rv := reflect.ValueOf(n.V)
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				iv, uv = rv.Int(), uint64(rv.Int())
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				uv, iv = rv.Uint(), int64(rv.Uint())
			case reflect.Float32, reflect.Float64:
				iv, uv = int64(rv.Float()), uint64(rv.Float())
			default:
				return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
			}
		default:
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
		}
		// an unsigned conversion past MaxInt64 keeps its bits boxed —
		// `uint64(1.6717361816799281e+19)` is 16717361816799281152, and a
		// wrapped int64(-9.2e18) would read it as a negative float when
		// converted back ($GOROOT/test/ken/convert.go's tu64 rows).
		if unsignedName(td.Name) && uv > math.MaxInt64 {
			return runtime.Tag(td, &runtime.GoValue{V: uv}), nil
		}
		if sizedIntName(td.Name) || td.Name == "int64" {
			// the converted value keeps its declared tag: arithmetic
			// re-wraps to the type's width (-u on uint8 yields 251), %T
			// prints the type name, and unsigned uint64 keeps its
			// domain for %x/%d. `int64(x)` tags too — an explicit
			// conversion declares its type — while bare ints stay
			// untagged (their %T already spells "int").
			return runtime.Tag(td, maskInt(iv, td.Name)), nil
		}
		return maskInt(iv, td.Name), nil
	case "float32":
		if iv, ok := x.(int64); ok && srcUnsigned {
			return runtime.Tag(td, float64(float32(uint64(iv)))), nil
		}
		if f, ok := hostFloat(x); ok {
			return runtime.Tag(td, float64(float32(f))), nil
		}
		switch x.(type) {
		case int64, float64:
			return runtime.Tag(td, float64(float32(toFloat(x)))), nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "float64":
		if iv, ok := x.(int64); ok && srcUnsigned {
			return float64(uint64(iv)), nil
		}
		if f, ok := hostFloat(x); ok {
			return f, nil
		}
		switch x.(type) {
		case int64, float64:
			return toFloat(x), nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "complex64":
		if cv, ok := convComplex(x); ok {
			return &runtime.GoValue{V: complex64(cv)}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "complex128":
		if cv, ok := convComplex(x); ok {
			return &runtime.GoValue{V: cv}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "string":
		switch sx := x.(type) {
		case string:
			return sx, nil
		case *runtime.TypedNil:
			// string([]byte(nil)) / string([]rune(nil)) yields "" — a
			// nilable source that isn't a slice is a convert error.
			if sx.Typ == nil || sx.Typ.Kind == runtime.KindSlice {
				return "", nil
			}
			return nil, fmt.Errorf("cannot convert %s to string", typeNameOf(x))
		case int64:
			return string(rune(sx)), nil
		case *runtime.Slice:
			// []byte or []rune -> string: the element family decides.
			// An untyped slice (host-produced) reads as bytes.
			fam := byte('b')
			tn := "[]byte"
			if sx.Typ != nil {
				fam = v.elemFamily(v.elemTypedef(v.topFrame(), sx.Typ))
				tn = tdName(sx.Typ)
			}
			switch fam {
			case 'b':
				bs := make([]byte, 0, len(sx.Elems))
				for _, e := range sx.Elems {
					i, ok := runtime.Unwrap(e).(int64)
					if !ok {
						return nil, fmt.Errorf("cannot convert %s to string", tn)
					}
					bs = append(bs, byte(i))
				}
				return string(bs), nil
			case 'r':
				rs := make([]rune, 0, len(sx.Elems))
				for _, e := range sx.Elems {
					i, ok := runtime.Unwrap(e).(int64)
					if !ok {
						return nil, fmt.Errorf("cannot convert %s to string", tn)
					}
					rs = append(rs, rune(i))
				}
				return string(rs), nil
			}
			return nil, fmt.Errorf("cannot convert %s to string", tdName(sx.Typ))
		}
		return nil, fmt.Errorf("cannot convert %s to string", typeNameOf(x))
	case "bool":
		return truthy(x), nil
	}
	// a declared name built on another type — an alias `type A = T` or a
	// chain `type C B` — converts through that type first. An alias yields
	// the target's own identity; `type C B` re-wraps in C's identity.
	switch td.Kind {
	case runtime.KindAlias:
		if u := v.peelAlias(td); u != nil && u != td {
			return v.convert(u, x)
		}
	case runtime.KindNamedBasic:
		if u := v.peelNamed(td); u != nil && u != td {
			cv, err := v.convert(u, x)
			if err != nil {
				return nil, err
			}
			// the inner conversion tags the underlying name — MyI16's
			// int16 — leaving Named{MyI16, Named{int16, v}}; binary ops
			// peel one level and would lose the declared tag. Flatten
			// to the single level the const-materialized form carries.
			if cn, ok := cv.(*runtime.Named); ok && sameTypeDef(cn.Typ, u) {
				cv = cn.V
			}
			return runtime.Tag(td, cv), nil
		}
	}
	switch td.Kind {
	case runtime.KindSlice:
		if an, isArr := v.arrayLen(v.topFrame(), td); isArr {
			return v.convertArray(td, x, an)
		}
		return v.convertSlice(td, x)
	case runtime.KindMap:
		return v.convertMap(td, x)
	case runtime.KindChan:
		return v.convertChan(td, x)
	case runtime.KindPointer:
		return v.convertPointer(td, x)
	case runtime.KindStruct:
		return v.convertStruct(td, x)
	case runtime.KindFunc:
		// generalized inference (Go 1.27): `F(Id)` instantiates the
		// generic function against F's signature first — the inferred
		// binds carry over into the (possibly named) result.
		if xv, ierr := v.inferForFuncTarget(x, td); ierr != nil {
			return nil, ierr
		} else {
			x = xv
		}
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			// a declared func type re-tags so `x.(F)` checks identity
			// and member access sees only F's declared method set —
			// function values carry no swappable tag otherwise.
			if td.Spec != nil {
				return runtime.Tag(td, x), nil
			}
			return x, nil // signatures are not modeled
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	// declared named basic types tag the converted value so its declared
	// identity survives reads (MyInt(5) is MyInt, not int). This point is
	// reached only when the underlying ident did not resolve (no engine
	// hooks or an unbound name) — the declared tag still applies, like
	// every other dynamic fallthrough.
	if td.Kind == runtime.KindNamedBasic {
		if !declaredType(td) {
			return x, nil
		}
		u := x
		if id, ok := td.Anon.(*ast.Ident); ok {
			cv, err := v.convert(&runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: id.Name}, x)
			if err != nil {
				return nil, err
			}
			u = cv
		}
		return runtime.Tag(td, u), nil
	}
	if td.Name != "" {
		return x, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// hostFloat reads a value as float64 for float conversions: plain
// int64/float64 pass through, and a boxed host number converts by its
// own kind (uint64 reads unsigned — a wide literal keeps 2^63, not
// the int64 reinterpretation).
func hostFloat(x runtime.Value) (float64, bool) {
	gv, ok := x.(*runtime.GoValue)
	if !ok {
		return 0, false
	}
	rv := reflect.ValueOf(gv.V)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

// unboxGoValue gives a boxed host value the script value of the same
// shape where one exists, so `[]byte(x)` and `string(x)` see through a
// GoValue carrying []byte, []rune or string. Other shapes stay boxed.
func unboxGoValue(x runtime.Value) runtime.Value {
	gv, ok := x.(*runtime.GoValue)
	if !ok {
		return x
	}
	switch h := gv.V.(type) {
	case string:
		return h
	case []byte:
		el := make([]runtime.Value, len(h))
		for i, b := range h {
			el[i] = namedBasicElem("byte", int64(b))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []rune:
		el := make([]runtime.Value, len(h))
		for i, r := range h {
			el[i] = namedBasicElem("rune", int64(r))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("rune")}
	}
	return x
}

// namedBasicElem tags a materialized element with its basic type — a
// slice literal's elements coerce through the element typedef (byte
// elements read as uint8s), so unboxed host slices carry the same tag
// or deep equality / %T see a different element type.
func namedBasicElem(name string, x runtime.Value) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(name), x)
}

// anonSliceTyp builds the anonymous []name typedef used to tag slices
// unboxed from host values (no package context — the name is a builtin).
func anonSliceTyp(name string) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent(name)}}
}

// anonArrayTyp builds the anonymous [n]name typedef used to tag arrays
// unboxed from host values.
func anonArrayTyp(n int, name string) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{
		Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(n)},
		Elt: ast.NewIdent(name),
	}}
}

// convertArray implements `[N]T(s)` on array typedefs (Go 1.20): the
// result is a fresh array copying the slice's first N elements — a
// too-short or wrongly-shaped source panics/errors like Go.
func (v *VM) convertArray(td *runtime.TypeDef, x runtime.Value, n int64) (runtime.Value, error) {
	if tn, isNil := asTypedNil(x); isNil {
		if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
			// a nil slice has length 0 — too short for a non-zero array.
			if n > 0 {
				panic(runtime.SliceToArrayPanic(0, n))
			}
			return &runtime.Slice{Elems: v.zeroElems(v.topFrame(), td, n), Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
	}
	if s, ok := runtime.Unwrap(x).(*runtime.Slice); ok {
		// an array source converts only to the identical array type —
		// slicing out a different length is a slice conversion's job.
		if s.Typ != nil {
			if _, srcIsArr := v.arrayLen(v.topFrame(), s.Typ); srcIsArr && !v.tdShapeEq(s.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
			}
		}
		if int64(len(s.Elems)) < n {
			panic(runtime.SliceToArrayPanic(len(s.Elems), n))
		}
		out := v.zeroElems(v.topFrame(), td, n)
		copy(out, s.Elems[:n])
		return &runtime.Slice{Elems: out, Typ: td}, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// zeroElems fills an array's element slots with the element type's zero.
func (v *VM) zeroElems(f *frame, td *runtime.TypeDef, n int64) []runtime.Value {
	out := make([]runtime.Value, n)
	if et := v.elemTypedef(f, td); et != nil {
		z := v.zeroValue(f, et)
		for i := range out {
			out[i] = z
		}
	}
	return out
}

// convertSlice implements `[]T(x)`: the special string->byte/rune-slice
// conversions plus slice->slice when the underlying shapes are identical
// (element types compare by identity — []int does not convert to
// []MyIntElem, while []uint8 and []byte are the same type).
func (v *VM) convertSlice(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	switch s := x.(type) {
	case string:
		et := v.elemTypedef(v.topFrame(), td)
		switch v.elemFamily(et) {
		case 'b':
			el := make([]runtime.Value, 0, len(s))
			for _, b := range []byte(s) {
				// elements coerce through the declared element type —
				// []byte("x") carries byte-tagged elements like the
				// []byte{...} literal does.
				el = append(el, v.coerce(v.topFrame(), int64(b), et))
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		case 'r':
			el := make([]runtime.Value, 0, len(s))
			for _, r := range s {
				el = append(el, v.coerce(v.topFrame(), int64(r), et))
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert string to %s", tdName(td))
	case *runtime.Slice:
		if an, isArr := v.arrayLen(v.topFrame(), td); isArr {
			// [N]T(s) — slice-to-array conversion copies the first N
			// elements (too-short slices panic like Go's runtime check).
			if int64(len(s.Elems)) < an {
				panic(runtime.SliceToArrayPanic(len(s.Elems), an))
			}
			return v.copyArray(v.topFrame(), &runtime.Slice{Elems: s.Elems[:an], Typ: td}, td), nil
		}
		if s.Typ != nil && !v.convShapeEq(s.Typ, td) {
			return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Typ), tdName(td))
		}
		// Go shares the backing array on a conversion: re-tag, no copy.
		return &runtime.Slice{Elems: s.Elems, Typ: td}, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// convertMap implements `M(x)` on map typedefs: identical underlying
// shapes share the same map (key types are part of the shape). The Named
// wrap keeps the declared tag while sends/indexes resolve through to the
// shared underlying map — Go's conversion aliases the map.
func (v *VM) convertMap(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	m, ok := x.(*runtime.Map)
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if m.Typ != nil && !v.convShapeEq(m.Typ, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(m.Typ), tdName(td))
	}
	return runtime.Tag(td, m), nil
}

// convertChan implements `C(x)` on channel typedefs: identical underlying
// shapes share the same channel. The Named wrap keeps the declared tag
// while the underlying queue stays shared — Go's conversion aliases the
// channel.
func (v *VM) convertChan(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	ch, ok := x.(*runtime.Chan)
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if ch.Typ != nil && !v.convShapeEq(ch.Typ, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(ch.Typ), tdName(td))
	}
	return runtime.Tag(td, ch), nil
}

// convertPointer implements `*T(x)` and `P(x)` on pointer typedefs. A
// pointer in this model is a cell — it carries no swappable type tag, so
// the conversion checks the pointee's declared shape when one is known
// and passes the pointer itself through.
func (v *VM) convertPointer(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	if tn, isNil := asTypedNil(x); isNil {
		// (*[N]T)(nilSlice) — a nil slice converts to a nil array
		// pointer only when N == 0; a larger array conversion panics
		// on the missing backing store like Go.
		if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
			if et := v.elemTypedef(v.topFrame(), td); et != nil {
				if an, isArr := v.arrayLen(v.topFrame(), et); isArr {
					if an == 0 {
						return &runtime.TypedNil{Typ: td}, nil
					}
					panic(runtime.SliceToArrayPanic(0, an))
				}
			}
		}
	}
	if s, isSlice := runtime.Unwrap(x).(*runtime.Slice); isSlice {
		// (*[N]T)(s) — slice-to-array-pointer conversion shares the
		// slice's backing array (too-short slices panic like Go's).
		if et := v.elemTypedef(v.topFrame(), td); et != nil {
			if an, isArr := v.arrayLen(v.topFrame(), et); isArr {
				if int64(len(s.Elems)) < an {
					panic(runtime.SliceToArrayPanic(len(s.Elems), an))
				}
				return &runtime.Cell{Elem: &runtime.Slice{Elems: s.Elems[:an], Typ: et}}, nil
			}
		}
	}
	if _, ok := runtime.Deref(x); !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if ptag := v.pointeeTag(x); ptag != nil && v.H.ElemOf != nil {
		if et, err := v.H.ElemOf(v.peelNamed(td)); err == nil && et != nil && !sameTypeDef(ptag, et) && !sameTypeDef(ptag, v.peelAlias(et)) {
			pe, pp := v.peelNamed(et), v.peelNamed(ptag)
			if pe == nil || pp == nil || pe.Kind != pp.Kind ||
				(pe.Kind == runtime.KindStruct && !structFieldsEq(pe, pp)) ||
				(pe.Kind != runtime.KindStruct && !v.convShapeEq(pe, pp)) {
				return nil, fmt.Errorf("cannot convert *%s to %s", tdName(ptag), tdName(td))
			}
		}
	}
	// a declared pointer type re-tags so `x.(P)` checks identity and
	// member access sees only P's declared method set — a bare cell's
	// dynamic type stays the anonymous *Elem. An anonymous *Declared
	// (`(*T)(p)` on a declared T) wraps too: the pointer type is unnamed,
	// but the value must keep T's declared identity so interface checks
	// and method dispatch see the pointee's method set.
	if td.Spec != nil {
		return runtime.Tag(td, x), nil
	}
	if v.H.ElemOf != nil {
		if et, err := v.H.ElemOf(td); err == nil && et != nil && declaredType(et) {
			return runtime.Tag(td, x), nil
		}
	}
	return x, nil
}

// convertStruct implements `S(x)` on struct typedefs: legal when the
// source is a struct whose field list matches the target's (the
// approximation of Go's identical-underlying rule — field types and tags
// are not modeled). The result is a copy carrying the target's typedef.
func (v *VM) convertStruct(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	s, ok := x.(*runtime.Struct)
	if !ok {
		if dv, isRef := runtime.Deref(x); isRef {
			s, ok = dv.(*runtime.Struct)
		}
	}
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if s.Def != nil && s.Def != td && !structFieldsEq(s.Def, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Def), tdName(td))
	}
	return &runtime.Struct{Def: td, Fields: append([]runtime.Value{}, s.Fields...)}, nil
}

// convShapeEq reports whether two typedefs have identical underlying
// shape for a conversion: both peel through declared chains to their
// structural spelling — `type C B` gives B's shape, `Wrap[int]` gives
// []int — while element positions compare by written identity, so []MyInt
// does not spell []int.
func (v *VM) convShapeEq(a, b *runtime.TypeDef) bool {
	sa, sb := v.underlyingShape(a), v.underlyingShape(b)
	return sa != "" && sa == sb
}

// underlyingShape spells a typedef's underlying shape. Declared chain
// heads (type C B, aliases) peel away; composite shapes spell their type
// expression with generic binds substituted.
func (v *VM) underlyingShape(td *runtime.TypeDef) string {
	u := v.peelNamed(td)
	if u == nil {
		return ""
	}
	return runtime.TypUnderlyingSpelling(u)
}

// elemFamily classifies a slice element typedef for the special string
// conversions: 'b' when its underlying is byte-family (byte, uint8, or a
// named type on them like `type MyByte byte`), 'r' for rune-family, else
// 0 — mirroring Go's `[]byte(s)` / `[]rune(s)` rule.
func (v *VM) elemFamily(et *runtime.TypeDef) byte {
	u := v.peelNamed(et)
	if u == nil {
		return 0
	}
	name := u.Name
	if name == "" {
		if id, ok := u.Anon.(*ast.Ident); ok {
			name = id.Name
		}
	}
	switch name {
	case "byte", "uint8":
		return 'b'
	case "rune", "int32":
		return 'r'
	}
	return 0
}

// ---- references, spread, types, specials (round 4) ----

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
	statics := make([]*runtime.TypeDef, argc)
	for i := argc - 1; i >= 0; i-- {
		if td, ok := f.pop().(*runtime.TypeDef); ok {
			statics[i] = td
		}
	}
	args := make([]runtime.Value, argc)
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
		last = materialize(f, last)
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
		args = append(args[:argc-1], s.Elems...)
		spreadTd := v.elemTypedef(f, declared)
		if spreadTd == nil && namedTyp != nil {
			spreadTd = v.elemTypedef(f, namedTyp)
		}
		return args, statics, spreadTd
	}
	return args, statics, nil
}

// typedefOf unwraps references down to a *TypeDef, or nil.
func typedefOf(v runtime.Value) *runtime.TypeDef {
	for {
		if td, ok := v.(*runtime.TypeDef); ok {
			return td
		}
		dv, ok := runtime.Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}

// typeAssert implements x.(T): returns x on match, panics with a script
// Panic (recoverable) on mismatch — Go semantics for a failed assertion.
// static is the operand's declared type (a bare identifier's cell type,
// NIL when the expression has none) — it names the interface in the
// message the way Go does: "main.I is main.T, not io.Writer".
func (v *VM) typeAssert(f *frame, x, tdv, static runtime.Value, pos token.Pos) runtime.Value {
	td := typedefOf(tdv)
	if td == nil {
		f.trap("type assertion target %T is not a type", tdv)
	}
	if v.typeMatches(f, td, x) {
		return unboxAsserted(x, td)
	}
	// asserting to a non-empty interface names the missing method:
	// "main.T is not io.Writer: missing method Write".
	if td.Kind == runtime.KindInterface {
		if miss := v.missingIfaceMethod(td, x); miss != "" {
			panic(&runtime.Panic{Value: &runtime.GoValue{V: fmt.Errorf("interface conversion: %s is not %s: missing method %s", typeNameOf(x), spelledTyp(td), miss)}})
		}
	}
	staticName := "interface {}"
	if st, ok := static.(*runtime.TypeDef); ok {
		staticName = spelledTyp(st)
		if staticName == "interface{}" {
			staticName = "interface {}"
		}
	}
	panic(&runtime.Panic{Value: &runtime.GoValue{V: fmt.Errorf("interface conversion: %s is %s, not %s", staticName, typeNameOf(x), spelledTyp(td))}})
}

// missingIfaceMethod names the first required method x lacks — Go's
// "missing method Write" detail when an interface assertion fails on the
// method set. Empty when the check can't name one (hooks missing, or the
// failure came from something else).
func (v *VM) missingIfaceMethod(td *runtime.TypeDef, x runtime.Value) string {
	if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 {
		return ""
	}
	if v.H.IfaceReqs == nil || v.H.MethodsOf == nil {
		return ""
	}
	reqs, err := v.H.IfaceReqs(td)
	if err != nil || len(reqs) == 0 {
		return ""
	}
	var have map[string]bool
	var unsure bool
	if v.H.MethodSetOf != nil {
		have, unsure, err = v.H.MethodSetOf(x)
	} else {
		have, err = v.H.MethodsOf(x)
	}
	if err != nil {
		return ""
	}
	missing := make([]string, 0, len(reqs))
	for m := range reqs {
		if !have[m] && !unsure {
			missing = append(missing, m)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return ""
	}
	return missing[0]
}

// spelledTyp spells a typedef the way Go's runtime does in conversion
// panics — package-qualified ("main.I", "io.Writer"), anonymous forms
// rendered from their AST.
func spelledTyp(td *runtime.TypeDef) string {
	if td == nil {
		return "interface {}"
	}
	return runtime.DisplayName(td)
}

// typeAssertOK implements the comma-ok form: pushes Tuple{val, ok}.
func (v *VM) typeAssertOK(f *frame, x, tdv runtime.Value) runtime.Value {
	td := typedefOf(tdv)
	if td == nil {
		f.trap("type assertion target %T is not a type", tdv)
	}
	if v.typeMatches(f, td, x) {
		return &runtime.Tuple{Elems: []runtime.Value{unboxAsserted(x, td), true}}
	}
	// Go binds the asserted type's zero value on failure.
	return &runtime.Tuple{Elems: []runtime.Value{v.zeroValue(f, td), false}}
}

// unboxAsserted: a successful assert to a concrete type T pulls the bare
// dynamic value out of an interface box — x.(*int) yields *int, not any.
func unboxAsserted(x runtime.Value, td *runtime.TypeDef) runtime.Value {
	if td.Kind != runtime.KindInterface {
		if in, ok := x.(*runtime.IfaceNil); ok {
			return &runtime.TypedNil{Typ: in.Typ}
		}
	}
	return x
}

// typeMatches implements duck-typing: interfaces check the method set via
// engine hooks; concrete typedefs match by descriptor identity (or name
// for builtins / primitives). Typed nils match by declared type identity.
func (v *VM) typeMatches(f *frame, td *runtime.TypeDef, x runtime.Value) bool {
	if x == nil || x == runtime.NIL {
		// nil has no dynamic type: every assert fails, including .(any).
		// (`case nil:` in a type switch is matched by BinEql, not here.)
		return false
	}
	if td.Kind == runtime.KindAlias {
		// aliases are transparent: `x.(A)` on `type A = T` asserts to T
		if u := v.peelAlias(td); u != td {
			td = u
		}
	}
	if n, ok := x.(*runtime.Named); ok {
		// a Named value's dynamic type is its declared typedef —
		// `x.(MyInt)` on MyInt matches, `x.(int)` does not.
		return v.typeMatchesTD(f, td, n.Typ)
	}
	if tn, ok := asTypedNil(x); ok {
		return v.typeMatchesTD(f, td, tn.Typ)
	}
	if in, ok := x.(*runtime.IfaceNil); ok && in.Typ != nil && in.Typ.Kind == runtime.KindInterface {
		// a nil interface value has no dynamic type — every assert
		// fails, including .(any).
		return false
	}
	if td.Kind == runtime.KindInterface {
		return v.satisfiesIface(f, td, x)
	}
	if td.Kind == runtime.KindPointer {
		if td.Spec != nil {
			// a declared pointer type asserts on its tag alone — the
			// Named branch above already handled it; a bare cell's
			// dynamic type is the anonymous *Elem, never P.
			return false
		}
		// asserting *T on a non-nil value: dereference one level and match
		// the element type (a Cell IS the pointer in this model).
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			et = v.localElemTypedef(f, td)
			if et == nil {
				if err != nil {
					f.trap("%s", err)
				}
				f.trap("cannot resolve element type of %s", tdName(td))
			}
		}
		if gv, ok := x.(*runtime.GoValue); ok {
			// a host box never dereferences through Deref — compare the
			// boxed native type instead. Host typedefs box a pointer
			// zero (sync.Mutex -> *sync.Mutex), so *T matches when the
			// native type equals the typedef's own box; other *T fall
			// back to comparing the printed type spelling.
			if et.HostNew != nil {
				return reflect.TypeOf(gv.V) == reflect.TypeOf(et.HostNew())
			}
			return reflect.TypeOf(gv.V).String() == tdName(td)
		}
		dv, ok := runtime.Deref(x)
		if !ok {
			return false
		}
		return v.typeMatches(f, et, dv)
	}
	dv := x
	// a *Struct under a Cell/other ref still matches T — pointers share the
	// element typedef in this model.
	if d, ok := runtime.Deref(dv); ok {
		if _, isStruct := d.(*runtime.Struct); isStruct {
			dv = d
		}
	}
	switch xv := dv.(type) {
	case *runtime.Struct:
		if xv.Def == td {
			return true
		}
		// anonymous struct spellings are the same type when their
		// field shapes match: `interface{}(struct{}{}).(struct{})`.
		if td.Name == "" && xv.Def != nil && xv.Def.Name == "" {
			return v.convShapeEq(xv.Def, td)
		}
		return xv.Def != nil && td.Name != "" && td.Pkg != nil && sameTypeDef(xv.Def, td)
	case int64:
		// a bare int64's dynamic type is int — int64(x) conversions and
		// the sized ints carry Named tags, so x.(int64), x.(int8) or
		// x.(MyInt) must not match here.
		return td.Name == "int"
	case float64:
		return td.Name == "float64"
	case string:
		return td.Name == "string"
	case bool:
		return td.Name == "bool"
	case *runtime.Slice:
		return v.containerAssert(f, td, xv.Typ, runtime.KindSlice)
	case *runtime.Map:
		return v.containerAssert(f, td, xv.Typ, runtime.KindMap)
	case *runtime.Chan:
		return v.containerAssert(f, td, xv.Typ, runtime.KindChan)
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return v.funcAssert(td, xv)
	case *runtime.GoValue:
		// a host box's dynamic type is its native Go type — complex64
		// boxes (there is no script complex type) assert back to
		// complex64, and interface{}(bytes.Buffer{}) asserts to
		// bytes.Buffer. Compare type spellings like the KindPointer
		// GoValue branch above.
		rt := reflect.TypeOf(xv.V)
		if rt == nil {
			return false
		}
		// a boxed host func compares signatures like a script func
		// value — func(int) int never asserts to func(string).
		if rt.Kind() == reflect.Func && td.Name == "" {
			if ft, ok := td.Anon.(*ast.FuncType); ok {
				return rt.String() == runtime.TypGoSpelling(ft, td)
			}
		}
		return rt.String() == tdName(td)
	default:
		return false
	}
}

// funcAssert implements x.(T) for a function value: an anonymous func
// typedef asserts by signature identity — func(int) int matches only a
// value declared with that signature — and a defined func type matches
// only its own tag (the *runtime.Named branch above handles declared
// func types, so a named target is false for every bare func value).
func (v *VM) funcAssert(td *runtime.TypeDef, x runtime.Value) bool {
	if td.Kind != runtime.KindFunc {
		return false
	}
	if td.Name != "" {
		// a host-bound func still names its real Go type through Target,
		// so a bound context.CancelFunc asserts back to context.CancelFunc.
		if bf, ok := x.(*runtime.BuiltinFunc); ok && bf.Target != nil {
			return reflect.TypeOf(bf.Target).String() == runtime.DisplayName(td)
		}
		return false
	}
	ft, ok := td.Anon.(*ast.FuncType)
	if !ok {
		// the asserted func typedef carries no signature AST to compare
		// against — fall back to the kind match.
		return td.Spec == nil
	}
	if sig, pkg, file, binds := funcSig(x); sig != nil {
		dyn := &runtime.TypeDef{Kind: runtime.KindFunc, Anon: sig, Pkg: pkg, File: file, Binds: binds}
		return runtime.TypIdentical(td, dyn)
	}
	if bf, ok := x.(*runtime.BuiltinFunc); ok && bf.Target != nil {
		// a bound host func compares by its real Go signature.
		return reflect.TypeOf(bf.Target).String() == runtime.TypGoSpelling(ft, td)
	}
	// intrinsics and synthesized adapters declare no signature to check —
	// keep the historical kind match.
	return td.Spec == nil
}

// containerAssert runs x.(T) on a stamped slice/map/chan: a declared tag
// asserts by typedef identity (`type A []int` is not `[]int`), anonymous
// tags compare by underlying shape ([]byte IS []uint8), and untagged
// (host-produced) containers fall back to the kind check.
func (v *VM) containerAssert(f *frame, td, tag *runtime.TypeDef, kind runtime.TypeKind) bool {
	if td.Kind != kind {
		return false
	}
	if tag == nil {
		return true
	}
	if td.Name != "" || tag.Name != "" {
		return v.typeMatchesTD(f, td, tag)
	}
	return v.convShapeEq(td, tag)
}

// satisfiesIface checks a value against an interface typedef's method set.
func (v *VM) satisfiesIface(f *frame, td *runtime.TypeDef, x runtime.Value) bool {
	ok, err := v.ifaceSatisfied(td, x)
	if err != nil {
		f.trap("%s", err)
	}
	return ok
}

// ifaceSatisfied is satisfiesIface's error-returning core, usable from
// contexts without a running frame (conversions).
func (v *VM) ifaceSatisfied(td *runtime.TypeDef, x runtime.Value) (bool, error) {
	if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 {
		return true, nil // empty interface
	}
	if v.H.IfaceReqs == nil || v.H.MethodsOf == nil {
		return false, fmt.Errorf("interface checks require engine hooks")
	}
	reqs, err := v.H.IfaceReqs(td)
	if err != nil {
		return false, err
	}
	if len(reqs) == 0 {
		return true, nil
	}
	var have map[string]bool
	var unsure bool
	if v.H.MethodSetOf != nil {
		have, unsure, err = v.H.MethodSetOf(x)
	} else {
		have, err = v.H.MethodsOf(x)
	}
	if err != nil {
		return false, err
	}
	for m := range reqs {
		if !have[m] && !unsure {
			// an unresolvable embedded type may promote any missing
			// method, so only a complete method set can fail the check
			return false, nil
		}
	}
	return v.ifaceSigsMatch(td, x)
}

// ifaceSigsMatch runs the signature pass of interface satisfaction: a
// same-named method whose declared signature differs does not satisfy
// the requirement — `foo() float64` never fulfills `foo() int`.
// Members without a comparable signature (facade requirements, host
// reflect methods) satisfy by name alone, as before.
func (v *VM) ifaceSigsMatch(td *runtime.TypeDef, x runtime.Value) (bool, error) {
	if v.H.IfaceSigs == nil || v.H.MethodFuncsOf == nil {
		return true, nil
	}
	reqFns, err := v.H.IfaceSigs(td)
	if err != nil {
		return false, err
	}
	if len(reqFns) == 0 {
		return true, nil
	}
	dynFns, err := v.H.MethodFuncsOf(x)
	if err != nil {
		return false, err
	}
	return memberSigsMatch(reqFns, dynFns), nil
}

// memberSigsMatch reports whether every signature-bearing requirement
// is met by a same-named member with an identical signature. A member
// absent from dynFns kept its name-only pass.
func memberSigsMatch(reqFns, dynFns map[string]*runtime.Function) bool {
	for m, reqFn := range reqFns {
		if dynFn, ok := dynFns[m]; ok && !memberSigEq(reqFn, dynFn) {
			return false
		}
	}
	return true
}

// memberSigEq compares two members' declared signatures — an interface
// requirement and the concrete method offered against it. Either side
// lacking a decl signature (synthesized shims) satisfies by name.
func memberSigEq(req, dyn *runtime.Function) bool {
	if req == nil || dyn == nil || req.Decl == nil || dyn.Decl == nil || req.Decl.Type == nil || dyn.Decl.Type == nil {
		return true
	}
	rt := &runtime.TypeDef{Kind: runtime.KindFunc, Anon: req.Decl.Type, Pkg: req.Pkg, File: req.File, Binds: req.Binds}
	dt := &runtime.TypeDef{Kind: runtime.KindFunc, Anon: dyn.Decl.Type, Pkg: dyn.Pkg, File: dyn.File, Binds: dyn.Binds}
	return runtime.TypIdentical(rt, dt)
}

// ---- declared types: zeros, typed nils, interface boxing (round 5) ----

// asTypedNil unwraps an interface-boxed nil down to its TypedNil.
func asTypedNil(x runtime.Value) (*runtime.TypedNil, bool) {
	if tn, ok := x.(*runtime.TypedNil); ok {
		return tn, true
	}
	if in, ok := x.(*runtime.IfaceNil); ok && in.Typ != nil && in.Typ.Kind != runtime.KindInterface {
		// an interface-kind IfaceNil IS the nil interface itself, not
		// a typed nil boxed in an interface — it has no dynamic type.
		return &runtime.TypedNil{Typ: in.Typ}, true
	}
	return nil, false
}

// typeMatchesTD runs x.(T) when x is a typed nil: the "dynamic type" is
// the typedef the nil was declared with. Interfaces check the typedef's
// method set; concrete types compare by type identity.
func (v *VM) typeMatchesTD(f *frame, td, dyn *runtime.TypeDef) bool {
	if td.Kind == runtime.KindInterface {
		if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 {
			return true
		}
		if v.H.IfaceReqs == nil || v.H.TypeMethods == nil {
			f.trap("interface checks require engine hooks")
		}
		reqs, err := v.H.IfaceReqs(td)
		if err != nil {
			f.trap("%s", err)
		}
		have, err := v.H.TypeMethods(dyn)
		if err != nil {
			f.trap("%s", err)
		}
		for m := range reqs {
			if !have[m] {
				return false
			}
		}
		if v.H.IfaceSigs != nil && v.H.TypeMethodFuncs != nil {
			reqFns, err := v.H.IfaceSigs(td)
			if err != nil {
				f.trap("%s", err)
			}
			if len(reqFns) > 0 {
				dynFns, err := v.H.TypeMethodFuncs(dyn)
				if err != nil {
					f.trap("%s", err)
				}
				return memberSigsMatch(reqFns, dynFns)
			}
		}
		return true
	}
	return sameTypeDef(td, dyn)
}

// typedMember resolves base.name on a container value that carries its
// declared typedef in Typ — values of `type L []E`, `type M map[K]V` or
// `type C chan T` keep the declared type's methods, including the generic
// methods added in Go 1.27. `what` names the container for the trap.
func (v *VM) typedMember(f *frame, td *runtime.TypeDef, name string, recv runtime.Value, what string) runtime.Value {
	if td != nil {
		if m, ok := td.Methods[name]; ok {
			if err := m.EnsureCompiled(); err != nil {
				f.trap("%s", err)
			}
			r := recv
			if m.PtrRecv {
				r = v.ptrReceiver(r)
			} else {
				if dv, ok := runtime.Deref(r); ok {
					r = dv
				}
				r = valueCopy(r)
			}
			return &runtime.BoundMethod{Recv: r, Fn: m}
		}
	}
	f.trap("select %s on %s", name, what)
	return nil
}

// memberOfType resolves base.name when base is a nil value carrying a
// type — a method on *T still binds (the body panics on field access);
// a field select on a nil pointer panics like Go.
func (v *VM) memberOfType(f *frame, td *runtime.TypeDef, name string, recv runtime.Value, isIface bool) runtime.Value {
	// methods resolve on the nil's own typedef first, then through an
	// anonymous pointer chain to the pointee (a *T nil keeps *T's method
	// set). A declared pointer typedef (`type P *Sq`) does not promote
	// pointee methods — P's method set is only what is declared on P.
	peeled := false
	for td != nil {
		// a nil interface value has no method at all — any call on it
		// panics like a nil-pointer dereference in Go. The nil arrives
		// as a typed nil on the value path or as an interface-kind
		// IfaceNil through a storage ref — both are the nil interface.
		if td.Kind == runtime.KindInterface {
			if in, isIN := recv.(*runtime.IfaceNil); isIN && (in.Typ == nil || in.Typ.Kind == runtime.KindInterface) {
				panic(runtime.NilDerefPanic())
			}
			if _, isNil := asTypedNil(recv); isNil {
				panic(runtime.NilDerefPanic())
			}
		}
		if m, ok := td.Methods[name]; ok {
			// A value receiver dereferences a peeled pointer chain at
			// dispatch — panic on nil like Go. A nil carrying a nilable
			// typedef (declared pointer/slice/map/chan/func values can
			// be nil) is a valid receiver: the call binds it and the
			// body decides.
			if !m.PtrRecv {
				if _, isNil := asTypedNil(recv); isNil && (peeled || !v.nilableTypedef(td)) {
					if _, isIfaceNil := recv.(*runtime.IfaceNil); isIfaceNil {
						// a value method dispatched through a nil
						// interface box reports Go's wrapper text, not
						// a bare nil dereference.
						panic(runtime.PlainPanic(fmt.Sprintf("value method %s.%s called using nil *%s pointer", spelledTyp(td), name, td.Name)))
					}
					panic(runtime.NilDerefPanic())
				}
			}
			r := recv
			if in, isNil := r.(*runtime.IfaceNil); isNil {
				// an interface holding a nil binds the concrete typed
				// nil as the receiver — `var i I = (*P)(nil); i.M()`
				// calls M on (*P)(nil), not on a nil interface.
				r = &runtime.TypedNil{Typ: in.Typ}
			}
			if !m.PtrRecv {
				if dv, ok := runtime.Deref(r); ok {
					r = valueCopy(dv)
				}
			}
			return &runtime.BoundMethod{Recv: r, Fn: m}
		}
		if td.Kind != runtime.KindPointer || td.Spec != nil {
			break
		}
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			et = v.localElemTypedef(f, td)
			if et == nil {
				break
			}
		}
		td = et
		peeled = true
	}
	// field access on a nil pointer panics in Go; on a nil slice/map/chan
	// it is a plain invalid select. A nil *T may still carry methods —
	// Go dispatches pointer methods on nil receivers and the method
	// body decides, so a host-backed typedef tries a bound zero-receiver
	// method before giving up to the deref panic.
	if tn, ok := recv.(*runtime.TypedNil); ok && tn.Typ.Kind == runtime.KindPointer {
		if bf, ok := v.hostNilMethod(tn.Typ, name); ok {
			return bf
		}
		panic(runtime.NilDerefPanic())
	}
	if in, ok := recv.(*runtime.IfaceNil); ok && in.Typ.Kind == runtime.KindPointer {
		if bf, ok := v.hostNilMethod(in.Typ, name); ok {
			return bf
		}
		panic(runtime.NilDerefPanic())
	}
	if isIface {
		f.trap("interface value has no field %s", name)
	}
	f.trap("select %s on nil %s", name, tdName(td))
	return nil
}

// coerce applies a declared type `td` to the value being bound: NIL picks
// up the type's zero value (var x T), a TypedNil crossing into an
// interface-typed slot boxes as an IfaceNil (var x any = (*int)(nil)),
// and `var x T = v` runs an assignability check — a value of a different
// named type or a mismatched basic family traps, like Go's type checker.
// Declared named basic types tag the result as *runtime.Named so the
// declared identity survives reads.
func (v *VM) coerce(f *frame, x runtime.Value, td *runtime.TypeDef) runtime.Value {
	// a slot store copies like a Go assignment: `key{mark: p.mark}` must
	// not alias p.mark through the literal's field slot.
	x = valueCopy(x)
	if td == nil {
		return x
	}
	if td.Kind == runtime.KindAlias {
		// aliases peel one hop at a time so each intermediate named type
		// keeps its own coerce (a Named value must land on its declared
		// type — `type A = Str` binds a Str-tagged value).
		if u := v.peelAlias(td); u != td {
			return v.coerce(f, x, u)
		}
	}
	if _, isNil := x.(runtime.Nil); isNil {
		return v.zeroValue(f, td)
	}
	if td.Kind == runtime.KindInterface {
		// an untyped constant binds an interface at its default type —
		// `var a any = 'a'` holds a rune, not the lazy constant.
		x = materialize(f, x)
		if tn, ok := x.(*runtime.TypedNil); ok {
			// boxing a typed nil still checks the method set: (*int)(nil)
			// cannot bind an interface that requires methods.
			if !v.satisfiesIface(f, td, x) {
				f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
			}
			return &runtime.IfaceNil{Typ: tn.Typ}
		}
		if !v.satisfiesIface(f, td, x) {
			f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
		}
		return x
	}
	// generalized inference (Go 1.27): a generic function bound to a
	// func-typed slot — assignment, composite element, call argument or
	// channel send — infers its unbound type parameters from the target
	// signature. `var h func(int) int = Id` instantiates Id[int].
	xv, err := v.inferForFuncTarget(x, td)
	if err != nil {
		f.trap("%s", err)
	}
	return v.coerceConcrete(f, xv, td)
}

// inferForFuncTarget instantiates an unbound generic function value when
// the target typedef is a func type whose signature teaches the missing
// binds. Anything unresolvable passes the value through unchanged — an
// unbound generic traps on use, matching the compile-total contract.
func (v *VM) inferForFuncTarget(x runtime.Value, td *runtime.TypeDef) (runtime.Value, error) {
	u := v.peelNamed(td)
	if u == nil || u.Kind != runtime.KindFunc {
		return x, nil
	}
	sig := funcTypeExpr(u)
	if sig == nil || v.H.ResolveType == nil {
		return x, nil
	}
	fn, wrap := funcTarget(x)
	if fn == nil || len(fn.TParams) == 0 || !hasUnbound(fn.TParams, fn.Binds) {
		return x, nil
	}
	if fn.Decl == nil || fn.Decl.Type == nil {
		return x, nil
	}
	tset := map[string]bool{}
	for _, tp := range fn.TParams {
		if _, ok := fn.Binds[tp]; !ok {
			tset[tp] = true
		}
	}
	binds := map[string]runtime.Value{}
	for k, bv := range fn.Binds {
		binds[k] = bv
	}
	// the pattern is the callee's declared signature; the concrete side
	// is the target func type resolved in ITS own context.
	ctx := &runtime.TypeDef{Pkg: fn.Pkg, File: fn.File, Binds: binds}
	v.unifyFieldTypes(ctx, tset, binds, fn.Decl.Type.Params, sig.Params, u)
	v.unifyFieldTypes(ctx, tset, binds, fn.Decl.Type.Results, sig.Results, u)
	if len(binds) == len(fn.Binds) {
		return x, nil // the target signature taught nothing
	}
	if err := v.checkTArgs(ctx, fn.TParams, fn.TConstraints, binds); err != nil {
		return nil, err
	}
	inst := &runtime.Function{
		Pkg: fn.Pkg, File: fn.File, Decl: fn.Decl, Name: fn.Name,
		Recv: fn.Recv, PtrRecv: fn.PtrRecv,
		TParams: fn.TParams, TConstraints: fn.TConstraints,
		Binds: binds, Compile: fn.Compile,
	}
	switch w := wrap.(type) {
	case nil:
		return inst, nil
	case *runtime.Closure:
		return &runtime.Closure{Fn: inst, Upvals: w.Upvals}, nil
	case *runtime.BoundMethod:
		return &runtime.BoundMethod{Recv: w.Recv, Fn: inst}, nil
	}
	return inst, nil
}

// funcTarget unwraps a function-ish value to its *runtime.Function,
// reporting the wrapper to rebuild after instantiation.
func funcTarget(x runtime.Value) (*runtime.Function, runtime.Value) {
	switch t := x.(type) {
	case *runtime.Function:
		return t, nil
	case *runtime.Closure:
		return t.Fn, t
	case *runtime.BoundMethod:
		return t.Fn, t
	case *runtime.Named:
		if fn, w := funcTarget(t.V); fn != nil {
			if w == nil {
				return fn, t
			}
			// rebuild the inner wrapper under the Named tag lazily — a
			// named func value's generic params live on the function.
			return fn, w
		}
	}
	return nil, nil
}

// declaredType reports whether td is a defined (declared) type rather than
// a builtin or anonymous shape: builtins carry only a Name, declared types
// carry their TypeSpec (or at least a package).
func declaredType(td *runtime.TypeDef) bool {
	return td.Spec != nil || td.Pkg != nil
}

// tagIsNamed reports whether td names a defined type — a `type` spec
// (Spec) or a predeclared name like int (Name). Anonymous structural
// typedefs carry a Pkg for name resolution but name nothing.
func tagIsNamed(td *runtime.TypeDef) bool {
	return td != nil && (td.Spec != nil || td.Name != "")
}

// declaredTag returns the declared type a value carries: a Named wrap's
// Typ, a named struct's Def, or a stamped container Typ. Bare values and
// values of unnamed (anonymous) types report nil.
func declaredTag(x runtime.Value) *runtime.TypeDef {
	var td *runtime.TypeDef
	switch t := x.(type) {
	case *runtime.Named:
		td = t.Typ
	case *runtime.Struct:
		td = t.Def
	case *runtime.Map:
		td = t.Typ
	case *runtime.Slice:
		td = t.Typ
	case *runtime.Chan:
		td = t.Typ
	}
	if tagIsNamed(td) {
		return td
	}
	return nil
}

// pointeeTag returns the declared type of a pointer value's pointee: the
// cell's stamped type first, else the stored value's own tag. Nil when
// the pointee is untyped (basic vars, anonymous literals).
func (v *VM) pointeeTag(x runtime.Value) *runtime.TypeDef {
	if c, ok := x.(*runtime.Cell); ok && tagIsNamed(c.Typ) {
		return c.Typ
	}
	dv, ok := runtime.Deref(x)
	if !ok {
		return nil
	}
	return declaredTag(dv)
}

// containerTyp reads the declared-type tag stamped on a container value.
func containerTyp(x runtime.Value) *runtime.TypeDef {
	switch t := x.(type) {
	case *runtime.Map:
		return t.Typ
	case *runtime.Slice:
		return t.Typ
	case *runtime.Chan:
		return t.Typ
	}
	return nil
}

// stampContainerTyp returns x with its declared-type tag set. The tag
// lives on the container header, which is a value in Go's terms: passing
// a slice/map/chan copies the header and only the backing aliases, so
// stamping must not mutate the shared header in place — a `[]Word`
// parameter receiving a `nat` argument would otherwise strip the
// caller's method set (math/big divW hands z to divWVW(z []Word,...)).
func stampContainerTyp(x runtime.Value, td *runtime.TypeDef) runtime.Value {
	switch t := x.(type) {
	case *runtime.Map:
		c := *t
		c.Typ = td
		return &c
	case *runtime.Slice:
		c := *t
		c.Typ = td
		return &c
	case *runtime.Chan:
		c := *t
		c.Typ = td
		return &c
	}
	return x
}

// coerceConcrete applies td to a non-nil x under a non-interface target.
// A Named value keeps its identity only for the identical declared type
// (Go: named-to-named needs a conversion); GoValues pass unchecked at the
// host boundary; a TypedNil re-tags when the underlying shape matches.
// convComplex reads a value into the complex domain for a conversion:
// ints and floats promote to complex128, boxed complex values pass.
func convComplex(x runtime.Value) (complex128, bool) {
	switch n := x.(type) {
	case int64:
		return complex(float64(n), 0), true
	case float64:
		return complex(n, 0), true
	case *runtime.GoValue:
		if cv, _, ok := asComplex(n); ok {
			return cv, true
		}
	}
	return 0, false
}

func (v *VM) coerceConcrete(f *frame, x runtime.Value, td *runtime.TypeDef) runtime.Value {
	if u, ok := x.(*runtime.UConst); ok {
		if utd := v.peelNamed(td); utd != nil && basicNameOf(utd) != "" {
			// a constant converts straight to the declared basic type:
			// `var r MyRune = 'a'`, `var i int8 = 300`'s overflow trap.
			return v.materializeConst(f, u, td)
		}
		// other targets take the default type, then assign normally.
		x = materialize(f, x)
	}
	if n, ok := x.(*runtime.Named); ok {
		// a Named value keeps its identity only for the identical declared
		// type — aliases count (they ARE the type), `type A B` chains do
		// not (Go: named-to-named needs a conversion).
		if sameTypeDef(n.Typ, td) || sameTypeDef(n.Typ, v.peelAlias(td)) {
			return x
		}
		// an unnamed target assigns any value whose underlying type is
		// identical — `type Number *Number`'s `*x` (Number) binds a
		// *Number parameter; a named target still needs the conversion.
		if !tagIsNamed(td) && v.tdShapeEq(n.Typ, td) {
			return x
		}
		f.trap("cannot use %s as %s", tdName(n.Typ), tdName(td))
	}
	if gv, ok := x.(*runtime.GoValue); ok {
		// a complex value coerces into a complex-typed slot at the slot's
		// width; other host boxes pass through unchecked as before.
		switch gv.V.(type) {
		case complex64, complex128:
			if n := basicNameOf(v.peelNamed(td)); n == "complex64" || n == "complex128" {
				cv, _, _ := asComplex(gv)
				if n == "complex64" {
					x = &runtime.GoValue{V: complex64(cv)}
				} else {
					x = &runtime.GoValue{V: cv}
				}
				if td != nil && declaredType(td) {
					return runtime.Tag(td, x)
				}
			}
		case uint64:
			// a wide uint64 box into an integer slot takes the declared
			// tag like a fitting constant would — `var u uint =
			// 18446744073709551615` is a uint, not a bare uint64.
			if n := basicNameOf(v.peelNamed(td)); sizedIntName(n) {
				return runtime.Tag(td, gv)
			}
		}
		// a host numeric converts to a float/complex slot by value —
		// `var f float64 = 16717361816799281152` keeps its unsigned
		// magnitude instead of binding the GoValue raw. The narrowed
		// float64 flows on through the same rounding and tagging a
		// script float takes.
		if fv, ok := hostFloat(gv); ok {
			switch basicNameOf(v.peelNamed(td)) {
			case "float32", "float64", "complex64", "complex128":
				x = fv
			default:
				return x
			}
		} else {
			return x // host boundary: assignability is unknowable
		}
	}
	if tn, ok := x.(*runtime.TypedNil); ok {
		if sameTypeDef(tn.Typ, td) || v.tdShapeEq(tn.Typ, td) {
			return &runtime.TypedNil{Typ: td} // re-tag to the declared type
		}
		f.trap("cannot use nil %s as %s", tdName(tn.Typ), tdName(td))
	}
	if _, ok := x.(*runtime.IfaceNil); ok {
		f.trap("cannot use interface value as %s", tdName(td))
	}
	// values carrying a declared tag (a named struct's Def, a stamped
	// container Typ) follow the named-to-named rule like *Named —
	// `var a A = sq` traps even though A shares Sq's storage. The trap
	// fires only when the target is named too: an unnamed target
	// (`var m map[string]int = om`) stays a shape check, and anonymous
	// values carry no tag at all.
	utd := v.peelNamed(td)
	if tag := declaredTag(x); tag != nil {
		if sameTypeDef(tag, td) || sameTypeDef(tag, v.peelAlias(td)) {
			return x
		}
		// a named target whose underlying is an interface (`type Token
		// any`) assigns by interface satisfaction below, not by tag —
		// the tag mismatch is only fatal for concrete targets.
		if tagIsNamed(td) && (utd == nil || utd.Kind != runtime.KindInterface) {
			f.trap("cannot use %s as %s", tdName(tag), tdName(td))
		}
	}
	if utd.Kind == runtime.KindPointer && v.H.ElemOf != nil {
		// `var p P = &v` — the pointee's declared type must match the
		// pointer's element type (a named pointer binds only its own
		// pointee type); untyped pointees defer to the shape check.
		if ptag := v.pointeeTag(x); ptag != nil {
			if et, err := v.H.ElemOf(utd); err == nil && et != nil &&
				!sameTypeDef(ptag, et) && !sameTypeDef(ptag, v.peelAlias(et)) {
				f.trap("cannot use %s as %s", "&"+tdName(ptag), tdName(td))
			}
		}
	}
	if utd.Kind == runtime.KindInterface {
		// `type I2 I` — the declared name's method set is the underlying
		// interface's; check and store unboxed, like a direct I slot.
		if !v.satisfiesIface(f, utd, x) {
			f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
		}
		return x
	}
	if !v.shapeOK(f, x, utd) {
		// a raw host value of a bound named basic carries no tag the
		// declared-tag check could see — arithmetic on time.Second
		// yields a bare time.Duration, so a declared target of the same
		// name binds it by name (identical types assign in Go).
		if td.Name != "" && typeNameOf(x) == td.Name {
			return runtime.Tag(td, x)
		}
		f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
	}
	// array-typed slots copy on assignment: `var b = a` owns its own
	// backing array, like Go's value semantics for arrays.
	if _, isArr := v.arrayLen(f, utd); isArr {
		if s, ok := x.(*runtime.Slice); ok {
			x = v.copyArray(f, s, utd)
		}
	}
	// an untyped int constant lands already converted: a float slot
	// takes float64(3) so `var f float64 = 3; f / 2` is 1.5, and a
	// sized-int slot wraps like a conversion (`var x int8 = y` keeps
	// the low byte of a non-constant operand). A bare int64 from an
	// int-typed variable converts too — the VM cannot distinguish the
	// two, and a Named int64 traps in the declared-tag check above.
	if iv, ok := runtime.Unwrap(x).(int64); ok && !declaredType(utd) {
		switch n := tdName(utd); n {
		case "float32":
			x = float64(float32(iv))
		case "float64":
			x = float64(iv)
		case "complex64":
			x = &runtime.GoValue{V: complex64(complex(float64(iv), 0))}
		case "complex128":
			x = &runtime.GoValue{V: complex(float64(iv), 0)}
		default:
			x = maskInt(iv, n)
		}
	}
	// assigning into a float32 slot narrows the way T(x) does —
	// `var f float32 = 0.1` stores the float64 that reads back as
	// float32(0.1), not the source literal's extra bits.
	if fv, ok := runtime.Unwrap(x).(float64); ok {
		switch basicNameOf(utd) {
		case "float32":
			x = float64(float32(fv))
		case "complex64":
			x = &runtime.GoValue{V: complex64(complex(fv, 0))}
		case "complex128":
			x = &runtime.GoValue{V: complex(fv, 0)}
		}
	}
	switch utd.Kind {
	case runtime.KindMap, runtime.KindSlice, runtime.KindChan:
		if ct := containerTyp(x); ct == nil {
			// a declared container type stamps the value so element
			// reads/writes coerce and missing-key reads yield the
			// declared element zero instead of NIL.
			x = stampContainerTyp(x, td)
		} else if !sameTypeDef(ct, td) {
			// two named container types do not re-bind (Go: named-to-named
			// needs a conversion); anonymous/underlying shapes may
			// re-bind only when the shapes match element-for-element.
			if ct.Name != "" && td.Name != "" {
				f.trap("cannot use %s as %s", tdName(ct), tdName(td))
			}
			if !v.tdShapeEval(f, ct, td) {
				f.trap("cannot use %s as %s", tdName(ct), tdName(td))
			}
			// an assignable value takes the slot's declared type: `var n
			// nat = []Word{...}` reads back as nat (its method set), not
			// as the source value's anonymous []Word tag.
			x = stampContainerTyp(x, td)
		}
	}
	switch td.Kind {
	case runtime.KindNamedBasic:
		// declared types and sized-int builtins tag the bound value —
		// without the tag, arithmetic on `var x uint8` loses its width
		// (maskInt at bind time wraps the constant, but -x has nothing
		// to re-wrap against). int64 and float32 tag too, so %T spells
		// the declared width; int and float64 stay bare since the bare
		// value already spells them.
		if declaredType(td) || sizedIntName(td.Name) ||
			td.Name == "int64" || td.Name == "float32" {
			return runtime.Tag(td, x)
		}
	case runtime.KindPointer, runtime.KindFunc:
		// declared pointer/func types tag the bound value so asserts
		// check declared identity and member access sees only the
		// declared method set. Anonymous *T/func() binds stay bare —
		// they carry the pointee's members (T's method set promotes).
		if td.Spec != nil {
			return runtime.Tag(td, x)
		}
	}
	return x
}

// peelAlias follows only Alias links, one hop at a time: `type A = B`
// is the same type as B, while `type A B` defines a distinct named type
// — peeling past it would let a B-tagged value bind an A slot and keep
// the wrong tag. Underlying is transitive (it crosses `type A B` too),
// so this needs the dedicated hook; without it only sameTypeDef runs.
func (v *VM) peelAlias(td *runtime.TypeDef) *runtime.TypeDef {
	if v.H.AliasOf == nil {
		return td
	}
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		u, err := v.H.AliasOf(td)
		if err != nil || u == nil || u == td {
			break
		}
		td = u
	}
	return td
}

// peelNamed follows a typedef through Alias and `type A B` (NamedBasic)
// chains to the typedef that gives its storage shape — builtin or
// composite — stopping on unresolvable references.
func (v *VM) peelNamed(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && (td.Kind == runtime.KindAlias || td.Kind == runtime.KindNamedBasic) && v.H.Underlying != nil && i < 32; i++ {
		u, err := v.H.Underlying(td)
		if err != nil || u == nil || u == td {
			break
		}
		td = u
	}
	return td
}

// nilableTypedef reports whether a typedef's values can be nil at all —
// declared pointers, slices, maps, chans, funcs and interfaces have a
// nil zero, so a nil receiver of one of these types still binds as a
// method receiver (the body decides). Structs and basics cannot be nil.
func (v *VM) nilableTypedef(td *runtime.TypeDef) bool {
	u := v.peelNamed(td)
	if u == nil {
		return false
	}
	switch u.Kind {
	case runtime.KindPointer, runtime.KindSlice, runtime.KindMap,
		runtime.KindChan, runtime.KindFunc, runtime.KindInterface:
		return true
	}
	return false
}

// tdShapeEq reports whether two typedefs have the same underlying shape —
// for typed-nil retagging (`var s S = ([]int)(nil)` needs S ~ []int).
// Shape spelling normalizes byte/rune and resolves bound type params
// (convShapeEq), so []byte and []uint8 count as one shape.
func (v *VM) tdShapeEq(a, b *runtime.TypeDef) bool {
	pa, pb := v.peelNamed(a), v.peelNamed(b)
	if pa == pb {
		return true
	}
	if pa == nil || pb == nil || pa.Kind != pb.Kind {
		return false
	}
	if sameTypeDef(pa, pb) {
		return true
	}
	if v.convShapeEq(pa, pb) {
		return true
	}
	if pa.Anon != nil && pb.Anon != nil {
		return runtime.TypSpelling(pa.Anon, pa) == runtime.TypSpelling(pb.Anon, pb)
	}
	return pa.Anon == nil && pb.Anon == nil
}

// tdShapeEval is tdShapeEq with evaluated array lengths: `[len(x)]*T`
// and `[3]*T` spell differently but name the same type once len(x)
// folds. Only reached when the plain spelling compare failed.
func (v *VM) tdShapeEval(f *frame, a, b *runtime.TypeDef) bool {
	if v.tdShapeEq(a, b) {
		return true
	}
	na, aok := v.arrayLen(f, a)
	nb, bok := v.arrayLen(f, b)
	if !aok || !bok || na != nb {
		return false
	}
	ea := v.elemTypedef(f, a)
	eb := v.elemTypedef(f, b)
	if ea == nil || eb == nil {
		return ea == nil && eb == nil
	}
	return v.tdShapeEval(f, ea, eb)
}

// structFieldsEq compares two struct typedefs by field name lists —
// the approximate "identical underlying" for an unnamed struct literal
// binding a named type (Go also requires matching tags and element
// types; neither is modeled here).
func structFieldsEq(a, b *runtime.TypeDef) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for i := range a.Fields {
		if a.Fields[i] != b.Fields[i] {
			return false
		}
	}
	return true
}

// shapeOK checks a bare value against a fully-peeled (non-named) typedef:
// basic families match by kind, structs by typedef identity, containers by
// kind. Unverifiable cases pass — the check is assignability, not typing.
func (v *VM) shapeOK(f *frame, x runtime.Value, td *runtime.TypeDef) bool {
	switch td.Kind {
	case runtime.KindNamedBasic, runtime.KindAlias:
		name := td.Name
		if name == "" {
			if id, ok := td.Anon.(*ast.Ident); ok {
				name = id.Name
			}
		}
		switch name {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
			_, ok := x.(int64)
			return ok
		case "float32", "float64":
			// int64 is accepted too: the VM cannot distinguish an untyped
			// constant (`var f float64 = 1`) from an int-typed variable.
			switch x.(type) {
			case int64, float64:
				return true
			}
			return false
		case "complex64", "complex128":
			// same cannot-distinguish rule: numerics promote to complex.
			switch x.(type) {
			case int64, float64:
				return true
			}
			if gv, ok := x.(*runtime.GoValue); ok {
				switch gv.V.(type) {
				case complex64, complex128:
					return true
				}
			}
			return false
		case "string":
			_, ok := x.(string)
			return ok
		case "bool":
			_, ok := x.(bool)
			return ok
		}
		return true // unknown underlying name — pass through
	case runtime.KindStruct:
		s, ok := x.(*runtime.Struct)
		if !ok {
			return false
		}
		if s.Def == nil || td.Name == "" {
			return true // anonymous shape — approximated ok
		}
		if sameTypeDef(s.Def, td) {
			return true
		}
		// an unnamed struct value binds a named struct type when the
		// field sets match (Go: identical underlying, V unnamed ->
		// assignable); a NAMED Def never reaches here — the declared-tag
		// check in coerceConcrete decides those binds. Field tags are
		// not tracked, so the comparison is names only.
		if !tagIsNamed(s.Def) {
			return structFieldsEq(s.Def, td)
		}
		return false
	case runtime.KindSlice:
		_, ok := x.(*runtime.Slice)
		return ok
	case runtime.KindMap:
		_, ok := x.(*runtime.Map)
		return ok
	case runtime.KindChan:
		_, ok := x.(*runtime.Chan)
		return ok
	case runtime.KindFunc:
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			return true
		}
		return false
	case runtime.KindPointer:
		_, ok := runtime.Deref(x)
		return ok
	}
	return true
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

// Zero implements the VMCaller hook for the new() builtin.
func (v *VM) Zero(td *runtime.TypeDef) runtime.Value {
	return v.zeroSeen(nil, td, map[*runtime.TypeDef]bool{})
}

// ElemZero returns the element-type zero of a container typedef — the
// value make() fills a new slice with.
// topFrame returns the innermost running frame — the caller whose locals
// may hold a function-local typedef — or nil outside a call.
func (v *VM) topFrame() *frame {
	if n := len(v.frames); n > 0 {
		return v.frames[n-1]
	}
	return nil
}

func (v *VM) ElemZero(td *runtime.TypeDef) runtime.Value {
	et := v.elemTypedef(v.topFrame(), td)
	if et == nil {
		return runtime.NIL
	}
	return v.zeroSeen(nil, et, map[*runtime.TypeDef]bool{})
}

// ArrayLenOf exposes arrayLen to builtins through VMCaller — len()/cap()
// on a nil *[N]T needs the constant length without a live frame.
func (v *VM) ArrayLenOf(td *runtime.TypeDef) (int64, bool) {
	return v.arrayLen(v.topFrame(), td)
}

// CallerPCs implements VMCaller: it snapshots the call stack — live
// frames (top first) with the frames the in-flight panic already unwound
// spliced in at the unwind boundary, where Go's traceback lists them:
// between the deferred-call chain and the still-live frames below.
func (v *VM) CallerPCs() []uintptr {
	sites := make([]runtime.CallSite, 0, len(v.frames)+len(v.unwinding))
	depth := len(v.frames)
	// unwinding stays populated through the drain even after the panic is
	// consumed, so the splice point is unwindDepth whenever it is non-empty.
	if len(v.unwinding) > 0 && v.unwindDepth < depth {
		depth = v.unwindDepth
	}
	for i := len(v.frames) - 1; i >= depth; i-- {
		sites = append(sites, v.callSite(v.frames[i]))
	}
	// unwound frames list grouped by their panic, newest unwind first:
	// a superseding panic's frames precede the superseded panic's
	// leftovers, matching Go's per-panic traceback order. Groups are
	// keyed by panic and ordered by their last pop.
	seen := map[*runtime.Panic]bool{}
	for i := len(v.unwinding) - 1; i >= 0; i-- {
		pn := v.unwinding[i].pn
		if seen[pn] {
			continue
		}
		seen[pn] = true
		for _, e := range v.unwinding {
			if e.pn == pn {
				sites = append(sites, v.callSite(e.f))
			}
		}
	}
	for i := depth - 1; i >= 0; i-- {
		sites = append(sites, v.callSite(v.frames[i]))
	}
	base := len(v.pcSites)
	v.pcSites = append(v.pcSites, sites...)
	pcs := make([]uintptr, len(sites))
	for i := range pcs {
		pcs[i] = uintptr(base + i + 1)
	}
	return pcs
}

// CallerFrame implements VMCaller: resolve a handle from CallerPCs.
func (v *VM) CallerFrame(pc uintptr) (runtime.CallSite, bool) {
	if pc == 0 || int(pc) > len(v.pcSites) {
		return runtime.CallSite{}, false
	}
	return v.pcSites[pc-1], true
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
	n, ok := v.arrayLen(f, &runtime.TypeDef{Anon: at, Kind: runtime.KindSlice})
	if !ok {
		return nil, false
	}
	return int64(n), true
}

// callSite renders one frame for runtime.Callers: the function's Go
// symbol name and the source position it is (or was) executing.
func (v *VM) callSite(f *frame) runtime.CallSite {
	site := runtime.CallSite{Name: f.fn.Name}
	pos := f.pos()
	if !pos.IsValid() && f.fn.Decl != nil {
		pos = f.fn.Decl.Pos()
	}
	if pos.IsValid() && f.fn.Pkg != nil && f.fn.Pkg.Fset != nil {
		p := f.fn.Pkg.Fset.Position(pos)
		site.File = p.Filename
		site.Line = p.Line
	}
	return site
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
	at, ok := td.Anon.(*ast.ArrayType)
	if !ok || at.Len == nil {
		return 0, false
	}
	switch l := at.Len.(type) {
	case *ast.BasicLit:
		n, err := strconv.ParseInt(l.Value, 0, 64)
		return n, err == nil
	case *ast.Ident:
		// a named const: its value lives in the package env once init
		// ran (Materialize returns NIL for const decls); fall back to the
		// index for a const declared below the use or not yet bound.
		if td.Pkg != nil {
			if gv, ok := td.Pkg.Globals.Get(l.Name); ok {
				if d, ok2 := runtime.Deref(gv); ok2 {
					gv = d
				}
				if n, ok2 := lenConstInt(gv); ok2 {
					return v.foldArrLen(at, n), true
				}
			}
			if td.Pkg.Index != nil && v.H.Materialize != nil {
				if d := td.Pkg.Index.Consts[l.Name]; d != nil {
					if mv, err := v.H.Materialize(td.Pkg, d); err == nil {
						if n, ok := lenConstInt(mv); ok {
							return v.foldArrLen(at, n), true
						}
					}
				}
			}
		}
	}
	// any other constant form ([N*2]int, [N+1]int): compile the length
	// expression and run it in the typedef's package scope — const
	// names resolve through globals like a normal expression. A
	// non-constant shape ([...]T's Ellipsis, a call) can't evaluate;
	// its trap is swallowed so the typedef stays non-array (ok=false).
	if td.Pkg != nil && v.H.CompileExpr != nil && lenConstShaped(at.Len) {
		var n int64
		var ok bool
		func() {
			defer func() { _ = recover() }()
			if ch, err := v.H.CompileExpr(td.Pkg, td.File, at.Len); err == nil && ch != nil {
				if r, err2 := v.call(&runtime.Function{Pkg: td.Pkg, File: td.File, Name: "<arraylen>", Chunk: ch}, nil, nil, nil); err2 == nil {
					if iv, ok2 := lenConstInt(r); ok2 {
						n, ok = iv, true
					}
				}
			}
		}()
		if ok {
			return v.foldArrLen(at, n), true
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
	case *ast.CallExpr:
		// len/cap of an array is a constant expression — `len(a)`
		// and `cap(a)` fold to the array's size; other calls are
		// non-constant and Go rejects them.
		id, ok := x.Fun.(*ast.Ident)
		return ok && (id.Name == "len" || id.Name == "cap")
	}
	return false
}

// foldArrLen rewrites an array typedef's length expression to the
// evaluated count so type spellings — `[3]*T`, `[N]T` — compare
// identically after folding, like Go's constant evaluation.
func (v *VM) foldArrLen(at *ast.ArrayType, n int64) int64 {
	at.Len = &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)}
	return n
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
	if td == nil {
		return ""
	}
	if sizedIntName(td.Name) {
		return td.Name
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if id, ok := x.(*ast.Ident); ok && sizedIntName(id.Name) {
		return id.Name
	}
	return ""
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

// builtinTypeName reports whether name is a predeclared basic type —
// the names a conversion or var decl may tag a Named value with.
func builtinTypeName(name string) bool {
	switch name {
	case "int", "int64", "bool", "string", "error",
		"float32", "float64", "complex64", "complex128",
		"int8", "int16", "int32", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

// basicNameOf resolves the underlying builtin basic-type name behind a
// typedef the way sizedNameOf resolves ints: `type F32 float32` and the
// bare float32 typedef both read "float32".
func basicNameOf(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if builtinTypeName(td.Name) {
		return td.Name
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if id, ok := x.(*ast.Ident); ok && builtinTypeName(id.Name) {
		return id.Name
	}
	return ""
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

// typeNameOf spells x's dynamic type the way Go names it in conversion
// panics: pointer boxes count one leading star per hop (*runtime.Cell →
// *main.Point) and container/func/typed-nil leaves spell their declared
// typedef ([]int, map[string]int, *main.Point) instead of the runtime
// Go struct name.
func typeNameOf(x runtime.Value) string {
	depth := 0
	for {
		switch x.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			depth++
		case *runtime.DerefRef:
			// *p is a view of base's pointee — the pointer layer
			// belongs to the base's type, not the ref.
		default:
			goto base
		}
		dv, ok := runtime.Deref(x)
		if !ok {
			goto base
		}
		x = dv
	}
base:
	name := typeBaseName(x)
	for ; depth > 0; depth-- {
		name = "*" + name
	}
	return name
}

// typeBaseName spells a non-reference value's dynamic type.
func typeBaseName(x runtime.Value) string {
	switch xv := x.(type) {
	case *runtime.UConst:
		return xv.DefaultName()
	case *runtime.Named:
		if in, ok := xv.V.(*runtime.IfaceNil); ok && in.Typ != nil && in.Typ.Kind == runtime.KindInterface {
			// a nil interface value wrapped in its declared tag still
			// has no dynamic type.
			return "<nil>"
		}
		return spelledTyp(xv.Typ)
	case *runtime.Struct:
		if xv.Def != nil {
			return spelledTyp(xv.Def)
		}
		return "struct"
	case *runtime.TypedNil:
		return spelledTyp(xv.Typ)
	case *runtime.IfaceNil:
		if xv.Typ != nil {
			if xv.Typ.Kind == runtime.KindInterface {
				// a nil interface value has no dynamic type.
				return "<nil>"
			}
			return spelledTyp(xv.Typ)
		}
		return "nil"
	case *runtime.Slice:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "slice"
	case *runtime.Map:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "map"
	case *runtime.Chan:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "chan"
	case *runtime.Function:
		return funcTypeName(xv)
	case *runtime.Closure:
		return funcTypeName(xv.Fn)
	case *runtime.BoundMethod:
		return funcTypeName(xv.Fn)
	case *runtime.BuiltinFunc:
		if m := xv.Method; m != nil {
			return m.Type.String()
		}
		if xv.Target != nil {
			return fmt.Sprintf("%T", xv.Target)
		}
		return "func"
	case *runtime.GoValue:
		// a host box names its Go type — "*errors.errorString" in a
		// conversion panic, like the real runtime prints it.
		return fmt.Sprintf("%T", xv.V)
	case int64:
		// the bare int64 is Go's int; sized ints arrive as Named{int64}
		// and spell themselves through spelledTyp above.
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case runtime.Nil:
		return "nil"
	default:
		return fmt.Sprintf("%T", x)
	}
}

// funcTypeName renders a script function's declared signature for the
// panic text — Go spells `func(int)`, `func() error` and friends.
func funcTypeName(xv *runtime.Function) string {
	if s, ok := runtime.FuncGoSpelling(xv); ok {
		return s
	}
	return "func"
}

// instantiate implements F[T, U] / T[Args] on generic functions and types;
// a non-generic base falls back to index lookup so `a[i]` and `F[T]` share
// one encoding.
func (v *VM) instantiate(f *frame, base runtime.Value, targs []runtime.Value, pos token.Pos) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		base = dv
	}
	switch g := base.(type) {
	case *runtime.Function:
		if len(g.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		if len(targs) != len(g.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Name, len(g.TParams), len(targs))
		}
		return v.instantiateFunc(f, g, targs)
	case *runtime.BoundMethod:
		// a generic method explicit instantiation: recv.M[T](...) — the
		// receiver's own type binds are kept, T binds the method's params.
		if len(g.Fn.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		if len(targs) != len(g.Fn.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Fn.Name, len(g.Fn.TParams), len(targs))
		}
		return &runtime.BoundMethod{Recv: g.Recv, Fn: v.instantiateFunc(f, g.Fn, targs)}
	case *runtime.BuiltinFunc:
		if g.GenFn == nil {
			return v.indexFallback(f, base, targs)
		}
		gen := g.GenFn
		return &runtime.BuiltinFunc{
			Name: g.Name,
			Pkg:  g.Pkg,
			Fn: func(vm runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
				return gen(vm, targs, args)
			},
		}
	case *runtime.TypeDef:
		if len(g.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		if len(targs) != len(g.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Name, len(g.TParams), len(targs))
		}
		binds := map[string]runtime.Value{}
		for i, tp := range g.TParams {
			binds[tp] = targs[i]
		}
		ctx := &runtime.TypeDef{Pkg: g.Pkg, File: g.File, Binds: binds}
		if err := v.checkTArgs(ctx, g.TParams, g.TConstraints, binds); err != nil {
			f.trap("%s", err)
		}
		return v.specializeType(g, targs)
	default:
		return v.indexFallback(f, base, targs)
	}
}

// instantiateFunc binds a generic function's own type parameters to the
// explicit type arguments — merging over binds already carried by the
// value (a generic method keeps its receiver's type binds).
func (v *VM) instantiateFunc(f *frame, g *runtime.Function, targs []runtime.Value) *runtime.Function {
	binds := map[string]runtime.Value{}
	for k, bv := range g.Binds {
		binds[k] = bv
	}
	for i, tp := range g.TParams {
		binds[tp] = targs[i]
	}
	ctx := &runtime.TypeDef{Pkg: g.Pkg, File: g.File, Binds: binds}
	if err := v.checkTArgs(ctx, g.TParams, g.TConstraints, binds); err != nil {
		f.trap("%s", err)
	}
	return &runtime.Function{
		Pkg: g.Pkg, File: g.File, Decl: g.Decl, Name: g.Name,
		Recv: g.Recv, PtrRecv: g.PtrRecv,
		TParams: g.TParams, TConstraints: g.TConstraints,
		Binds: binds, Compile: g.Compile,
	}
}

// indexFallback applies plain index semantics when the [..] was not a
// generic instantiation after all (single index only).
func (v *VM) indexFallback(f *frame, base runtime.Value, targs []runtime.Value) runtime.Value {
	if len(targs) != 1 {
		f.trap("cannot index %T with %d indices", base, len(targs))
	}
	return v.index(f, base, targs[0])
}

// specializeType clones a generic typedef with its methods re-bound to the
// concrete type arguments.
func (v *VM) specializeType(g *runtime.TypeDef, targs []runtime.Value) *runtime.TypeDef {
	binds := map[string]runtime.Value{}
	for i, tp := range g.TParams {
		if i < len(targs) {
			binds[tp] = targs[i]
		}
	}
	td := &runtime.TypeDef{
		Pkg: g.Pkg, Name: g.Name, File: g.File, Spec: g.Spec, Kind: g.Kind,
		Fields: g.Fields, FTags: g.FTags, Anon: g.Anon, TParams: g.TParams,
		TConstraints: g.TConstraints, Binds: binds,
		MReqs: g.MReqs, IEmbeds: g.IEmbeds,
		EmbedSpecs: g.EmbedSpecs, EmbedIdx: g.EmbedIdx, Embeds: g.Embeds,
		LocalTypes: g.LocalTypes, Elem: g.Elem, HostNew: g.HostNew,
		Local: g.Local,
	}
	if len(g.Methods) > 0 {
		td.Methods = make(map[string]*runtime.Function, len(g.Methods))
		for name, m := range g.Methods {
			binds := map[string]runtime.Value{}
			for k, bv := range m.Binds {
				binds[k] = bv
			}
			for i, tp := range g.TParams {
				binds[tp] = targs[i]
			}
			// the receiver may rename the type's parameters — `func (l
			// List[E])` on `type List[T]` scopes E in the method body, so
			// the receiver's own names bind to the same arguments.
			for i, rp := range recvTypeParamNames(m.Decl) {
				if rp == "_" {
					continue // `_` names no parameter to bind
				}
				if i < len(targs) {
					binds[rp] = targs[i]
				}
			}
			td.Methods[name] = &runtime.Function{
				Pkg: m.Pkg, File: m.File, Decl: m.Decl, Name: m.Name,
				Recv: m.Recv, PtrRecv: m.PtrRecv,
				TParams: m.TParams, TConstraints: m.TConstraints,
				Binds: binds, Compile: m.Compile,
			}
		}
	}
	return td
}

// recvTypeParamNames extracts the type parameter names a method receiver
// declares — `func (l List[E, F])` yields [E, F]. Non-generic receivers
// yield nil.
func recvTypeParamNames(d *ast.FuncDecl) []string {
	if d == nil || d.Recv == nil || len(d.Recv.List) == 0 {
		return nil
	}
	var idxs []ast.Expr
	switch t := d.Recv.List[0].Type.(type) {
	case *ast.IndexExpr:
		idxs = []ast.Expr{t.Index}
	case *ast.IndexListExpr:
		idxs = t.Indices
	}
	var out []string
	for _, x := range idxs {
		if id, ok := x.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
	}
	return out
}

// ---- special forms ----

// specialCtx implements runtime.SpecialContext: the surface a special-form
// handler sees of the caller's frame.
type specialCtx struct {
	v *VM
	f *frame
	q *runtime.QuotedCall
}

func (s *specialCtx) Position(n ast.Node) token.Position {
	if s.f.fn.Pkg != nil && s.f.fn.Pkg.Fset != nil {
		return s.f.fn.Pkg.Fset.Position(n.Pos())
	}
	return token.Position{}
}

func (s *specialCtx) File() *syntax.File        { return s.q.File }
func (s *specialCtx) Package() *runtime.Package { return s.f.fn.Pkg }

func (s *specialCtx) Format(n ast.Node) string {
	var b strings.Builder
	var fset *token.FileSet
	if s.f.fn.Pkg != nil {
		fset = s.f.fn.Pkg.Fset
	}
	if fset == nil {
		fset = token.NewFileSet()
	}
	if err := format.Node(&b, fset, n); err != nil {
		return fmt.Sprintf("<bad node %T>", n)
	}
	return b.String()
}

func (s *specialCtx) Call(fn runtime.Value, args []runtime.Value) (runtime.Value, error) {
	return s.v.Call(fn, args)
}

// ResolveSymbol maps an expression to its canonical SymbolID through the
// caller file's import scope — no package is materialized, matching the
// plan's index-level laziness for special forms.
func (s *specialCtx) ResolveSymbol(e ast.Expr) (runtime.SymbolID, error) {
	pkg := s.f.fn.Pkg
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return runtime.SymbolID{}, fmt.Errorf("cannot resolve %s to a symbol", s.Format(e))
		}
		// a local or captured variable may shadow an import name: selector
		// expressions on it are member access, not package symbols
		if _, ok := s.q.Locals[id.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x.X, "%s is a local variable, not an import alias", id.Name)
		}
		if _, ok := s.q.Upvals[id.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x.X, "%s is a captured variable, not an import alias", id.Name)
		}
		if pkg != nil {
			if refs, ok := pkg.Scopes[s.q.File]; ok {
				if ref, ok := refs[id.Name]; ok {
					return runtime.SymbolID{PackagePath: ref.Path, Name: x.Sel.Name}, nil
				}
			}
		}
		return runtime.SymbolID{}, s.Errorf(x, "%s is not an import alias in this file", id.Name)
	case *ast.Ident:
		if _, ok := s.q.Locals[x.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x, "%s is a local variable, not a package symbol", x.Name)
		}
		if _, ok := s.q.Upvals[x.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x, "%s is a captured variable, not a package symbol", x.Name)
		}
		path := ""
		if pkg != nil {
			path = pkg.Path
		}
		return runtime.SymbolID{PackagePath: path, Name: x.Name}, nil
	default:
		return runtime.SymbolID{}, s.Errorf(e, "cannot resolve %T to a symbol", e)
	}
}

// Resolve maps a symbol expression to its runtime value through ordinary
// name resolution — locals/upvals first, then globals (materializing only
// the named decl), then a pkg.Sym selector via the file's import table.
// Non-symbol expressions are rejected, keeping resolution declaration-
// level lazy: no arbitrary code runs that Eval would allow.
func (s *specialCtx) Resolve(e ast.Expr) (runtime.Value, error) {
	switch x := e.(type) {
	case *ast.Ident:
		if slot, ok := s.q.Locals[x.Name]; ok {
			if slot >= len(s.f.locals) || s.f.locals[slot] == nil {
				return nil, s.Errorf(x, "local %s is not bound", x.Name)
			}
			return s.f.locals[slot].Elem, nil
		}
		if idx, ok := s.q.Upvals[x.Name]; ok {
			if idx >= len(s.f.upvals) || s.f.upvals[idx] == nil {
				return nil, s.Errorf(x, "upvalue %s is not bound", x.Name)
			}
			return s.f.upvals[idx].Elem, nil
		}
		mv, err := s.v.resolveGlobalE(s.f, x.Name)
		if err != nil {
			return nil, s.Errorf(x, "%s", err)
		}
		return mv, nil
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, s.Errorf(e, "cannot resolve %s to a value", s.Format(e))
		}
		if _, ok := s.q.Locals[id.Name]; ok {
			return nil, s.Errorf(x.X, "%s is a local variable, not an import alias", id.Name)
		}
		if _, ok := s.q.Upvals[id.Name]; ok {
			return nil, s.Errorf(x.X, "%s is a captured variable, not an import alias", id.Name)
		}
		pkg := s.f.fn.Pkg
		if pkg == nil {
			return nil, s.Errorf(x, "no package context")
		}
		ref, ok := pkg.Scopes[s.q.File][id.Name]
		if !ok {
			return nil, s.Errorf(x, "%s is not an import alias in this file", id.Name)
		}
		p, err := ref.Materialize()
		if err != nil {
			return nil, err
		}
		mv, err := s.v.memberOf(p, x.Sel.Name)
		if err != nil {
			return nil, err
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			return c.Elem, nil
		}
		return mv, nil
	default:
		return nil, s.Errorf(e, "cannot resolve %T to a value", e)
	}
}

// ResolveType resolves a type expression to its *TypeDef: named types
// through Resolve, composite forms as the same anonymous typedefs the
// compiler emits for declared types (Anon specs keep element types
// lazily resolved on use).
func (s *specialCtx) ResolveType(e ast.Expr) (*runtime.TypeDef, error) {
	pkg := s.f.fn.Pkg
	switch t := e.(type) {
	case *ast.Ident:
		// Inside a generic instantiation a bare ident may name a type
		// parameter: resolve it through the function's binds first.
		if td, ok := s.f.fn.Binds[t.Name]; ok {
			if td2, ok := td.(*runtime.TypeDef); ok {
				return td2, nil
			}
			return nil, s.Errorf(e, "%s is bound to a non-type", t.Name)
		}
		mv, err := s.Resolve(e)
		if err != nil {
			return nil, err
		}
		td, ok := mv.(*runtime.TypeDef)
		if !ok {
			return nil, s.Errorf(e, "%s is not a type", s.Format(e))
		}
		return td, nil
	case *ast.SelectorExpr:
		mv, err := s.Resolve(e)
		if err != nil {
			return nil, err
		}
		td, ok := mv.(*runtime.TypeDef)
		if !ok {
			return nil, s.Errorf(e, "%s is not a type", s.Format(e))
		}
		return td, nil
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.MapType:
		return &runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.ChanType:
		return &runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.FuncType:
		return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.StructType:
		td := &runtime.TypeDef{Kind: runtime.KindStruct, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}
		td.FTags = runtime.StructFieldTags(t)
		for _, fld := range t.Fields.List {
			if len(fld.Names) == 0 {
				td.EmbedSpecs = append(td.EmbedSpecs, fld.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, embeddedFieldName(fld.Type))
				continue
			}
			for _, n := range fld.Names {
				td.Fields = append(td.Fields, n.Name)
			}
		}
		return td, nil
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Kind: runtime.KindInterface, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				td.IEmbeds = append(td.IEmbeds, m.Type)
				continue
			}
			for _, n := range m.Names {
				td.MReqs = append(td.MReqs, n.Name)
			}
		}
		return td, nil
	case *ast.ParenExpr:
		return s.ResolveType(t.X)
	case *ast.IndexExpr:
		return s.instantiateType(e, t.X, []ast.Expr{t.Index})
	case *ast.IndexListExpr:
		return s.instantiateType(e, t.X, t.Indices)
	case *ast.Ellipsis:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: t.Pos(), Elt: t.Elt}, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	default:
		return nil, s.Errorf(e, "unsupported type expression %T", e)
	}
}

// instantiateType resolves the T of T[Args] and each argument typedef,
// then instantiates through the VM's generic binder.
func (s *specialCtx) instantiateType(e ast.Expr, x ast.Expr, argExprs []ast.Expr) (*runtime.TypeDef, error) {
	base, err := s.Resolve(x)
	if err != nil {
		return nil, err
	}
	targs := make([]runtime.Value, len(argExprs))
	for i, a := range argExprs {
		td, err := s.ResolveType(a)
		if err != nil {
			return nil, err
		}
		targs[i] = td
	}
	iv := s.v.instantiate(s.f, base, targs, e.Pos())
	td, ok := iv.(*runtime.TypeDef)
	if !ok {
		return nil, s.Errorf(e, "%s is not a type", s.Format(e))
	}
	return td, nil
}

// embeddedFieldName derives the field name of an anonymous (embedded)
// struct field: the base type name, ignoring pointers, packages and
// type args. Mirrors compile's embedFieldName.
func embeddedFieldName(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embeddedFieldName(t.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(t.X)
	}
	return ""
}

// Eval compiles expr against the caller's live scope (locals/upvals snap-
// shotted at the special call site) and runs it in a fresh frame sharing
// the caller's cells — writes by the quoted expr are visible to the caller.
func (s *specialCtx) Eval(e ast.Expr) (runtime.Value, error) {
	if s.v.H.CompileScopedExpr == nil {
		return nil, fmt.Errorf("no scoped-expression compiler")
	}
	ch, err := s.v.H.CompileScopedExpr(s.f.fn.Pkg, s.q.File, e, s.q.Locals, s.q.Upvals)
	if err != nil {
		return nil, err
	}
	fr := &frame{
		fn:     &runtime.Function{Pkg: s.f.fn.Pkg, File: s.q.File, Name: "<special-eval>"},
		ch:     ch,
		locals: make([]*runtime.Cell, ch.NLocals),
		upvals: s.f.upvals,
	}
	// share the caller's cells — slot indices line up by construction
	n := len(s.f.locals)
	if n > len(fr.locals) {
		n = len(fr.locals)
	}
	copy(fr.locals, s.f.locals[:n])
	for i := n; i < len(fr.locals); i++ {
		fr.locals[i] = &runtime.Cell{Elem: runtime.NIL}
	}
	s.v.exec(fr)
	if len(fr.stack) == 0 {
		return runtime.NIL, nil
	}
	return fr.stack[len(fr.stack)-1], nil
}

func (s *specialCtx) Errorf(n ast.Node, formatStr string, args ...any) error {
	return fmt.Errorf("%s: %s", s.Position(n), fmt.Sprintf(formatStr, args...))
}

// ---- call-site generic inference + constraint checks ----

// hasUnbound reports whether any of the given type params lacks a bind.
func hasUnbound(tparams []string, binds map[string]runtime.Value) bool {
	for _, tp := range tparams {
		if _, ok := binds[tp]; !ok {
			return true
		}
	}
	return false
}

// inferBinds binds a generic function's unbound type parameters from the
// runtime argument types — `Id(40)` infers T=int the way v1's heuristic
// did, and (Go 1.27) deeper param shapes infer structurally: `f func(E) R`
// against a func(int) string argument binds R=string. A method's receiver
// occupies args[0]; its own type binds (from the receiver type's
// instantiation) are kept, only the method's type params infer. Params
// that stay unbound resolve to a run-time trap on use, matching the
// compiler-is-total contract.
func (v *VM) inferBinds(fn *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (*runtime.Function, error) {
	if fn.Decl == nil || fn.Decl.Type == nil || fn.Decl.Type.Params == nil {
		return fn, nil
	}
	tset := map[string]bool{}
	for _, t := range fn.TParams {
		if _, ok := fn.Binds[t]; !ok {
			tset[t] = true
		}
	}
	if len(tset) == 0 {
		return fn, nil
	}
	binds := map[string]runtime.Value{}
	for k, bv := range fn.Binds {
		binds[k] = bv
	}
	// ctx resolves named type expressions in the callee's own scope — its
	// package, file imports and binds so far.
	ctx := &runtime.TypeDef{Pkg: fn.Pkg, File: fn.File, Binds: binds}
	pos := 0
	if fn.Decl.Recv != nil {
		pos = 1 // args[0] is the receiver; declared params exclude it
	}
	for _, field := range fn.Decl.Type.Params.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		et := field.Type
		variadic := false
		if el, ok := et.(*ast.Ellipsis); ok {
			et = el.Elt
			variadic = true
		}
		for i := 0; i < n; i++ {
			if pos >= len(args) {
				break
			}
			v.unifyType(ctx, tset, binds, et, args[pos], staticAt(statics, pos))
			pos++
		}
		// ...T consumes all remaining args; the first arg that yields a
		// typedef wins the binding.
		for variadic && pos < len(args) {
			v.unifyType(ctx, tset, binds, et, args[pos], staticAt(statics, pos))
			pos++
		}
		if variadic && spreadTd != nil {
			// `Sum(n...)` spreads n's elements away, but the slice's
			// declared element type still teaches the bind — a nil or
			// empty []int expands to zero args yet infers T=int.
			v.unifyTypeDef(ctx, tset, binds, et, spreadTd)
		}
	}
	if len(binds) == len(fn.Binds) {
		return fn, nil // nothing inferred
	}
	if err := v.checkTArgs(ctx, fn.TParams, fn.TConstraints, binds); err != nil {
		return nil, err
	}
	return &runtime.Function{
		Pkg: fn.Pkg, File: fn.File, Decl: fn.Decl, Name: fn.Name,
		Recv: fn.Recv, PtrRecv: fn.PtrRecv,
		TParams: fn.TParams, TConstraints: fn.TConstraints,
		Binds: binds, Compile: fn.Compile,
	}, nil
}

// unifyType learns type-argument binds by walking a parameter's declared
// type expression (the pattern, which may mention tparams) alongside the
// argument's runtime type. tset holds the tparam names still unbound;
// inferred binds land in binds. Everything is best-effort: mismatched or
// unsupported shapes simply teach nothing, and already-bound tparams are
// not re-bound (no consistency check — approximation).
func (v *VM) unifyType(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat ast.Expr, arg runtime.Value, static *runtime.TypeDef) {
	// Go infers T from the argument's static type, so a declared typedef
	// from the call site wins over the value's dynamic one — `id(e)`
	// with `var e error` binds T=error even when e holds *errorString.
	conc := static
	if conc == nil {
		conc = v.argTypedef(arg)
	}
	if conc == nil {
		return
	}
	v.unifyTypeDef(ctx, tset, binds, pat, conc)
}

func staticAt(statics []*runtime.TypeDef, i int) *runtime.TypeDef {
	if i >= 0 && i < len(statics) {
		return statics[i]
	}
	return nil
}

func (v *VM) unifyTypeDef(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat ast.Expr, conc *runtime.TypeDef) {
	switch p := pat.(type) {
	case *ast.Ident:
		if tset[p.Name] {
			if _, ok := binds[p.Name]; !ok {
				binds[p.Name] = conc
			}
		}
		// a bound or foreign ident teaches nothing
	case *ast.ParenExpr:
		v.unifyTypeDef(ctx, tset, binds, p.X, conc)
	case *ast.Ellipsis:
		v.unifyTypeDef(ctx, tset, binds, p.Elt, conc)
	case *ast.StarExpr:
		// *T unifies only against pointer-shaped args — a slice or
		// map's element type is not a pointee and must teach nothing.
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindPointer {
			et := v.elemTypedef(v.topFrame(), conc)
			if et == nil {
				et = conc.Elem
			}
			if et != nil {
				v.unifyTypeDef(ctx, tset, binds, p.X, et)
			}
		}
	case *ast.ArrayType:
		// []T unifies only against slice/array-shaped args — a pointer's
		// pointee is not an element and must teach nothing.
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindSlice {
			et := v.elemTypedef(v.topFrame(), conc)
			if et == nil {
				// element named by a type the typedef cannot resolve —
				// a function-local `type` decl, say — falls back to the
				// element evidence argTypedef lifted off the value.
				et = conc.Elem
			}
			if et != nil {
				v.unifyTypeDef(ctx, tset, binds, p.Elt, et)
			}
		}
	case *ast.MapType:
		if mt, ok := conc.Anon.(*ast.MapType); ok && v.H.ResolveType != nil {
			if kt, err := v.H.ResolveType(conc, mt.Key); err == nil {
				v.unifyTypeDef(ctx, tset, binds, p.Key, kt)
			}
			if vt, err := v.H.ResolveType(conc, mt.Value); err == nil {
				v.unifyTypeDef(ctx, tset, binds, p.Value, vt)
			}
		}
	case *ast.ChanType:
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindChan {
			if et := v.elemTypedef(v.topFrame(), conc); et != nil {
				v.unifyTypeDef(ctx, tset, binds, p.Value, et)
			}
		}
	case *ast.FuncType:
		cs := funcTypeExpr(conc)
		if cs == nil || v.H.ResolveType == nil {
			break
		}
		v.unifyFieldTypes(ctx, tset, binds, p.Params, cs.Params, conc)
		v.unifyFieldTypes(ctx, tset, binds, p.Results, cs.Results, conc)
	case *ast.IndexExpr:
		v.unifyIndices(ctx, tset, binds, p.X, []ast.Expr{p.Index}, conc)
	case *ast.IndexListExpr:
		v.unifyIndices(ctx, tset, binds, p.X, p.Indices, conc)
	}
}

// unifyFieldTypes zips two flattened field lists (params or results):
// each declared type expr on the pattern side unifies against the
// resolved typedef on the concrete side. A pattern `...T` pairs with
// either `...U` or `[]U`.
func (v *VM) unifyFieldTypes(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat, conc *ast.FieldList, concCtx *runtime.TypeDef) {
	if pat == nil || conc == nil {
		return
	}
	pe := flattenFieldExprs(pat)
	ce := flattenFieldExprs(conc)
	for i, p := range pe {
		if i >= len(ce) {
			break
		}
		pv := p
		cvv := ce[i]
		peEl, _ := pv.(*ast.Ellipsis)
		ceEl, _ := cvv.(*ast.Ellipsis)
		if peEl != nil && ceEl == nil {
			// variadic pattern against a non-variadic concrete func:
			// approximate by unifying the element against the param as-is.
			pv = peEl.Elt
		}
		ct, err := v.H.ResolveType(concCtx, cvv)
		if err != nil || ct == nil {
			continue
		}
		if ceEl != nil {
			// the concrete side is `...U` — the pattern sees []U
			ct = &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: ceEl.Pos(), Elt: ceEl.Elt}, Pkg: concCtx.Pkg, File: concCtx.File, Binds: concCtx.Binds}
		}
		v.unifyTypeDef(ctx, tset, binds, pv, ct)
	}
}

func flattenFieldExprs(fl *ast.FieldList) []ast.Expr {
	var out []ast.Expr
	for _, fd := range fl.List {
		n := len(fd.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, fd.Type)
		}
	}
	return out
}

// unifyIndices handles a `Name[E]`-shaped pattern against an instantiated
// concrete typedef: each tparam index binds to the concrete type's own
// binding for the base's parameter at that position.
func (v *VM) unifyIndices(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, base ast.Expr, idxs []ast.Expr, conc *runtime.TypeDef) {
	if v.H.ResolveType == nil {
		return
	}
	bt, err := v.H.ResolveType(ctx, base)
	if err != nil || bt == nil || len(bt.TParams) == 0 {
		return
	}
	for i, x := range idxs {
		if i >= len(bt.TParams) {
			break
		}
		id, ok := x.(*ast.Ident)
		if !ok || !tset[id.Name] {
			continue
		}
		if bv, ok := conc.Binds[bt.TParams[i]]; ok {
			if _, bound := binds[id.Name]; !bound {
				binds[id.Name] = bv
			}
		}
	}
}

// funcTypeExpr returns the *ast.FuncType carried by a func-typed typedef,
// from its anonymous or declared form.
func funcTypeExpr(td *runtime.TypeDef) *ast.FuncType {
	for _, x := range []ast.Expr{td.Anon, specTypeOf(td)} {
		if ft, ok := x.(*ast.FuncType); ok {
			return ft
		}
	}
	return nil
}

func specTypeOf(td *runtime.TypeDef) ast.Expr {
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// funcSig returns the declared signature of a function value — the type
// the value carries when stored in an interface: a method expression
// (T.M / (*T).M surfaces as a bare *runtime.Function) signs with its
// receiver as the first parameter, a bound method without it.
func funcSig(x runtime.Value) (*ast.FuncType, *runtime.Package, *syntax.File, map[string]runtime.Value) {
	switch fn := x.(type) {
	case *runtime.Function:
		if sig := funcDeclSig(fn); sig != nil {
			return sig, fn.Pkg, fn.File, fn.Binds
		}
	case *runtime.Closure:
		if sig := funcDeclSig(fn.Fn); sig != nil {
			return sig, fn.Fn.Pkg, fn.Fn.File, fn.Fn.Binds
		}
	case *runtime.BoundMethod:
		if fn.Fn != nil && fn.Fn.Decl != nil && fn.Fn.Decl.Type != nil {
			return fn.Fn.Decl.Type, fn.Fn.Pkg, fn.Fn.File, fn.Fn.Binds
		}
	}
	return nil, nil, nil, nil
}

// funcDeclSig returns the signature a declared function carries as a
// value: a method declaration signs with the receiver prepended (a bare
// Function value only surfaces as a method expression — BoundMethod
// takes the receiver-less path in funcSig).
func funcDeclSig(fn *runtime.Function) *ast.FuncType {
	if fn == nil || fn.Decl == nil || fn.Decl.Type == nil {
		return nil
	}
	if fn.Decl.Recv == nil || len(fn.Decl.Recv.List) == 0 {
		return fn.Decl.Type
	}
	params := []*ast.Field{fn.Decl.Recv.List[0]}
	if fn.Decl.Type.Params != nil {
		params = append(params, fn.Decl.Type.Params.List...)
	}
	return &ast.FuncType{Params: &ast.FieldList{List: params}, Results: fn.Decl.Type.Results}
}

// argTypedef is typeOfValue enriched for inference: stamped container
// typedefs pass through, pointers remember the pointee typedef, and
// function values carry their signature AST so `func(E) R`-shaped
// patterns can unify position-by-position.
func (v *VM) argTypedef(x runtime.Value) *runtime.TypeDef {
	switch xv := x.(type) {
	case *runtime.Slice:
		// the first element's own typedef is kept as Elem evidence —
		// elements of a function-local named type resolve nowhere else.
		var ev *runtime.TypeDef
		if len(xv.Elems) > 0 {
			ev = v.argTypedef(xv.Elems[0])
		}
		if xv.Typ != nil {
			return &runtime.TypeDef{
				Kind: xv.Typ.Kind, Name: xv.Typ.Name, Pkg: xv.Typ.Pkg,
				File: xv.Typ.File, Anon: xv.Typ.Anon, Spec: xv.Typ.Spec,
				Binds: xv.Typ.Binds, Elem: ev,
			}
		}
		return &runtime.TypeDef{Kind: runtime.KindSlice, Elem: ev}
	case *runtime.Map:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindMap}
	case *runtime.Chan:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindChan}
	case *runtime.Cell:
		// a pointer: Elem records the pointee typedef — Anon cannot name
		// it without the variable's declaration.
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.argTypedef(xv.Elem)}
	case *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(x); ok {
			return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.argTypedef(dv)}
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer}
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
		if sig, pkg, file, binds := funcSig(x); sig != nil {
			return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: sig, Pkg: pkg, File: file, Binds: binds}
		}
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	case *runtime.BuiltinFunc:
		// a host builtin carries no declared signature, but its Target
		// holds the adapted Go func — reflect its real signature into a
		// synthesized FuncType so generic inference can bind tparams from
		// `mapper(s, strconv.Itoa)`-style arguments.
		if rt := reflect.TypeOf(xv.Target); rt != nil && rt.Kind() == reflect.Func {
			if td := v.reflectFuncTypedef(rt); td != nil {
				return td
			}
		}
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	}
	return v.typeOfValue(x)
}

// reflectFuncTypedef synthesizes a FuncType typedef from a reflected Go
// signature so host functions without declared ASTs can still teach
// generic inference — `strconv.Itoa`'s `func(int) string` binds T in
// `mapper[F, T]([]F, func(F) T)`. Any component type the translator
// cannot render conservatively aborts the whole signature (unifying on a
// partial shape would bind tparams wrongly).
func (v *VM) reflectFuncTypedef(rt reflect.Type) *runtime.TypeDef {
	params := &ast.FieldList{}
	for i := 0; i < rt.NumIn(); i++ {
		e := reflectTypeExpr(rt.In(i))
		if e == nil {
			return nil
		}
		params.List = append(params.List, &ast.Field{Type: e})
	}
	results := &ast.FieldList{}
	for i := 0; i < rt.NumOut(); i++ {
		e := reflectTypeExpr(rt.Out(i))
		if e == nil {
			return nil
		}
		results.List = append(results.List, &ast.Field{Type: e})
	}
	return &runtime.TypeDef{
		Kind: runtime.KindFunc,
		Anon: &ast.FuncType{Params: params, Results: results},
	}
}

// reflectTypeExpr renders a reflected Go type as the AST a comparable
// source declaration would have had — basic names, pointer, slice,
// array, map and chan shapes, empty interface and error. Named or
// structural types return nil: spelling them from reflect would fabricate
// a name the caller's file never declared.
func reflectTypeExpr(rt reflect.Type) ast.Expr {
	switch rt.Kind() {
	case reflect.Bool:
		return &ast.Ident{Name: "bool"}
	case reflect.Int:
		return &ast.Ident{Name: "int"}
	case reflect.Int8:
		return &ast.Ident{Name: "int8"}
	case reflect.Int16:
		return &ast.Ident{Name: "int16"}
	case reflect.Int32:
		return &ast.Ident{Name: "int32"}
	case reflect.Int64:
		return &ast.Ident{Name: "int64"}
	case reflect.Uint:
		return &ast.Ident{Name: "uint"}
	case reflect.Uint8:
		return &ast.Ident{Name: "uint8"}
	case reflect.Uint16:
		return &ast.Ident{Name: "uint16"}
	case reflect.Uint32:
		return &ast.Ident{Name: "uint32"}
	case reflect.Uint64:
		return &ast.Ident{Name: "uint64"}
	case reflect.Uintptr:
		return &ast.Ident{Name: "uintptr"}
	case reflect.Float32:
		return &ast.Ident{Name: "float32"}
	case reflect.Float64:
		return &ast.Ident{Name: "float64"}
	case reflect.Complex64:
		return &ast.Ident{Name: "complex64"}
	case reflect.Complex128:
		return &ast.Ident{Name: "complex128"}
	case reflect.String:
		return &ast.Ident{Name: "string"}
	case reflect.Pointer:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.StarExpr{X: e}
		}
	case reflect.Slice:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.ArrayType{Elt: e}
		}
	case reflect.Array:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.ArrayType{Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(rt.Len())}, Elt: e}
		}
	case reflect.Map:
		k, e := reflectTypeExpr(rt.Key()), reflectTypeExpr(rt.Elem())
		if k != nil && e != nil {
			return &ast.MapType{Key: k, Value: e}
		}
	case reflect.Chan:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			var dir ast.ChanDir
			switch rt.ChanDir() {
			case reflect.RecvDir:
				dir = ast.RECV
			case reflect.SendDir:
				dir = ast.SEND
			}
			return &ast.ChanType{Dir: dir, Value: e}
		}
	case reflect.Interface:
		if rt.Name() == "error" {
			return &ast.Ident{Name: "error"}
		}
		if rt.NumMethod() == 0 {
			return &ast.Ident{Name: "any"}
		}
	}
	return nil
}

// typeOfValue returns a typedef describing a runtime value, for call-site
// inference. Composite builtin values get anonymous kinds (a []int arg
// binds T to "some slice" — enough for `var z T` and T(x) conversions).
func (v *VM) typeOfValue(x runtime.Value) *runtime.TypeDef {
	if dv, ok := runtime.Deref(x); ok {
		// a pointer argument: T binds to a pointer-ish typedef — the
		// element has no AST on this path, so Anon stays nil and
		// elem-typed operations on it degrade to traps.
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.typeOfValue(dv)}
	}
	switch xv := x.(type) {
	case int64:
		return v.builtinTypedef("int")
	case float64:
		return v.builtinTypedef("float64")
	case string:
		return v.builtinTypedef("string")
	case bool:
		return v.builtinTypedef("bool")
	case *runtime.Slice:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindSlice}
	case *runtime.Map:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindMap}
	case *runtime.Chan:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindChan}
	case *runtime.Struct:
		return xv.Def
	case *runtime.Named:
		return xv.Typ // a named arg binds T to its declared type
	case *runtime.GoValue:
		// a boxed basic (complex128 is the common one — it has no
		// scalar runtime.Value) reports its builtin type so
		// TypeOf(ElemZero([]complex128)) still resolves the element.
		switch xv.V.(type) {
		case complex64:
			return v.builtinTypedef("complex64")
		case complex128:
			return v.builtinTypedef("complex128")
		}
		return nil
	case *runtime.TypedNil:
		return xv.Typ
	case *runtime.IfaceNil:
		return xv.Typ
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	}
	return nil
}

func (v *VM) builtinTypedef(name string) *runtime.TypeDef {
	if v.H.Builtin == nil {
		return nil
	}
	if bv, ok := v.H.Builtin(name); ok {
		if td, ok := bv.(*runtime.TypeDef); ok {
			return td
		}
	}
	return nil
}

// checkTArgs verifies an explicit F[...] / T[...] instantiation against
// the declared constraints. Failures trap with a Go-like message. ctx is
// the generic definition's own typedef context (its package, file imports
// and binds), used to resolve named constraints — including
// self-referential ones like `A Adder[A]` (Go 1.26), which check the
// constraint's required methods against the argument's method set.
func (v *VM) checkTArgs(ctx *runtime.TypeDef, tparams []string, cons []ast.Expr, binds map[string]runtime.Value) error {
	for i, tp := range tparams {
		if i >= len(cons) {
			break
		}
		td := typedefOf(binds[tp])
		if td == nil || v.satisfiesConstraint(ctx, cons[i], td) {
			continue
		}
		return fmt.Errorf("type argument %s does not satisfy constraint %s", tdName(td), typeExprName(cons[i]))
	}
	return nil
}

// satisfiesConstraint approximates Go's constraint check. `any` and
// `comparable` pass; an interface literal checks its type elements
// (~T by underlying-type name, unions by any-match) plus its method
// requirements against the argument's method set; a named or
// instantiated-named constraint resolves through the type index and
// checks the same way — this covers self-referential constraints like
// `A Adder[A]` without ever instantiating the constraint type. Anything
// unresolvable is approximated satisfied — the compiler stays total and
// wrong instantiations may still fail later.
func (v *VM) satisfiesConstraint(ctx *runtime.TypeDef, cons ast.Expr, td *runtime.TypeDef) bool {
	switch t := cons.(type) {
	case nil:
		return true
	case *ast.ParenExpr:
		return v.satisfiesConstraint(ctx, t.X, td)
	case *ast.InterfaceType:
		var reqs []string
		for _, m := range t.Methods.List {
			if len(m.Names) > 0 {
				// method requirements — checked against the method set
				for _, n := range m.Names {
					reqs = append(reqs, n.Name)
				}
				continue
			}
			if !v.satisfiesTypeElem(ctx, m.Type, td) {
				return false
			}
		}
		return v.typeHasMethods(td, reqs)
	case *ast.Ident:
		switch t.Name {
		case "any", "comparable":
			return true
		}
		if isBuiltinTypeName(t.Name) {
			return tdNameOrAnon(td) == t.Name
		}
		// named constraint — resolve it and check its requirements
		if ok, done := v.satisfiesNamed(ctx, t, td); done {
			return ok
		}
		return true
	default:
		// top-level type elements — `T ~int`, `T ~A | ~B`, `T []int`
		// reach the element checker; named/selector/index constraint
		// exprs resolve against the type index first.
		switch cons.(type) {
		case *ast.BinaryExpr, *ast.UnaryExpr, *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.ChanType:
			return v.satisfiesTypeElem(ctx, cons, td)
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
			if ok, done := v.satisfiesNamed(ctx, cons, td); done {
				return ok
			}
		}
		return true
	}
}

// satisfiesTypeElem checks one type element inside a constraint interface.
func (v *VM) satisfiesTypeElem(ctx *runtime.TypeDef, e ast.Expr, td *runtime.TypeDef) bool {
	switch t := e.(type) {
	case *ast.BinaryExpr:
		if t.Op == token.OR {
			return v.satisfiesTypeElem(ctx, t.X, td) || v.satisfiesTypeElem(ctx, t.Y, td)
		}
		return true
	case *ast.UnaryExpr:
		if t.Op == token.TILDE {
			// `~int` matches any type whose UNDERLYING type is int —
			// a named `type MyInt int` satisfies it; peel the argument.
			return underlyingNameOf(v.peelNamed(td)) == typeExprName(t.X)
		}
		return v.satisfiesTypeElem(ctx, t.X, td)
	case *ast.ParenExpr:
		return v.satisfiesTypeElem(ctx, t.X, td)
	case *ast.InterfaceType:
		// embedded interface literal — check its declared methods
		var reqs []string
		for _, m := range t.Methods.List {
			for _, n := range m.Names {
				reqs = append(reqs, n.Name)
			}
		}
		return v.typeHasMethods(td, reqs)
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		if ok, done := v.satisfiesNamed(ctx, e, td); done {
			return ok
		}
		if id, ok := e.(*ast.Ident); ok {
			switch id.Name {
			case "any":
				return true
			case "comparable":
				// approximated: slices, maps and funcs are never
				// comparable; everything else is let through.
				if u := v.peelNamed(td); u != nil {
					switch u.Kind {
					case runtime.KindSlice, runtime.KindMap, runtime.KindFunc:
						return false
					}
				}
				return true
			case "error":
				return v.typeHasMethods(td, []string{"Error"})
			}
		}
		// bare type element: exact type-name match
		return typeExprName(e) == tdNameOrAnon(td)
	default:
		return typeExprName(e) == tdNameOrAnon(td)
	}
}

// satisfiesNamed resolves a named or instantiated constraint element
// (`io.Reader`, `Adder[T]`) through the type index and checks the targ
// against it: an interface checks its required methods, a concrete type
// requires an exact match. done=false means unresolvable — callers fall
// back to their approximation.
func (v *VM) satisfiesNamed(ctx *runtime.TypeDef, e ast.Expr, td *runtime.TypeDef) (ok bool, done bool) {
	if v.H.ResolveType == nil {
		return false, false
	}
	var base ast.Expr
	switch x := e.(type) {
	case *ast.IndexExpr:
		base = x.X
	case *ast.IndexListExpr:
		base = x.X
	case *ast.Ident:
		if isBuiltinTypeName(x.Name) {
			return false, false
		}
		base = x
	case *ast.SelectorExpr:
		base = x
	default:
		return false, false
	}
	nt, err := v.H.ResolveType(ctx, base)
	if err != nil || nt == nil {
		return false, false
	}
	if nt.Kind == runtime.KindInterface {
		// the constraint's type elements and embedded interfaces
		// constrain the argument too — checking only the method set
		// would let `interface{ ~int }` accept every type.
		for _, e := range nt.IEmbeds {
			if !v.satisfiesTypeElem(nt, e, td) {
				return false, true
			}
		}
		return v.typeHasMethods(td, v.ifaceReqNames(nt)), true
	}
	return sameTypeDef(td, nt), true
}

// ifaceReqNames collects a resolved interface typedef's required method
// names through the engine's recursive interface requirement walk.
func (v *VM) ifaceReqNames(nt *runtime.TypeDef) []string {
	if v.H.IfaceReqs != nil {
		if reqs, err := v.H.IfaceReqs(nt); err == nil && reqs != nil {
			out := make([]string, 0, len(reqs))
			for name := range reqs {
				out = append(out, name)
			}
			return out
		}
	}
	return nt.MReqs
}

// typeHasMethods reports whether td's method set covers every required
// name — generic methods are already excluded from method sets (they
// cannot satisfy interfaces). nil td or no method hook passes: an
// unresolvable targ traps later on use anyway.
func (v *VM) typeHasMethods(td *runtime.TypeDef, reqs []string) bool {
	if len(reqs) == 0 || td == nil || v.H.TypeMethods == nil {
		return true
	}
	ms, err := v.H.TypeMethods(td)
	if err != nil || ms == nil {
		return len(reqs) == 0
	}
	for _, r := range reqs {
		if _, ok := ms[r]; !ok {
			return false
		}
	}
	return true
}

// tdNameOrAnon is the exact-type name for bare constraint elements:
// `interface{ int }` matches only `int`, not a defined `type MyInt int`.
func tdNameOrAnon(td *runtime.TypeDef) string {
	if td.Name != "" {
		return td.Name
	}
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	return ""
}

// underlyingNameOf follows Anon to a builtin-ish shape name for `~T`.
func underlyingNameOf(td *runtime.TypeDef) string {
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	if td.Name != "" {
		return td.Name
	}
	return ""
}

var builtinTypeNames = map[string]bool{
	"bool": true, "string": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "byte": true, "rune": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

func isBuiltinTypeName(n string) bool { return builtinTypeNames[n] }
