// Stdlib intrinsics: a small native surface bound into every engine so
// interpreted code doesn't have to parse GOROOT sources for the common
// cases. Script values are marshalled to Go natives at the boundary
// (hostbridge.GoNative) so the real fmt/strconv/... implementations do the work.
// Errors return Go-style (value, err) tuples where the real API has one.
//
// Deliberately small — anything absent falls through to lazy GOROOT source
// interpretation, which stays the documented path for full fidelity.

package minigo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdfmt "github.com/podhmo/minigo/internal/stdlib/fmt"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	stdlibos "github.com/podhmo/minigo/internal/stdlib/os"
	stdtime "github.com/podhmo/minigo/internal/stdlib/time"
	"github.com/podhmo/minigo/runtime"
)

var ()

// hostType delegates to the shared binding machinery in internal/stdlib.
func hostType(name string, new func() any) *runtime.TypeDef {
	return stdlib.HostType(name, new)
}

// hostHelpers builds BuiltinFuncs whose Fn marshals arguments to Go natives
// and results back to runtime values.
type hostHelpers struct {
	env bind.Env // the narrow host-environment surface the bindings see
}

func (h *hostHelpers) Fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fnvc(name, func(_ runtime.VMCaller, a []any) (any, error) { return f(a) }, target...)
}

// fnvc is fn for bindings that need the caller (e.g. deadlock-wait
// bookkeeping through the channel a host call hands to the script).
func (h *hostHelpers) Fnvc(name string, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(vc, a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fnvcRaw is an arity-checked fnvc that also hands f the raw script
// args — an error argument must reach hostErrOf unflattened so a script
// error keeps its identity through the host (hostbridge.GoNative would format it).
func (h *hostHelpers) FnvcRaw(name string, arity int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != arity {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, arity, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(vc, a, args)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// The exported variants satisfy stdlib.Helpers for the moved
// installers (P2-12c).
// Fn1/Fn2/Fn3 are arity-checked variants.
func (h *hostHelpers) Fn1(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 1, f, target...)
}

func (h *hostHelpers) Fn2(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 2, f, target...)
}

func (h *hostHelpers) Fn3(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 3, f, target...)
}

func (h *hostHelpers) Arity(name string, n int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < n {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, n, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// scriptVal delegates to the shared binding machinery in internal/stdlib.
func scriptVal(v any) runtime.Value {
	return stdlib.ScriptVal(v)
}

// timerChanBox/tickerChanBox/afterFuncTimer moved with the time
// installer (P2-12c); the aliases keep typeid_test.go's references.
type timerChanBox = stdtime.TimerChanBox
type afterFuncTimer = stdtime.AfterFuncTimer

// proxyHostType resolves the public spelling of a host type — the
// TypeAlias hook for the reflect facade (moved with the time
// installer, P2-12c).
func proxyHostType(rt reflect.Type) reflect.Type {
	return stdtime.ProxyHostType(rt)
}

// proxyUnwrapHost is the facade's UnwrapHost hook — the inverse of
// proxyHostType (P2-12c).
func proxyUnwrapHost(v any, t reflect.Type) (reflect.Value, bool) {
	return stdtime.ProxyUnwrapHost(v, t)
}

func byteSlice(v any) []byte {
	return stdlib.ByteSlice(v)
}

// asCell peels Named tags to reach the addressable cell underneath —
// the pointee slot whose Typ is the declared (static) type. Ref kinds
// (FieldRef/IndexRef) are not cells and report nil.

// ioAdapters bundles the env hooks the moved io package needs
// (P2-12): the declared method set and the fmt argument unwrap.
func (h *hostHelpers) ioAdapters() *stdio.Adapters {
	return &stdio.Adapters{
		MethodSetOf: func(v any) map[string]bool {
			set, _, _ := h.env.MethodSetOfValue(v)
			return set
		},
		UnwrapArg: stdfmt.UnwrapFmtArg,
	}
}

func (h *hostHelpers) asReaderVM(vc runtime.MemberCaller, v any) (io.Reader, error) {
	return h.ioAdapters().AsReaderVM(vc, v)
}

func asWriterVM(vc runtime.MemberCaller, v any) (io.Writer, error) {
	return defaultIOAdapters.AsWriterVM(vc, v)
}

var defaultIOAdapters = &stdio.Adapters{UnwrapArg: stdfmt.UnwrapFmtArg}

// out returns the environment's output writer (io.Discard when unset).
func (h *hostHelpers) out() io.Writer {
	if h.env == nil {
		return io.Discard
	}
	return h.env.Out()
}

// engineFileOf unwraps the script-visible os.Stdout facade to the real
// file for net's FileConn/FilePacketConn adapters (P2-12).
func engineFileOf(v any) *os.File {
	return stdlibos.EngineFileOf(v)
}

// cwdAbs anchors a script path at the virtual cwd without a roots check —
// for pure path math (filepath.Abs/Rel) where no filesystem is touched.
func (e *Engine) cwdAbs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(e.cwd, p)
}
