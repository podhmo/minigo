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
	// FindMethod resolves a promoted method on a struct through embedded
	// fields: (fn, recv, true) binds fn to recv; (nil, field, true) means
	// the embedded field is interface-typed and the VM should select the
	// member on the stored concrete value.
	FindMethod func(s *runtime.Struct, name string) (*runtime.Function, runtime.Value, bool)
	// ElemOf returns the element typedef of a container typedef ([]T->T,
	// map[K]V->V, chan T->T, *T->T) — used by elided composite literal
	// elements (`{{1,2}}` inside `[][]int`).
	ElemOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// TypeMethods returns the method set of a typedef (declared +
	// promoted; pointer receivers included) — used when an interface
	// satisfaction check must run against a type rather than a value
	// (typed nils in assertions).
	TypeMethods func(td *runtime.TypeDef) (map[string]bool, error)
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
}

// proc is one interpreter process: the goroutines belonging to a root Call.
type proc struct {
	done     chan struct{}
	doneOnce sync.Once
	mu       sync.Mutex
	fatal    error // first goroutine failure (panic/trap/builtin error)
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
	child := &VM{H: v.H, proc: p, task: t}
	go func() {
		r, err := child.Call(fn, args)
		t.Result = r
		t.Finish(err, IsProcExit(err))
		if err != nil && !IsProcExit(err) {
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
// untraceable crash in Go).
const maxFrames = 10000

type deferredCall struct {
	fn   runtime.Value
	args []runtime.Value
	pos  token.Pos
}

type frame struct {
	fn     *runtime.Function
	ch     *bytecode.Chunk
	locals []*runtime.Cell
	upvals []*runtime.Cell
	stack  []runtime.Value
	ip     int

	defers   []deferredCall // LIFO
	deferred bool           // frame created for a deferred call
	retNamed bool           // gather named result slots after defers run
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
	}
	c.Elem = valueCopy(val)
}

// Call invokes a function-like value: Function, Closure, BoundMethod,
// BuiltinFunc, TypeDef (conversion), or Cell wrapping any of those.
// It is the engine boundary: script panics and traps unwind as Go panics
// and are converted to errors here. The outermost Call on a VM is its
// process's root: the proc is created lazily and killed on return.
func (v *VM) Call(callee runtime.Value, args []runtime.Value) (result runtime.Value, err error) {
	gid := goroutineID()
	v.callMu.Lock()
	if v.callDepth > 0 && v.callGid != gid {
		v.callMu.Unlock()
		// a foreign goroutine — a retained host callback firing off-VM
		// (WaitGroup.Go, time.AfterFunc, a Pool.New hit from another
		// script goroutine): the owning goroutine holds this frame
		// stack, so run the call on a spawned child VM of the same
		// process and join it instead of racing the owner's frames.
		t := v.Spawn(callee, args)
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
			result, err = nil, asError(r)
		}
		// a goroutine's panic fails the process like Go's crash: report
		// the real failure over this call's own outcome (including a
		// procExit this goroutine received while unwinding)
		if ferr := p.fatalErr(); ferr != nil && (err == nil || IsProcExit(err)) {
			err = ferr
		}
	}()
	return v.call(callee, args)
}

// call is Call without the boundary: script *Panic / *Trap propagate as Go
// panics through intermediate frames so defers and recover() see them.
func (v *VM) call(callee runtime.Value, args []runtime.Value) (runtime.Value, error) {
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
			panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
		case *runtime.Named:
			// a value of a named func type calls through its underlying
			callee = c.V
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
	fr, err := v.prepFrame(callee, args)
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
	if n := len(v.frames); n > 0 && v.frames[n-1].deferred && v.inflight != nil {
		val := v.inflight.Value
		v.inflight = nil
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
func (v *VM) prepFrame(callee runtime.Value, args []runtime.Value) (*frame, error) {
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
	// generic function called without instantiation (Id(40)): infer the
	// unbound type arguments from the runtime argument types. A method of
	// an instantiated generic type arrives partly bound — the receiver's
	// own type binds stay, only the method's own type params infer.
	if fn != nil && len(fn.TParams) > 0 && hasUnbound(fn.TParams, fn.Binds) {
		inferred, err := v.inferBinds(fn, args)
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
	// procExit passes through like Trap: a process-exit unwind must not
	// become a recoverable script panic.
	case nil, *runtime.Trap, *runtime.Panic, procExit:
		return r
	default:
		return &runtime.Panic{Value: &runtime.GoValue{V: r}, GoStack: string(debug.Stack())}
	}
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
	saved := v.inflight
	v.inflight = p
	v.runDefers(f)
	p = v.inflight
	v.inflight = saved
	switch {
	case p != nil:
		panic(p) // still panicking, or a deferred call panicked
	case r != nil:
		if _, ok := r.(*runtime.Panic); !ok {
			panic(r) // Trap/host panic: recover() must not swallow it
		}
		fallthrough // script panic recovered by a deferred function
	default:
		f.stack = []runtime.Value{f.finalResult()}
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

// runDefers drains the frame's defer list LIFO. As in Go, a script panic
// inside a deferred call supersedes the panic being unwound but the
// remaining defers still run; a Trap aborts the rest.
func (v *VM) runDefers(f *frame) {
	for len(f.defers) > 0 {
		d := f.defers[len(f.defers)-1]
		f.defers = f.defers[:len(f.defers)-1]
		func() {
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
		}()
	}
}

// invokeDeferred runs one deferred call. Callees without a bytecode frame
// (BuiltinFunc, TypeDef conversion, Cell-wrapped values) still run at
// teardown; a sentinel frame marked deferred is pushed for them so
// recover() still sees the call as a deferred function — matching Go,
// where `defer recover()` catches the panic being unwound.
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
			callee = c.V
			continue
		default:
			if dv, ok := runtime.Deref(callee); ok {
				callee = dv
				continue
			}
		}
		break
	}
	fr, err := v.prepFrame(callee, d.args)
	if err != nil {
		panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
	}
	fr.deferred = true
	v.exec(fr)
}

// pushDeferredSentinel records a deferred host call on the frame stack so
// Recover() treats it as the innermost deferred function. It is not a real
// frame: no chunk, no locals — only the deferred flag matters.
func (v *VM) pushDeferredSentinel(name string) {
	v.frames = append(v.frames, &frame{fn: &runtime.Function{Name: name}, deferred: true})
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
			f.push(&runtime.FieldRef{Base: base, Name: consts[ins.A].(string)})
		case bytecode.OpIndexRef:
			key := f.pop()
			base := f.pop()
			if _, isMap := runtime.Unwrap(base).(*runtime.Map); isMap {
				// Go rejects &m[k] at compile time: map elements are
				// not addressable. The nearest loud failure is a trap.
				f.trap("cannot take the address of map element")
			}
			f.push(&runtime.IndexRef{Base: base, Key: key})
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
			if td, ok := x.(*runtime.TypeDef); ok {
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
					panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
				}
				f.trap("deref of non-pointer %T", x)
			} else {
				f.trap("deref of non-pointer %T", x)
			}
		case bytecode.OpSetInd:
			val := f.pop()
			ref := f.pop()
			if tn, ok := asTypedNil(ref); ok && tn.Typ.Kind == runtime.KindPointer {
				panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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
			args := v.popArgs(f, int(ins.A), ins.B == 1, ins.Pos)
			fn := f.pop()
			r, err := v.call(fn, args)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			f.push(r)
		case bytecode.OpDefer:
			// callee + args are evaluated now (Go semantics); the call itself
			// runs at frame teardown, LIFO.
			args := v.popArgs(f, int(ins.A), ins.B == 1, ins.Pos)
			fn := f.pop()
			f.defers = append(f.defers, deferredCall{fn: fn, args: args, pos: ins.Pos})
		case bytecode.OpGo:
			// `go f(x)` spawns a real goroutine in this process: callee and
			// args are evaluated now; the call runs concurrently.
			args := v.popArgs(f, int(ins.A), ins.B == 1, ins.Pos)
			fn := f.pop()
			v.Spawn(fn, args)
		case bytecode.OpEvalAST:
			frag := consts[ins.A].(*bytecode.ASTFragment)
			if v.H.CompileExpr == nil {
				f.trap("OpEvalAST: no CompileExpr hook")
			}
			ch, err := v.H.CompileExpr(f.fn.Pkg, frag.File, frag.Expr)
			if err != nil {
				panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error(), Err: err})
			}
			r, err := v.call(&runtime.Function{Pkg: f.fn.Pkg, File: frag.File, Name: "<eval>", Chunk: ch}, nil)
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
				v.driveFuncIter(f, it, int(ins.C), top, int(ins.A))
				// The producer and every iteration already ran; f.ip sits
				// where the body last stopped:
				//   [top, end]  loop is done — take the exit jump
				//   < top       a goto left the loop backward — keep target
				//   > end       a goto forward or OpReturn — keep target
				if f.ip >= top && f.ip <= int(ins.A) {
					f.ip = int(ins.A)
				}
			} else if !v.iterNext(f, it, int(ins.C)) {
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
		for _, ref := range pkg.Imports[file] {
			if ref.Alias != "" {
				continue
			}
			p, err := ref.Materialize()
			if err == nil && p != nil && p.Name == name {
				return ref, nil
			}
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
	case *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(base)
		if !ok {
			f.trap("select %s on unresolved reference %T", name, base)
		}
		if s, isStruct := dv.(*runtime.Struct); isStruct {
			return v.structMember(f, s, name, base)
		}
		if n, isNamed := dv.(*runtime.Named); isNamed {
			return v.namedMember(f, n, name, base)
		}
		switch t := dv.(type) {
		case *runtime.Slice:
			return v.typedMember(f, t.Typ, name, base, "slice")
		case *runtime.Map:
			return v.typedMember(f, t.Typ, name, base, "map")
		case *runtime.Chan:
			return v.typedMember(f, t.Typ, name, base, "chan")
		case *runtime.GoValue:
			return v.selectMember(f, t, name)
		case *runtime.Package:
			return v.selectMember(f, t, name)
		}
		f.trap("select %s on %T", name, dv)
	case *runtime.TypeDef:
		if _, isPtr := b.Anon.(*ast.StarExpr); isPtr {
			// `(*T).M` — a pointer method expression sees the full
			// method set (value and pointer receivers alike).
			if et := v.elemTypedef(f, b); et != nil {
				if m, ok := et.Methods[name]; ok {
					return m
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
		f.trap("type %s has no method %s", b.Name, name)
	case *runtime.IfaceNil:
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
	// the method value's own PC is a thunk (reflect.methodValueCall)
	// — the declared method's Func is the only handle that still
	// points at the real code, so keep it for inspect.
	if tm, ok := reflect.TypeOf(hv).MethodByName(name); ok {
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
		return v
	case int8, int16, int32:
		// sized ints keep their declared width so %T spells them
		// like Go (int32 also covers rune — an alias).
		return &runtime.Named{Typ: sizedIntTyp(rv.Kind()), V: rv.Int()}
	case uint, uint8, uint16, uint32, uintptr:
		return &runtime.Named{Typ: sizedIntTyp(rv.Kind()), V: int64(reflect.ValueOf(v).Uint())}
	case uint64:
		if v <= math.MaxInt64 {
			return int64(v)
		}
		return &runtime.GoValue{V: x}
	case string:
		return v
	case bool:
		return v
	case float32:
		return float64(v)
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
			el[i] = int64(b)
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
			kv := goValueOf(reflect.ValueOf(k))
			m.Pairs[runtime.CanonicalKey(kv)] = goValueOf(reflect.ValueOf(e))
			m.Order = append(m.Order, kv)
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
	var holder runtime.Value
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
		out := reflect.MakeMapWithSize(t, len(x.Pairs))
		for _, k := range x.Order {
			e := x.Pairs[runtime.CanonicalKey(k)]
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
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", av.Type(), t)
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
			panic(&runtime.Panic{Value: err.Error()})
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
		out := make(map[any]any, len(x.Pairs))
		for _, k := range x.Order {
			out[deepHost(k)] = deepHost(x.Pairs[runtime.CanonicalKey(k)])
		}
		return out
	}
	return v
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
	if useSlice {
		out = m.CallSlice(in)
	} else {
		out = m.Call(in)
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
				val = &runtime.Named{Typ: n.Typ, V: vv}
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
			// pointer receiver needs an addressable reference
			if _, ok := runtime.Deref(r); !ok {
				r = &runtime.Cell{Elem: r}
			}
		} else {
			// value receiver operates on a copy
			if dv, ok := runtime.Deref(r); ok {
				r = dv
			}
			r = valueCopy(r)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}
	}
	// promoted field: embedded fields expose their members one level up
	// (Go 1.27 also accepts them as composite-literal keys). Breadth
	// beats depth here the same way it does for promoted methods.
	if inner, j, hrecv, ok := v.promotedField(f, s, name, true); ok {
		if hrecv != nil {
			return v.selectMember(f, hrecv, name)
		}
		return inner.Fields[j]
	}
	// promoted method: reach through embedded fields via the engine hook
	if v.H.FindMethod != nil {
		if m, rcv, ok := v.H.FindMethod(s, name); ok {
			if m == nil {
				// interface-typed embedded field: dispatch on the stored value
				return v.selectMember(f, rcv, name)
			}
			if err := m.EnsureCompiled(); err != nil {
				f.trap("%s", err)
			}
			r := rcv
			if m.PtrRecv {
				if _, ok := runtime.Deref(r); !ok {
					r = &runtime.Cell{Elem: r}
				}
			} else {
				if dv, ok := runtime.Deref(r); ok {
					r = dv
				}
				r = valueCopy(r)
			}
			return &runtime.BoundMethod{Recv: r, Fn: m}
		}
	}
	f.trap("%s has no field or method %s", def.Name, name)
	return nil
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
			if _, ok := runtime.Deref(r); !ok {
				r = &runtime.Cell{Elem: r}
			}
		} else if td != n.Typ {
			// a value receiver reached through the peeled pointer binds
			// the pointee, re-tagged to the declared type.
			sv := n.V
			if dv, ok := runtime.Deref(sv); ok {
				sv = dv
			}
			r = valueCopy(&runtime.Named{Typ: td, V: sv})
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
	if s, isStruct := sv.(*runtime.Struct); isStruct {
		for i, fn := range s.Def.Fields {
			if fn == name {
				return s.Fields[i]
			}
		}
		if inner, j, hrecv, ok := v.promotedField(f, s, name, true); ok {
			if hrecv != nil {
				return v.selectMember(f, hrecv, name)
			}
			return inner.Fields[j]
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
		if inner, j, hrecv, ok := v.promotedField(f, b, name, true); ok {
			if hrecv != nil {
				v.setField(f, hrecv, name, val)
				return
			}
			var ft *runtime.TypeDef
			if fts := v.fieldTypedefs(inner.Def); j < len(fts) {
				ft = fts[j]
			}
			inner.Fields[j] = v.coerce(f, val, ft)
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
			panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
		}
		f.trap("set field %s on nil %s", name, tdName(b.Typ))
	case *runtime.IfaceNil:
		if b.Typ != nil && b.Typ.Kind == runtime.KindPointer {
			panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
		}
		f.trap("set field %s on nil %s", name, tdName(b.Typ))
	default:
		f.trap("set field %s on %T", name, base)
	}
}

func (v *VM) index(f *frame, base, idx runtime.Value) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		return v.index(f, dv, idx)
	}
	idx = runtime.Unwrap(materialize(f, idx)) // named key/index types hash as their value
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
			panic(&runtime.Panic{Value: fmt.Sprintf("runtime error: index out of range [%v] with length 0", runtime.Unwrap(idx))})
		default:
			f.trap("index on nil %s", tdName(b.Typ))
		}
	case *runtime.Slice:
		i, ok := idx.(int64)
		if !ok {
			f.trap("slice index is %T", idx)
		}
		return v.elemRead(f, b.Typ, b.Elems[i])
	case *runtime.Map:
		val, found := b.Pairs[runtime.CanonicalKey(idx)]
		if !found {
			val = v.mapZero(f, b.Typ)
		}
		return v.elemRead(f, b.Typ, val)
	case string:
		i, ok := idx.(int64)
		if !ok {
			f.trap("string index is %T", idx)
		}
		return &runtime.Named{Typ: v.builtinTypedef("uint8"), V: int64(b[i])}
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

// promotedField locates `name` among s's promoted (embedded) fields,
// breadth-first: the shallowest level wins and multiple hits at the same
// level trap as ambiguous (Go rejects them at compile time). Host-typed
// embeds (sync.Mutex, sync.Pool, ...) join the search as leaves — their
// promoted surface is the boxed value's exported fields and methods, so
// a name promoted through a host embed and a script embed at the same
// depth is ambiguous too. A host hit returns the embedded value itself
// (third result) for the caller to select on. Script-embed methods are
// counted for the ambiguity check but not returned (they dispatch
// through findMethod). allowPtr controls whether *T embeds are
// traversed — composite-literal keys forbid pointer indirection (Go
// reports "invalid implicit pointer indirection"), while selector
// access allows it and a nil embedded pointer panics on the way
// through, like Go.
func (v *VM) promotedField(f *frame, s *runtime.Struct, name string, allowPtr bool) (*runtime.Struct, int, runtime.Value, bool) {
	if v.H.ResolveType == nil {
		return nil, 0, nil, false
	}
	type slot struct {
		st  *runtime.Struct
		idx int
	}
	// a host member's absolute resolution depth includes the embedding
	// inside the host type itself (template.Template → *parse.Tree →
	// Root), so a hit can land below the level that discovered it.
	type hostHit struct {
		abs  int
		recv runtime.Value
	}
	var hostHits []hostHit
	level := []*runtime.Struct{s}
	// nilDepth/nilPaths track resolutions that exist only through a nil
	// embedded pointer: Go selects fields statically, so such a field is
	// not "undefined" — reaching it panics on the implicit dereference
	// (and a same-depth tie with a real path is ambiguous, like Go's
	// compile-time rejection).
	nilDepth, nilPaths := 0, 0
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
		var hits []slot
		methHits := 0
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
				if _, ok := raw.Methods[name]; ok {
					// promoted methods resolve through findMethod, but
					// they still collide: a same-depth field or host
					// member with the name makes the selector ambiguous,
					// like Go's compile-time rejection. Only the embed's
					// own methods count — a defined type does not inherit
					// the underlying type's methods.
					methHits++
					continue
				}
				if len(embTd.Fields) == 0 {
					// non-struct embeds (interfaces, basics) promote no
					// fields — methods dispatch through FindMethod.
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
						// by value — ask the type whether the field
						// exists deeper, and at which depth.
						if d, c := v.embedTypeDepth(embTd, name, map[*runtime.TypeDef]bool{}); d > 0 {
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
					panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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
		total := len(hits) + len(thisHost) + methHits
		if total > 0 {
			if nilDepth > 0 && nilDepth <= depth+1 {
				// a nil path ties the real field's depth — Go
				// rejects the selector as ambiguous.
				f.trap("ambiguous selector %s", name)
			}
			if total > 1 {
				f.trap("ambiguous selector %s", name)
			}
			if len(thisHost) == 1 {
				return nil, 0, thisHost[0], true
			}
			if len(hits) == 1 {
				return hits[0].st, hits[0].idx, nil, true
			}
			// a sole promoted-method hit at this depth — it is
			// not a field, so let findMethod bind it below.
			return nil, 0, nil, false
		}
		if nilDepth > 0 && nilDepth <= depth+1 {
			if nilPaths > 1 {
				f.trap("ambiguous selector %s", name)
			}
			panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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
			panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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
		return nil, 0, recv, true
	}
	if nilDepth > 0 {
		if nilPaths > 1 {
			f.trap("ambiguous selector %s", name)
		}
		panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
	}
	return nil, 0, nil, false
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
		val, found := m.Pairs[runtime.CanonicalKey(idx)]
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
			panic(&runtime.Panic{Value: "assignment to entry in nil map"})
		case runtime.KindSlice:
			panic(&runtime.Panic{Value: fmt.Sprintf("runtime error: index out of range [%v] with length 0", runtime.Unwrap(idx))})
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
		b.Elems[i] = val
	case *runtime.Map:
		if idx != nil && !reflect.TypeOf(idx).Comparable() {
			f.trap("map key %T is not comparable", idx)
		}
		// the map's declared element type constrains the write.
		if et := v.elemTypedef(f, b.Typ); et != nil {
			val = v.coerce(f, val, et)
		}
		ck := runtime.CanonicalKey(idx)
		if _, exists := b.Pairs[ck]; !exists {
			b.Order = append(b.Order, idx)
		}
		b.Pairs[ck] = val
	default:
		f.trap("index assign on %T", base)
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
			panic(&runtime.Panic{Value: nilSliceBoundsReason(l, h, m, three)})
		}
		return b
	case *runtime.Slice:
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
	const p = "runtime error: slice bounds out of range"
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
	if td.HostNew != nil {
		// host-backed type (sync.Mutex, sync.Pool, ...): the literal
		// yields a fresh boxed host value; keyed fields initialize the
		// exported fields of the host struct through reflection
		// (`&sync.Pool{New: f}`).
		hv := td.HostNew()
		if n > 0 {
			if !kv {
				f.trap("cannot initialize host type %s with positional fields", td.Name)
			}
			v.initHostLiteral(f, td, hv, raw[:2*n])
		}
		return &runtime.GoValue{V: hv}
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
				s.Elems[i] = zv
			}
			if kv {
				for i := 0; i < n; i++ {
					k := raw[i*2]
					if nk, ok := k.(*runtime.Named); ok {
						k = nk.V
					}
					ival, ok := k.(int64)
					if !ok {
						f.trap("array literal index %T", raw[i*2])
					}
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
			for i := 0; i < n; i++ {
				k := raw[i*2]
				if nk, ok := k.(*runtime.Named); ok {
					k = nk.V
				}
				ival, ok := k.(int64)
				if !ok {
					f.trap("slice literal index %T", raw[i*2])
				}
				idx[i] = ival
				if ival > max {
					max = ival
				}
			}
			s.Elems = make([]runtime.Value, max+1)
			for i := range s.Elems {
				s.Elems[i] = runtime.NIL
			}
			for i := 0; i < n; i++ {
				s.Elems[idx[i]] = raw[i*2+1]
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
		return s
	case runtime.KindMap:
		m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}, Typ: td}
		et := v.elemTypedef(f, td)
		for i := 0; i < n; i++ {
			k := runtime.Unwrap(materialize(f, raw[i*2]))
			ck := runtime.CanonicalKey(k)
			if _, exists := m.Pairs[ck]; !exists {
				m.Order = append(m.Order, k)
			}
			m.Pairs[ck] = v.coerce(f, raw[i*2+1], et)
		}
		return m
	case runtime.KindPointer:
		// elided `&T{...}` inside a []*T{...} literal: build the element
		// composite and wrap it in a fresh cell (a pointer).
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
		var es *runtime.Struct
		if z, isStruct := v.zeroValue(f, et).(*runtime.Struct); isStruct {
			es = z
		} else {
			es = &runtime.Struct{Def: et, Fields: make([]runtime.Value, len(et.Fields))}
			for i := range es.Fields {
				es.Fields[i] = runtime.NIL
			}
		}
		if kv {
			fts := v.fieldTypedefs(et)
			for i := 0; i < n; i++ {
				name, ok := raw[i*2].(string)
				if !ok {
					f.trap("struct literal key %T", raw[i*2])
				}
				found := v.setLitField(f, es, et, fts, name, raw[i*2+1])
				if !found {
					f.trap("%s has no field %s", tdName(et), name)
				}
			}
		} else {
			fts := v.fieldTypedefs(et)
			for i := 0; i < n && i < len(es.Fields); i++ {
				var ft *runtime.TypeDef
				if i < len(fts) {
					ft = fts[i]
				}
				es.Fields[i] = v.coerce(f, raw[i], ft)
			}
		}
		return &runtime.Cell{Elem: es}
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
// implicit pointer indirection"), so promotedField is called with
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
	if inner, j, hrecv, ok := v.promotedField(f, s, name, false); ok {
		if hrecv != nil {
			v.setField(f, hrecv, name, val)
			return true
		}
		var ft *runtime.TypeDef
		if ifts := v.fieldTypedefs(inner.Def); j < len(ifts) {
			ft = ifts[j]
		}
		inner.Fields[j] = v.coerce(f, val, ft)
		return true
	}
	return false
}

// valueCopy implements Go assignment semantics: structs copy by value;
// slices, maps and pointers share.
func valueCopy(v runtime.Value) runtime.Value {
	switch x := v.(type) {
	case *runtime.Struct:
		cp := &runtime.Struct{Def: x.Def, Fields: make([]runtime.Value, len(x.Fields))}
		copy(cp.Fields, x.Fields)
		return cp
	case *runtime.Slice:
		// arrays copy on assignment like structs — nested arrays copy
		// recursively. Plain slices share their backing (Go semantics).
		if isArrayTyp(x.Typ) {
			el := make([]runtime.Value, len(x.Elems))
			for i, e := range x.Elems {
				el[i] = valueCopy(e)
			}
			return &runtime.Slice{Elems: el, Typ: x.Typ}
		}
		return v
	case *runtime.Named:
		// assignment copies the underlying value but keeps the declared tag
		return &runtime.Named{Typ: x.Typ, V: valueCopy(x.V)}
	}
	return v
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
	_, err := v.call(fn, nil)
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
		return &runtime.Iterator{Kind: 's', Elems: c.Elems}
	case *runtime.Map:
		it := &runtime.Iterator{Kind: 'm', Keys: c.Order}
		for _, k := range c.Order {
			it.Elems = append(it.Elems, c.Pairs[runtime.CanonicalKey(k)])
		}
		return it
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
		// range over a nil slice/map iterates zero times
		if tn, ok := coll.(*runtime.TypedNil); ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindChan {
			// a nil channel range blocks forever, as in Go
			return &runtime.Iterator{Kind: 'c', ChRV: nilChanRV, ETyp: v.elemTypedef(f, tn.Typ)}
		}
		return &runtime.Iterator{Kind: 's'}
	case nil, runtime.Nil:
		return &runtime.Iterator{Kind: 's'}
	default:
		f.trap("range over %T", coll)
		return nil
	}
}

// iterNext pushes nvars values (key/index, elem) and returns false when done.
func (v *VM) iterNext(f *frame, it *runtime.Iterator, nvars int) bool {
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
		if it.Idx >= len(it.Elems) {
			return false
		}
		push(int64(it.Idx), it.Elems[it.Idx])
		it.Idx++
		return true
	case 'm':
		if it.Idx >= len(it.Keys) {
			return false
		}
		push(it.Keys[it.Idx], it.Elems[it.Idx])
		it.Idx++
		return true
	case 'i':
		if it.Idx >= it.Limit {
			return false
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
				panic(&runtime.Panic{Value: "range function continued iteration after yield returned false"})
			}
			if nvars > 0 && len(args) != nvars {
				return nil, fmt.Errorf("yield must be called with %d argument(s), got %d", nvars, len(args))
			}
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
	if _, err := v.call(it.Fn, []runtime.Value{yield}); err != nil {
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
				return &runtime.Named{Typ: &runtime.TypeDef{Name: "rune", Kind: runtime.KindNamedBasic}, V: i}, nil
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
		return fv, nil
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
		if u.V.Kind() != constant.Int {
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
		x = float64(f32)
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
		return nil, fmt.Errorf("cannot use constant %s as %s", u.V, tdName(td))
	}
	// the tag rule from coerceConcrete applies equally: a converted const
	// keeps the declared name (and int64/float32/complex64 tag even
	// spelled bare).
	if td != nil && (declaredType(td) || sizedIntName(td.Name) || td.Name == "int64" ||
		td.Name == "float32" || td.Name == "complex64") {
		return &runtime.Named{Typ: td, V: x}, nil
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
	return constant.Float64Val(cv)
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
	return complex(re, im)
}

// fitsIntConst reports whether an integer constant is representable as
// the named Go int type — Go's constant-to-type conversion check.
func fitsIntConst(cv constant.Value, name string) (int64, bool) {
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
		return constant.MakeBool(constant.Compare(ua.V, tok, ub.V)), true
	case token.SHL, token.SHR:
		s, ok := constant.Uint64Val(ub.V)
		if !ok {
			return nil, false
		}
		return &runtime.UConst{V: constant.Shift(ua.V, tok, uint(s)), Rune: ua.Rune || ub.Rune}, true
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
// stays out — it is typed, not a constant.
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
	}
	return nil, false
}

// isPlainConst reports whether x reads as a compile-time constant for
// mixed folding — bare numerics and strings do, typed (Named) values
// and everything else do not.
func isPlainConst(x runtime.Value) bool {
	switch x.(type) {
	case int64, float64, string, bool:
		return true
	}
	return false
}

func binaryOp(f *frame, op bytecode.BinOp, a, b runtime.Value) runtime.Value {
	// untyped constants fold in the arbitrary-precision constant domain
	// while both sides read as constants — a bare int64/float64 operand
	// came from a folded literal and can lift back (`const C = B - 1<<99`
	// computes exactly). With a real value a constant materializes to
	// its default type instead (and can fail to, like Go's compile-time
	// "constant overflows int").
	if _, isA := a.(*runtime.UConst); isA {
		if _, isB := b.(*runtime.UConst); isB || isPlainConst(b) {
			ca, _ := constOf(a)
			cb, _ := constOf(b)
			if r, ok := constBinary(op, ca, cb); ok {
				return r
			}
		}
		a = materialize(f, a)
	}
	if _, ok := b.(*runtime.UConst); ok {
		if isPlainConst(a) {
			ca, _ := constOf(a)
			cb, _ := constOf(b)
			if r, ok2 := constBinary(op, ca, cb); ok2 {
				return r
			}
		}
		b = materialize(f, b)
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
					return &runtime.Named{Typ: tag, V: r}
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
					return &runtime.Named{Typ: tag, V: maskInt(int64(res.(uint64)), uname)}
				}
			}
		}
		res := binaryOp(f, op, a, b)
		if iv, ok := res.(int64); ok {
			return &runtime.Named{Typ: tag, V: maskInt(iv, sizedNameOf(tag))}
		}
		if fv, ok := res.(float64); ok {
			// a float32-flavored tag narrows the result the way an
			// assignment into a float32 slot does.
			if basicNameOf(tag) == "float32" {
				fv = float64(float32(fv))
			}
			return &runtime.Named{Typ: tag, V: fv}
		}
		if _, ok := res.(string); ok {
			return &runtime.Named{Typ: tag, V: res}
		}
		return res
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
	if g, ok := a.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			a = int64(u)
			ubox = true
		}
	}
	if g, ok := b.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			b = int64(u)
			ubox = true
		}
	}
	// equality works on any comparable pair
	switch op {
	case bytecode.BinEql:
		return eqlValue(a, b)
	case bytecode.BinNeq:
		return !eqlValue(a, b)
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
	}
	f.trap("uint binary %s", op)
	return nil, false
}

// shiftOp evaluates << and >> in the left operand's signedness: Go types
// the result by the left side alone (the count is always an unsigned
// count), so a uintptr/uint64 value shifts logically while int64 shifts
// arithmetically. A declared-width operand re-tags and re-masks.
func shiftOp(f *frame, op bytecode.BinOp, a, b runtime.Value) runtime.Value {
	a = materialize(f, a)
	b = materialize(f, b)
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag, a = n.Typ, n.V
	}
	count, ok := shiftCount(b)
	if !ok {
		f.trap("unsupported shift count %T", b)
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
			return &runtime.Named{Typ: tag, V: maskInt(r, sizedNameOf(tag))}
		}
		return r
	case time.Duration:
		return time.Duration(shiftInt(op, int64(x), count))
	case *runtime.GoValue:
		if u, ok := x.V.(uint64); ok {
			r := shiftUint(op, u, count)
			if tag != nil {
				return &runtime.Named{Typ: tag, V: maskInt(int64(r), sizedNameOf(tag))}
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
			panic(&runtime.Panic{Value: "runtime error: negative shift amount"})
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
		}
	}
	return 0, false
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
				return &runtime.Named{Typ: tag, V: maskInt(iv, sizedNameOf(tag))}
			}
			switch r.(type) {
			case float64, string, bool:
				return &runtime.Named{Typ: tag, V: r}
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
				return &runtime.GoValue{V: -u}
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
				return &runtime.GoValue{V: ^u}
			}
		}
		f.trap("unary ^ on %T", a)
	}
	f.trap("unary %s on %T", op, a)
	return nil
}

// structDefsEq reports whether two struct typedefs are the same type:
// identical pointers, or two anonymous struct typedefs carrying the same
// field-name list — Go treats `struct{A int}` written twice as one type.
func structDefsEq(a, b *runtime.TypeDef) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Name != "" || b.Name != "" {
		return a.Name == b.Name && a.Pkg == b.Pkg
	}
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
		}
		return false
	}
	if af, ok := a.(float64); ok {
		switch bv := b.(type) {
		case int64:
			return af == float64(bv)
		case float64:
			return af == bv
		}
		return false
	}
	if in, ok := a.(*runtime.IfaceNil); ok {
		// interface value holding a typed nil: nil only to a same-typed nil
		switch bi := b.(type) {
		case *runtime.IfaceNil:
			return sameTypeDef(in.Typ, bi.Typ)
		case *runtime.TypedNil:
			return sameTypeDef(in.Typ, bi.Typ)
		}
		return false
	}
	if tn, ok := a.(*runtime.TypedNil); ok {
		switch bi := b.(type) {
		case runtime.Nil, *runtime.TypedNil:
			return true // a nil pointer/slice/map/chan/func == nil
		case *runtime.IfaceNil:
			return sameTypeDef(tn.Typ, bi.Typ)
		}
		return false
	}
	if _, ok := a.(runtime.Nil); ok {
		switch b.(type) {
		case runtime.Nil, *runtime.TypedNil:
			return true
		}
		return false
	}
	switch av := a.(type) {
	case *runtime.Struct:
		// structs compare field-wise in Go; the defs must name the same
		// type — anonymous struct typedefs with the same field list are
		// the same type (Go's identical-underlying rule).
		bs, ok := b.(*runtime.Struct)
		if !ok || !structDefsEq(av.Def, bs.Def) || len(av.Fields) != len(bs.Fields) {
			return false
		}
		for i := range av.Fields {
			if !eqlValue(av.Fields[i], bs.Fields[i]) {
				return false
			}
		}
		return true
	case *runtime.Slice:
		if bs, ok := b.(*runtime.Slice); ok {
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
			panic(&runtime.Panic{Value: "runtime error: comparing uncomparable type " + kindName(av)})
		}
		return false
	case *runtime.Map:
		if _, ok := b.(*runtime.Map); ok {
			panic(&runtime.Panic{Value: "runtime error: comparing uncomparable type " + kindName(av)})
		}
		return false
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch b.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			panic(&runtime.Panic{Value: "runtime error: comparing uncomparable type func"})
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
				panic(&runtime.Panic{Value: "runtime error: comparing uncomparable type " + t.String()})
			}
			return av.V == bg.V
		}
		return false
	}
	return a == b // pointers, strings, bools
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

// kindName names a value's type for runtime-error messages.
func kindName(v runtime.Value) string {
	switch x := v.(type) {
	case *runtime.Slice:
		if x.Typ != nil {
			return tdName(x.Typ)
		}
		return "slice"
	case *runtime.Map:
		if x.Typ != nil {
			return tdName(x.Typ)
		}
		return "map"
	}
	return fmt.Sprintf("%T", v)
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

// sameTypeDef reports whether two typedefs name the same type: identical
// typedefs, equal named types (name+package), or anonymous types with the
// same shape spelling ([]int, *Sq, map[string]int, ...).
func sameTypeDef(a, b *runtime.TypeDef) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	if a.Name != "" || b.Name != "" {
		return a.Name != "" && canonBasicName(a.Name) == canonBasicName(b.Name) && a.Pkg == b.Pkg && bindsEq(a.Binds, b.Binds)
	}
	if a.Anon != nil && b.Anon != nil {
		return typeExprNameCtx(a.Anon, a.File, a.Pkg) == typeExprNameCtx(b.Anon, b.File, b.Pkg)
	}
	return false
}

// canonBasicName folds predeclared aliases: byte is uint8 and rune is int32
// — an alias spelled at a call site and its canonical name are the same type.
func canonBasicName(n string) string {
	switch n {
	case "byte":
		return "uint8"
	case "rune":
		return "int32"
	}
	return n
}

// bindsEq compares generic instantiation bindings: `Wrap[int]` and
// `Wrap[string]` share Name+Pkg but instantiate differently — a declared
// type is identical only when its type arguments are.
func bindsEq(a, b map[string]runtime.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bindArgEq(av, bv) {
			return false
		}
	}
	return true
}

func bindArgEq(a, b runtime.Value) bool {
	at, aok := a.(*runtime.TypeDef)
	bt, bok := b.(*runtime.TypeDef)
	if aok != bok {
		return false
	}
	if !aok {
		return a == b
	}
	return sameTypeDef(at, bt)
}

// typeExprName renders a type AST to a comparable shape string for
// anonymous-type identity (approximation: structural equality by shape,
// not by the go/types identity rules).
// lenExprName renders an array-length expression inside a type-identity
// spelling — the `3` of `[3]int`, a named const, or `...`.
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

// typeExprNameCtx renders a type AST like typeExprName but package-aware:
// a non-predeclared ident spells as pkgPath.Name, and a selector `a.T`
// resolves through the file's import table to the imported path — so
// `[]Foo` typedefs written in different packages never spell equal, and
// `a.Foo`/`b.Foo` written under different aliases compare correctly.
// Predeclared names and unresolved selectors stay unqualified.
func typeExprNameCtx(e ast.Expr, file *syntax.File, pkg *runtime.Package) string {
	switch t := e.(type) {
	case *ast.Ident:
		if predeclaredTypeName(t.Name) {
			return t.Name
		}
		if pkg != nil {
			return pkg.Path + "." + t.Name
		}
		return t.Name
	case *ast.StarExpr:
		return "*" + typeExprNameCtx(t.X, file, pkg)
	case *ast.ArrayType:
		return "[" + lenExprName(t.Len) + "]" + typeExprNameCtx(t.Elt, file, pkg)
	case *ast.MapType:
		return "map[" + typeExprNameCtx(t.Key, file, pkg) + "]" + typeExprNameCtx(t.Value, file, pkg)
	case *ast.ChanType:
		return "chan " + typeExprNameCtx(t.Value, file, pkg)
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p := importPathFor(file, id.Name); p != "" {
				return p + "." + t.Sel.Name
			}
		}
		return typeExprNameCtx(t.X, file, pkg) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return typeExprNameCtx(t.X, file, pkg) + "[" + typeExprNameCtx(t.Index, file, pkg) + "]"
	case *ast.IndexListExpr:
		s := typeExprNameCtx(t.X, file, pkg) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += typeExprNameCtx(x, file, pkg)
		}
		return s + "]"
	case *ast.ParenExpr:
		return typeExprNameCtx(t.X, file, pkg)
	case *ast.Ellipsis:
		return "..." + typeExprNameCtx(t.Elt, file, pkg)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}

// predeclaredTypeName reports whether name is a predeclared type-ish
// identifier — basic types, aliases (byte, rune) and pseudo-types
// (error, any, comparable) — which never carry a package qualifier.
func predeclaredTypeName(name string) bool {
	switch name {
	case "bool", "string",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "float32", "float64", "complex64", "complex128",
		"error", "any", "comparable":
		return true
	}
	return false
}

// importPathFor resolves a file-local import alias (explicit or the
// basename-derived default) to its import path.
func importPathFor(file *syntax.File, alias string) string {
	if file == nil {
		return ""
	}
	for _, im := range file.Imports {
		if im.LocalName() == alias {
			return im.Path
		}
	}
	return ""
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
		k := td.Kind
		if k == runtime.KindNamedBasic || k == runtime.KindAlias {
			if u := v.peelNamed(td); u != nil {
				k = u.Kind
			}
		}
		switch k {
		case runtime.KindInterface:
			return runtime.NIL, nil
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
		case runtime.KindSlice, runtime.KindMap, runtime.KindChan, runtime.KindFunc, runtime.KindPointer:
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
	// a Named value converts through its underlying value — `string(x)` on
	// a named string value works like the underlying conversion; `T(x)`
	// on the same declared type is a no-op.
	if n, ok := x.(*runtime.Named); ok {
		if sameTypeDef(n.Typ, td) {
			return x, nil
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
		switch n := x.(type) {
		case int64:
			iv = n
		case float64:
			iv = int64(n)
		case string:
			iv = int64([]rune(n)[0]) // int("x") is the first rune's code point
		case *runtime.GoValue:
			// a boxed host integer (a wide uint64 literal, a reflect
			// result) converts by its host kind.
			rv := reflect.ValueOf(n.V)
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				iv = rv.Int()
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				iv = int64(rv.Uint())
			case reflect.Float32, reflect.Float64:
				iv = int64(rv.Float())
			default:
				return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
			}
		default:
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
		}
		if sizedIntName(td.Name) || td.Name == "int64" {
			// the converted value keeps its declared tag: arithmetic
			// re-wraps to the type's width (-u on uint8 yields 251), %T
			// prints the type name, and unsigned uint64 keeps its
			// domain for %x/%d. `int64(x)` tags too — an explicit
			// conversion declares its type — while bare ints stay
			// untagged (their %T already spells "int").
			return &runtime.Named{Typ: td, V: maskInt(iv, td.Name)}, nil
		}
		return maskInt(iv, td.Name), nil
	case "float32":
		switch x.(type) {
		case int64, float64:
			return &runtime.Named{Typ: td, V: float64(float32(toFloat(x)))}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "float64":
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
			return &runtime.Named{Typ: td, V: cv}, nil
		}
	}
	switch td.Kind {
	case runtime.KindSlice:
		return v.convertSlice(td, x)
	case runtime.KindMap:
		return v.convertMap(td, x)
	case runtime.KindChan:
		return v.convertChan(td, x)
	case runtime.KindPointer:
		return v.convertPointer(td, x)
	case runtime.KindStruct:
		return v.convertStruct(td, x)
	case runtime.KindInterface:
		ok, err := v.ifaceSatisfied(td, x)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
		}
		return x, nil
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
				return &runtime.Named{Typ: td, V: x}, nil
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
		return &runtime.Named{Typ: td, V: u}, nil
	}
	if td.Name != "" {
		return x, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
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
			el[i] = int64(b)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []rune:
		el := make([]runtime.Value, len(h))
		for i, r := range h {
			el[i] = int64(r)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("rune")}
	}
	return x
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

// convertSlice implements `[]T(x)`: the special string->byte/rune-slice
// conversions plus slice->slice when the underlying shapes are identical
// (element types compare by identity — []int does not convert to
// []MyIntElem, while []uint8 and []byte are the same type).
func (v *VM) convertSlice(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	switch s := x.(type) {
	case string:
		switch v.elemFamily(v.elemTypedef(v.topFrame(), td)) {
		case 'b':
			el := make([]runtime.Value, 0, len(s))
			for _, b := range []byte(s) {
				el = append(el, int64(b))
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		case 'r':
			el := make([]runtime.Value, 0, len(s))
			for _, r := range s {
				el = append(el, int64(r))
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert string to %s", tdName(td))
	case *runtime.Slice:
		if an, isArr := v.arrayLen(v.topFrame(), td); isArr {
			// [N]T(s) — slice-to-array conversion copies the first N
			// elements (too-short slices panic like Go's runtime check).
			if int64(len(s.Elems)) < an {
				panic(&runtime.Panic{Value: fmt.Sprintf("runtime error: cannot convert slice with length %d to array or pointer to array with length %d", len(s.Elems), an)})
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
	return &runtime.Named{Typ: td, V: m}, nil
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
	return &runtime.Named{Typ: td, V: ch}, nil
}

// convertPointer implements `*T(x)` and `P(x)` on pointer typedefs. A
// pointer in this model is a cell — it carries no swappable type tag, so
// the conversion checks the pointee's declared shape when one is known
// and passes the pointer itself through.
func (v *VM) convertPointer(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	if s, isSlice := runtime.Unwrap(x).(*runtime.Slice); isSlice {
		// (*[N]T)(s) — slice-to-array-pointer conversion shares the
		// slice's backing array (too-short slices panic like Go's).
		if et := v.elemTypedef(v.topFrame(), td); et != nil {
			if an, isArr := v.arrayLen(v.topFrame(), et); isArr {
				if int64(len(s.Elems)) < an {
					panic(&runtime.Panic{Value: fmt.Sprintf("runtime error: cannot convert slice with length %d to array or pointer to array with length %d", len(s.Elems), an)})
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
		return &runtime.Named{Typ: td, V: x}, nil
	}
	if v.H.ElemOf != nil {
		if et, err := v.H.ElemOf(td); err == nil && et != nil && declaredType(et) {
			return &runtime.Named{Typ: td, V: x}, nil
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
	src := u.Anon
	if src == nil && u.Spec != nil {
		src = u.Spec.Type
	}
	if src != nil {
		return v.shapeSpelling(src, u)
	}
	return normBasicName(u.Name)
}

// shapeSpelling renders a type expression to a comparable string in the
// context of its declaring typedef: generic binds substitute bound type
// parameters, non-predeclared idents qualify by package path, and
// `a.T` selectors resolve through the file's import table — so `[]Foo`
// in two packages never collides. byte and rune normalize to their
// canonical names so []byte and []uint8 spell identically (byte IS uint8).
func (v *VM) shapeSpelling(e ast.Expr, ctx *runtime.TypeDef) string {
	binds := ctx.Binds
	switch t := e.(type) {
	case *ast.Ident:
		if btd := boundTypedef(binds, t.Name); btd != nil {
			return v.boundShape(btd)
		}
		if predeclaredTypeName(t.Name) {
			return normBasicName(t.Name)
		}
		if ctx.Pkg != nil {
			return ctx.Pkg.Path + "." + t.Name
		}
		return normBasicName(t.Name)
	case *ast.StarExpr:
		return "*" + v.shapeSpelling(t.X, ctx)
	case *ast.ArrayType:
		return "[]" + v.shapeSpelling(t.Elt, ctx)
	case *ast.Ellipsis:
		return "[]" + v.shapeSpelling(t.Elt, ctx)
	case *ast.MapType:
		return "map[" + v.shapeSpelling(t.Key, ctx) + "]" + v.shapeSpelling(t.Value, ctx)
	case *ast.ChanType:
		return "chan " + v.shapeSpelling(t.Value, ctx)
	case *ast.ParenExpr:
		return v.shapeSpelling(t.X, ctx)
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p := importPathFor(ctx.File, id.Name); p != "" {
				return p + "." + t.Sel.Name
			}
		}
		return v.shapeSpelling(t.X, ctx) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return v.shapeSpelling(t.X, ctx) + "[" + v.shapeSpelling(t.Index, ctx) + "]"
	case *ast.IndexListExpr:
		s := v.shapeSpelling(t.X, ctx) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += v.shapeSpelling(x, ctx)
		}
		return s + "]"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}

// boundShape spells an instantiated type argument: a named type keeps its
// declared identity (T=MyInt spells "pkg.MyInt", not "int"), an anonymous
// shape spells structurally.
func (v *VM) boundShape(td *runtime.TypeDef) string {
	if td.Name != "" {
		if td.Pkg != nil {
			return td.Pkg.Path + "." + td.Name
		}
		return normBasicName(td.Name)
	}
	src := td.Anon
	if src == nil && td.Spec != nil {
		src = td.Spec.Type
	}
	if src != nil {
		return v.shapeSpelling(src, td)
	}
	return fmt.Sprintf("%p", td)
}

// boundTypedef returns the typedef a type parameter binds to, if any.
func boundTypedef(binds map[string]runtime.Value, name string) *runtime.TypeDef {
	if binds == nil {
		return nil
	}
	if bv, ok := binds[name]; ok {
		if btd, ok := bv.(*runtime.TypeDef); ok {
			return btd
		}
	}
	return nil
}

// normBasicName maps the predeclared aliases to their canonical names:
// byte IS uint8 and rune IS int32, so shapes spelled with either compare
// equal.
func normBasicName(name string) string {
	switch name {
	case "byte":
		return "uint8"
	case "rune":
		return "int32"
	}
	return name
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

// popArgs pops argc args off the stack. When spread is set the last arg
// must be a *Slice (f(xs...)) and is expanded in place.
func (v *VM) popArgs(f *frame, argc int, spread bool, pos token.Pos) []runtime.Value {
	args := make([]runtime.Value, argc)
	for i := argc - 1; i >= 0; i-- {
		args[i] = f.pop()
	}
	// args stay lazy across the boundary: the callee's declared-param
	// coerce applies Go's constant-to-type conversion (`f('a')` into an
	// int param), and host marshaling materializes what is left.
	if spread {
		if argc == 0 {
			f.trap("spread call with no arguments")
		}
		last := args[argc-1]
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
			args = args[:argc-1] // nil slice spreads to zero args
			return args
		}
		if n, ok := last.(*runtime.Named); ok {
			last = n.V
		}
		if _, isNil := last.(runtime.Nil); isNil {
			args = args[:argc-1]
			return args
		}
		if last == nil || last == runtime.NIL {
			// f(nil...) on a nil slice expands to zero arguments
			args = args[:argc-1]
			return args
		}
		s, ok := last.(*runtime.Slice)
		if !ok {
			f.trap("cannot use %T as spread argument", last)
		}
		args = append(args[:argc-1], s.Elems...)
	}
	return args
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
			panic(&runtime.Panic{Value: fmt.Sprintf("interface conversion: %s is not %s: missing method %s", typeNameOf(x), spelledTyp(td), miss)})
		}
	}
	staticName := "interface {}"
	if st, ok := static.(*runtime.TypeDef); ok {
		staticName = spelledTyp(st)
	}
	panic(&runtime.Panic{Value: fmt.Sprintf("interface conversion: %s is %s, not %s", staticName, typeNameOf(x), spelledTyp(td))})
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
	if td.Name != "" {
		switch td.Name {
		case "byte":
			return "uint8"
		case "rune":
			return "int32"
		case "any":
			return "interface {}"
		}
		// imported typedefs spell their own package ("io.Writer"); only
		// the script's own names need the package prefix.
		if td.Pkg != nil && td.Pkg.Name != "" && !strings.Contains(td.Name, ".") {
			return td.Pkg.Name + "." + td.Name
		}
		return td.Name
	}
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	return tdName(td)
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
		return xv.Def != nil && td.Name != "" && xv.Def.Name == td.Name && xv.Def.Pkg == td.Pkg && td.Pkg != nil && bindsEq(xv.Def.Binds, td.Binds)
	case int64:
		switch td.Name {
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8",
			"uint16", "uint32", "uint64", "byte", "rune", "uintptr":
			return true
		}
		// named basic type: match its underlying literal type name
		if td.Kind == runtime.KindNamedBasic && td.Anon != nil {
			if id, ok := td.Anon.(*ast.Ident); ok {
				return v.typeMatches(f, &runtime.TypeDef{Kind: td.Kind, Name: id.Name}, x)
			}
		}
		return false
	case float64:
		return td.Name == "float64" || td.Name == "float32"
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
		// anonymous func shapes kind-match; a declared func type asserts
		// on its Named tag only.
		return td.Kind == runtime.KindFunc && td.Spec == nil
	default:
		return false
	}
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
	return true, nil
}

// ---- declared types: zeros, typed nils, interface boxing (round 5) ----

// asTypedNil unwraps an interface-boxed nil down to its TypedNil.
func asTypedNil(x runtime.Value) (*runtime.TypedNil, bool) {
	if tn, ok := x.(*runtime.TypedNil); ok {
		return tn, true
	}
	if in, ok := x.(*runtime.IfaceNil); ok {
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
				if _, ok := runtime.Deref(r); !ok {
					r = &runtime.Cell{Elem: r}
				}
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
		if m, ok := td.Methods[name]; ok {
			// A value receiver dereferences a peeled pointer chain at
			// dispatch — panic on nil like Go. A nil carrying a nilable
			// typedef (declared pointer/slice/map/chan/func values can
			// be nil) is a valid receiver: the call binds it and the
			// body decides.
			if !m.PtrRecv {
				if _, isNil := asTypedNil(recv); isNil && (peeled || !v.nilableTypedef(td)) {
					panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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
	// it is a plain invalid select.
	if tn, ok := recv.(*runtime.TypedNil); ok && tn.Typ.Kind == runtime.KindPointer {
		panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
	}
	if in, ok := recv.(*runtime.IfaceNil); ok && in.Typ.Kind == runtime.KindPointer {
		panic(&runtime.Panic{Value: "runtime error: invalid memory address or nil pointer dereference"})
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

// setContainerTyp stamps a container value's declared-type tag.
func setContainerTyp(x runtime.Value, td *runtime.TypeDef) {
	switch t := x.(type) {
	case *runtime.Map:
		t.Typ = td
	case *runtime.Slice:
		t.Typ = td
	case *runtime.Chan:
		t.Typ = td
	}
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
					return &runtime.Named{Typ: td, V: x}
				}
			}
		}
		return x // host boundary: assignability is unknowable
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
	if tag := declaredTag(x); tag != nil {
		if sameTypeDef(tag, td) || sameTypeDef(tag, v.peelAlias(td)) {
			return x
		}
		if tagIsNamed(td) {
			f.trap("cannot use %s as %s", tdName(tag), tdName(td))
		}
	}
	utd := v.peelNamed(td)
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
			setContainerTyp(x, td)
		} else if !sameTypeDef(ct, td) {
			// two named container types do not re-bind (Go: named-to-named
			// needs a conversion); anonymous/underlying shapes may
			// re-bind only when the shapes match element-for-element.
			if ct.Name != "" && td.Name != "" {
				f.trap("cannot use %s as %s", tdName(ct), tdName(td))
			}
			if !v.tdShapeEq(ct, td) {
				f.trap("cannot use %s as %s", tdName(ct), tdName(td))
			}
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
			return &runtime.Named{Typ: td, V: x}
		}
	case runtime.KindPointer, runtime.KindFunc:
		// declared pointer/func types tag the bound value so asserts
		// check declared identity and member access sees only the
		// declared method set. Anonymous *T/func() binds stay bare —
		// they carry the pointee's members (T's method set promotes).
		if td.Spec != nil {
			return &runtime.Named{Typ: td, V: x}
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
		return typeExprNameCtx(pa.Anon, pa.File, pa.Pkg) == typeExprNameCtx(pb.Anon, pb.File, pb.Pkg)
	}
	return pa.Anon == nil && pb.Anon == nil
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
	// an array typedef materializes a fixed-length slice of element
	// zeros — `var a [3]int` yields [0 0 0], not a nil slice.
	if n, isArr := v.arrayLen(f, td); isArr {
		zv := v.zeroSeen(f, v.elemTypedef(f, td), seen)
		el := make([]runtime.Value, n)
		for i := range el {
			el[i] = zv
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
	return &runtime.Named{Typ: td, V: z}
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
		// a named const: resolve through the typedef's package index.
		if td.Pkg != nil && td.Pkg.Index != nil && v.H.Materialize != nil {
			if d := td.Pkg.Index.Consts[l.Name]; d != nil {
				if mv, err := v.H.Materialize(td.Pkg, d); err == nil {
					if n, ok := runtime.Unwrap(mv).(int64); ok {
						return n, true
					}
				}
			}
		}
	}
	return 0, false
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

func typeNameOf(x runtime.Value) string {
	switch xv := x.(type) {
	case *runtime.UConst:
		return xv.DefaultName()
	case *runtime.Named:
		return spelledTyp(xv.Typ)
	case *runtime.Struct:
		if xv.Def != nil && xv.Def.Name != "" {
			return spelledTyp(xv.Def)
		}
		return "struct"
	case *runtime.GoValue:
		// a host box names its Go type — "*errors.errorString" in a
		// conversion panic, like the real runtime prints it.
		return fmt.Sprintf("%T", xv.V)
	case int64:
		return "int64"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case *runtime.Slice:
		return "slice"
	case *runtime.Map:
		return "map"
	case runtime.Nil:
		return "nil"
	default:
		return fmt.Sprintf("%T", x)
	}
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
func (v *VM) inferBinds(fn *runtime.Function, args []runtime.Value) (*runtime.Function, error) {
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
			v.unifyType(ctx, tset, binds, et, args[pos])
			pos++
		}
		// ...T consumes all remaining args; the first arg that yields a
		// typedef wins the binding.
		for variadic && pos < len(args) {
			v.unifyType(ctx, tset, binds, et, args[pos])
			pos++
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
func (v *VM) unifyType(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat ast.Expr, arg runtime.Value) {
	conc := v.argTypedef(arg)
	if conc == nil {
		return
	}
	v.unifyTypeDef(ctx, tset, binds, pat, conc)
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

// funcSig returns the declared signature of a function value.
func funcSig(x runtime.Value) (*ast.FuncType, *runtime.Package, *syntax.File, map[string]runtime.Value) {
	switch fn := x.(type) {
	case *runtime.Function:
		if fn.Decl != nil && fn.Decl.Type != nil {
			return fn.Decl.Type, fn.Pkg, fn.File, fn.Binds
		}
	case *runtime.Closure:
		if fn.Fn != nil && fn.Fn.Decl != nil && fn.Fn.Decl.Type != nil {
			return fn.Fn.Decl.Type, fn.Fn.Pkg, fn.Fn.File, fn.Fn.Binds
		}
	case *runtime.BoundMethod:
		if fn.Fn != nil && fn.Fn.Decl != nil && fn.Fn.Decl.Type != nil {
			return fn.Fn.Decl.Type, fn.Fn.Pkg, fn.Fn.File, fn.Fn.Binds
		}
	}
	return nil, nil, nil, nil
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
	}
	return v.typeOfValue(x)
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
