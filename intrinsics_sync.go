package minigo

import (
	"fmt"
	"sync"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installSync(h *hostHelpers) {
	// sync: the real host types back `var wg sync.WaitGroup` — TypeDef.HostNew
	// boxes a fresh Go value per zero, and member access dispatches through
	// the host method set (Lock/Unlock, Add/Wait/Done, Do).
	e.Bind("sync", map[string]runtime.Value{
		"WaitGroup": hostType("sync.WaitGroup", func() any { return &sync.WaitGroup{} }),
		"Mutex":     hostType("sync.Mutex", func() any { return &sync.Mutex{} }),
		"RWMutex":   hostType("sync.RWMutex", func() any { return &sync.RWMutex{} }),
		"Once":      hostType("sync.Once", func() any { return &sync.Once{} }),
		"Map":       hostType("sync.Map", func() any { return &sync.Map{} }),
		"Pool":      hostType("sync.Pool", func() any { return &sync.Pool{} }),
		"Cond":      hostType("sync.Cond", func() any { return &sync.Cond{} }),
		// an interface typedef: `var l sync.Locker` / parameter coercion
		// duck-type against the reflect method set (GoValue) or the
		// declared method set (script types).
		"Locker": &runtime.TypeDef{
			Name: "sync.Locker", Kind: runtime.KindInterface,
			MReqs: []string{"Lock", "Unlock"},
		},
		"NewCond": &runtime.BuiltinFunc{Name: "sync.NewCond", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("sync.NewCond needs 1 arg, got %d", len(args))
			}
			l, err := lockerOf(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: sync.NewCond(l)}, nil
		}},
		// sync.OnceFunc family: memoization lives on the host (a real
		// sync.Once), while the wrapped function runs on whichever VM
		// calls the result — like Go, a panic on the first call still
		// consumes the once.
		"OnceFunc": &runtime.BuiltinFunc{Name: "sync.OnceFunc", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceFunc", args)
		}},
		"OnceValue": &runtime.BuiltinFunc{Name: "sync.OnceValue", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceValue", args)
		}},
		"OnceValues": &runtime.BuiltinFunc{Name: "sync.OnceValues", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceValues", args)
		}},
	})
}
