package minigo

import (
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installRuntime(h *hostHelpers) {
	e.Bind("runtime", map[string]runtime.Value{
		"GOOS":   goruntime.GOOS,
		"GOARCH": goruntime.GOARCH,
		"NumGoroutine": h.fn("runtime.NumGoroutine", func(a []any) (any, error) {
			return goruntime.NumGoroutine(), nil
		}),
		"Gosched": h.fn("runtime.Gosched", func(a []any) (any, error) {
			goruntime.Gosched()
			return nil, nil
		}),
		"NumCPU": h.fn("runtime.NumCPU", func(a []any) (any, error) { return goruntime.NumCPU(), nil }),
		"GOMAXPROCS": &runtime.BuiltinFunc{Name: "runtime.GOMAXPROCS", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// read-only on the script side: GOMAXPROCS(0) reports the
			// current setting without mutating the host process's
			// parallelism.
			return goruntime.GOMAXPROCS(0), nil
		}},
		"Version":  h.fn("runtime.Version", func(a []any) (any, error) { return goruntime.Version(), nil }),
		"GC":       h.fn("runtime.GC", func(a []any) (any, error) { return nil, nil }),
		"Compiler": "gc",
		// //go:linkname targets other packages wire to (maphash's
		// runtime_rand/runtime_memhash): unexported, so only reachable
		// through the linkname hook. rand feeds seed generation;
		// memhash is a deterministic byte-span hash keyed by seed —
		// gc's aeshash is process-random anyway, so parity lives in the
		// contract (same input, same hash), not the value.
		"rand": &runtime.BuiltinFunc{Name: "runtime.rand", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			var b [8]byte
			if _, err := crand.Read(b[:]); err != nil {
				return nil, err
			}
			// the decl is `func rand() uint64`; returning int64 would
			// leak a different dynamic type to the script side.
			return uint64(binary.LittleEndian.Uint64(b[:])), nil
		}},
		// the coroutine leaves iter.Pull/Pull2 linkname to: a coro is a
		// rendezvous holding exactly one parked goroutine — coroswitch
		// releases the one parked inside and parks the caller, so the
		// seq worker and the next/stop caller alternate control.
		"newcoro": &runtime.BuiltinFunc{Name: "runtime.newcoro", RetainsArgs: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("runtime.newcoro needs 1 arg, got %d", len(args))
			}
			c := &scriptCoro{ch: make(chan struct{})}
			cv := &runtime.GoValue{V: c}
			// the worker parks in c before running f like gc's
			// coroutine does, and its exit releases whoever is parked
			// in c — coroexit's half-switch that does not park itself.
			// Both parks are registered channel ops, so the rendezvous
			// counts toward deadlock detection and a process ending
			// unwinds a parked side with procExit instead of leaking it.
			vc.Spawn(&runtime.BuiltinFunc{Name: "runtime.newcoro worker", Fn: func(wc runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				defer func() { vm.ParkedChanSend(wc, c.ch) }()
				vm.ParkedChanRecv(wc, c.ch)
				_, err := wc.Call(args[0], []runtime.Value{cv})
				return nil, err
			}}, nil)
			return cv, nil
		}},
		"coroswitch": &runtime.BuiltinFunc{Name: "runtime.coroswitch", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("runtime.coroswitch needs 1 arg, got %d", len(args))
			}
			c, ok := goNative(args[0]).(*scriptCoro)
			if !ok {
				return nil, fmt.Errorf("runtime.coroswitch needs a *coro, got %T", goNative(args[0]))
			}
			vm.ParkedChanSend(vc, c.ch) // release the goroutine parked in c…
			vm.ParkedChanRecv(vc, c.ch) // …and become it
			return nil, nil
		}},
		"memhash": &runtime.BuiltinFunc{Name: "runtime.memhash", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("runtime.memhash needs 3 args, got %d", len(args))
			}
			elems, err := unsafeElems(args[0], args[2])
			if err != nil {
				return nil, err
			}
			// FNV-1a over the byte span, keyed by the seed and put
			// through a murmur-style finalizer.
			h := uint64(int64Of(goNative(args[1]))) ^ 14695981039346656037
			for _, el := range elems {
				h ^= uint64(int64Of(goNative(el))) & 0xFF
				h *= 1099511628211
			}
			h ^= h >> 33
			h *= 0xff51afd7ed558ccd
			h ^= h >> 33
			h *= 0xc4ceb9fe1a85ec53
			h ^= h >> 33
			return uintptr(h), nil
		}},
		// runtime.GOROOT is deprecated for the host; report the env's
		// root, falling back to `go env GOROOT` like go/build does.
		"GOROOT": h.fn("runtime.GOROOT", func(a []any) (any, error) {
			if gr := os.Getenv("GOROOT"); gr != "" {
				return gr, nil
			}
			out, err := exec.Command("go", "env", "GOROOT").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}),
		// a debugger trap is a no-op for the interpreter — the program
		// just continues, which is what these tests rely on.
		"Breakpoint": h.fn("runtime.Breakpoint", func(a []any) (any, error) { return nil, nil }),
		// no GC: registering a finalizer is accepted but never fires —
		// same observable behavior as a run with no GC pressure.
		"SetFinalizer": h.fn("runtime.SetFinalizer", func(a []any) (any, error) {
			if len(a) != 2 {
				return nil, errors.New("runtime.SetFinalizer needs 2 args")
			}
			return nil, nil
		}),
		// AddCleanup is inert like SetFinalizer: the cleanup never fires
		// without a GC, so the call only has to hand back a Cleanup
		// handle whose Stop is a no-op (weak.Make-based registrars like
		// Songmu/flagx call it once per FlagSet).
		"AddCleanup": h.fn("runtime.AddCleanup", func(a []any) (any, error) {
			if len(a) != 3 {
				return nil, fmt.Errorf("runtime.AddCleanup needs 3 args, got %d", len(a))
			}
			return &runtime.GoValue{V: cleanupHandle{}}, nil
		}),
		"Cleanup":        hostType("runtime.Cleanup", func() any { return cleanupHandle{} }),
		"MemProfileRate": &runtime.Cell{Elem: int64(512 * 1024)},
		// liveness hints and heap profiling have no GC to act on —
		// KeepAlive is a no-op; MemProfile reports no records.
		"KeepAlive": h.fn("runtime.KeepAlive", func(a []any) (any, error) { return nil, nil }),
		"MemProfile": &runtime.BuiltinFunc{Name: "runtime.MemProfile", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return &runtime.Tuple{Elems: []runtime.Value{int64(0), true}}, nil
		}},
		"MemProfileRecord": &runtime.TypeDef{
			Name: "runtime.MemProfileRecord", Kind: runtime.KindStruct,
			Fields: []string{"AllocBytes", "FreeBytes", "AllocObjects", "FreeObjects"},
		},
		"Error":    &runtime.TypeDef{Name: "runtime.Error", Kind: runtime.KindInterface, MReqs: []string{"Error", "RuntimeError"}},
		"MemStats": hostType("runtime.MemStats", func() any { return &goruntime.MemStats{} }),
		// the engine's own package handle — scripts get *runtime.Package
		// values from inspect.DirOf/Current; the typedef lets them spell
		// that type in signatures and asserts with one identity.
		"Package": hostType("runtime.Package", func() any { return &runtime.Package{} }),
		"ReadMemStats": h.fn("runtime.ReadMemStats", func(a []any) (any, error) {
			m, ok := a[0].(*goruntime.MemStats)
			if !ok {
				return nil, fmt.Errorf("ReadMemStats needs *runtime.MemStats, got %T", a[0])
			}
			// Serving the host allocator's counters leaks interpreter
			// activity into script assertions: any delta check
			// (`n0 != m.Mallocs`) false-positives and depends on GC
			// timing. minigo exposes no per-script heap accounting, so
			// fill one snapshot and serve it thereafter — every delta
			// reads as 0 and sanity checks like `m.Sys > 0` still hold.
			memStatsOnce.Do(func() { goruntime.ReadMemStats(&memStatsSnapshot) })
			*m = memStatsSnapshot
			return nil, nil
		}),
		"Caller": &runtime.BuiltinFunc{Name: "runtime.Caller", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, errors.New("runtime.Caller needs 1 arg")
			}
			// pcs is top-first: index 0 is the innermost live frame —
			// the builtin's own call site, which is Caller(0) in Go.
			skip := intOf(goNative(args[0]))
			pcs := vc.CallerPCs()
			if skip < 0 || skip >= len(pcs) {
				return &runtime.Tuple{Elems: []runtime.Value{int64(0), "", int64(0), false}}, nil
			}
			pc := pcs[skip]
			s, _ := vc.CallerFrame(pc)
			return &runtime.Tuple{Elems: []runtime.Value{int64(pc), s.File, int64(s.Line), true}}, nil
		}},
		"Callers": &runtime.BuiltinFunc{Name: "runtime.Callers", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 2 {
				return nil, errors.New("runtime.Callers needs 2 args")
			}
			sl, ok := args[1].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("runtime.Callers: pc slice is %T", args[1])
			}
			// index 0 is the Callers builtin itself, like Go — CallerFrame
			// never resolves it. skip drops that many leading PCs.
			pcs := append([]uintptr{0}, vc.CallerPCs()...)
			skip := intOf(goNative(args[0]))
			if skip > len(pcs) {
				skip = len(pcs)
			}
			pcs = pcs[skip:]
			n := len(pcs)
			if len(sl.Elems) < n {
				n = len(sl.Elems)
			}
			for i := 0; i < n; i++ {
				sl.Elems[i] = int64(pcs[i])
			}
			return int64(n), nil
		}},
		"CallersFrames": &runtime.BuiltinFunc{Name: "runtime.CallersFrames", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			sl, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("runtime.CallersFrames: pcs is %T", args[0])
			}
			sites := []runtime.CallSite{}
			for _, e := range sl.Elems {
				if s, ok := vc.CallerFrame(uintptr(int64Of(goNative(e)))); ok {
					sites = append(sites, s)
				}
			}
			return &runtime.GoValue{V: &callerFrames{sites: sites}}, nil
		}},
	})
}

// callerFrames is the script-side *runtime.Frames: it iterates the
// call sites a runtime.Callers snapshot captured.
type callerFrames struct {
	sites []runtime.CallSite
	i     int
}

// callerFrame is the script-side runtime.Frame.
type callerFrame struct {
	PC       uintptr
	Func     *callerFunc
	Function string
	File     string
	Line     int
	Entry    uintptr
}

// callerFunc is the script-side *runtime.Func (only Name() is used).
type callerFunc struct{ name string }

// Next implements (*runtime.Frames).Next — the bool reports whether a
// further call would yield another frame, so the last real frame
// returns false and `for { f, more := frames.Next(); ...; if !more {
// break } }` does not process an extra empty frame.
func (cf *callerFrames) Next() (callerFrame, bool) {
	if cf.i >= len(cf.sites) {
		return callerFrame{}, false
	}
	s := cf.sites[cf.i]
	cf.i++
	return callerFrame{
		PC:       uintptr(cf.i),
		Func:     &callerFunc{name: s.Name},
		Function: s.Name,
		File:     s.File,
		Line:     s.Line,
	}, cf.i < len(cf.sites)
}

// Name implements (*runtime.Func).Name.
func (f *callerFunc) Name() string { return f.name }
