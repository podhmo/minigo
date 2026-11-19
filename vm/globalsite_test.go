package vm

import (
	"fmt"
	"sync"
	"testing"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// P4-5a: OpGlobal's site cache answers a name while the package's Globals
// generation is unchanged, re-resolves on a new binding, reads a cached
// cell live, and cannot leak one package's answer into another env.
func TestGlobalSiteCache(t *testing.T) {
	chunk := func() *bytecode.Chunk {
		return &bytecode.Chunk{
			Code: []bytecode.Instruction{
				{Op: bytecode.OpGlobal, A: 0, C: 0},
				{Op: bytecode.OpReturn, A: 1},
			},
			Consts: []any{"x"},
			Sites:  []*bytecode.GlobalSite{{}},
		}
	}
	call := func(v *VM, ch *bytecode.Chunk, env *runtime.Env) (runtime.Value, error) {
		return v.Call(&runtime.Function{Chunk: ch, Pkg: &runtime.Package{PackageInstance: runtime.PackageInstance{Globals: env}}}, nil)
	}

	t.Run("rebind invalidates", func(t *testing.T) {
		env := runtime.NewEnv()
		env.Set("x", &runtime.Cell{Elem: 1})
		v := &VM{}
		ch := chunk()
		if got, err := call(v, ch, env); err != nil || got != 1 {
			t.Fatalf("first read = %v, %v; want 1", got, err)
		}
		env.Set("x", &runtime.Cell{Elem: 2})
		if got, err := call(v, ch, env); err != nil || got != 2 {
			t.Fatalf("after rebind = %v, %v; want 2 (gen bump re-resolves)", got, err)
		}
	})

	t.Run("cell reads live", func(t *testing.T) {
		env := runtime.NewEnv()
		cell := &runtime.Cell{Elem: 1}
		env.Set("x", cell)
		v := &VM{}
		ch := chunk()
		if got, err := call(v, ch, env); err != nil || got != 1 {
			t.Fatalf("first read = %v, %v; want 1", got, err)
		}
		cell.Elem = 42
		if got, err := call(v, ch, env); err != nil || got != 42 {
			t.Fatalf("after cell write = %v, %v; want 42 (no rebind needed)", got, err)
		}
	})

	t.Run("env guard isolates packages", func(t *testing.T) {
		envA := runtime.NewEnv()
		envA.Set("x", &runtime.Cell{Elem: 1})
		envB := runtime.NewEnv()
		envB.Set("x", &runtime.Cell{Elem: 2})
		v := &VM{}
		ch := chunk()
		if got, err := call(v, ch, envA); err != nil || got != 1 {
			t.Fatalf("envA read = %v, %v; want 1", got, err)
		}
		if got, err := call(v, ch, envB); err != nil || got != 2 {
			t.Fatalf("envB read = %v, %v; want 2 (foreign env misses the slot)", got, err)
		}
	})

	t.Run("concurrent readers agree", func(t *testing.T) {
		env := runtime.NewEnv()
		env.Set("x", &runtime.Cell{Elem: 7})
		ch := chunk()
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for i := range 32 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				v := &VM{} // one VM per goroutine, like execution does
				for range 100 {
					got, err := call(v, ch, env)
					if err != nil {
						errs <- fmt.Errorf("goroutine %d: %w", i, err)
						return
					}
					if got != 7 {
						errs <- fmt.Errorf("goroutine %d: read %v, want 7", i, got)
						return
					}
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	})
}

// BenchmarkGlobalSite measures the site's cached OpGlobal read against
// the uncached resolveGlobalKind walk it replaces. P4-5a bench delta.
func BenchmarkGlobalSite(b *testing.B) {
	env := runtime.NewEnv()
	env.Set("x", &runtime.Cell{Elem: 1})
	pkg := &runtime.Package{PackageInstance: runtime.PackageInstance{Globals: env}}
	v := &VM{}
	f := &frame{v: v, fn: &runtime.Function{Pkg: pkg}}

	b.Run("hit", func(b *testing.B) {
		site := &bytecode.GlobalSite{}
		site.Cache.Store(&globalSlot{env: env, gen: env.Gen(), cell: &runtime.Cell{Elem: 1}})
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			// the OpGlobal fast path verbatim: slot still valid -> push
			s, _ := site.Cache.Load().(*globalSlot)
			if s != nil && s.env == env && s.gen == env.Gen() {
				if s.cell != nil {
					f.push(s.cell.Elem)
				} else {
					f.push(s.val)
				}
			}
			f.sp = 0
		}
	})

	b.Run("resolve", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, _, _, err := v.resolveGlobalKind(f, "x"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
