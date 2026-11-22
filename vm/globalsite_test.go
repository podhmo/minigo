package vm

import (
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
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
	// firstRead primes the site through env and checks the initial
	// read — the shared preamble the mutation subtests run before
	// perturbing the env.
	firstRead := func(t *testing.T, env *runtime.Env, want runtime.Value) (*VM, *bytecode.Chunk) {
		t.Helper()
		v := &VM{}
		ch := chunk()
		got, err := call(v, ch, env)
		if err != nil {
			t.Fatalf("first read: %v", err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("first read (-want +got):\n%s", diff)
		}
		return v, ch
	}

	t.Run("rebind invalidates", func(t *testing.T) {
		env := runtime.NewEnv()
		env.Set("x", &runtime.Cell{Elem: 1})
		v, ch := firstRead(t, env, 1)
		env.Set("x", &runtime.Cell{Elem: 2})
		got, err := call(v, ch, env)
		if err != nil {
			t.Fatalf("after rebind: %v", err)
		}
		if diff := cmp.Diff(2, got); diff != "" {
			t.Fatalf("after rebind, gen bump must re-resolve (-want +got):\n%s", diff)
		}
	})

	t.Run("cell reads live", func(t *testing.T) {
		env := runtime.NewEnv()
		cell := &runtime.Cell{Elem: 1}
		env.Set("x", cell)
		v, ch := firstRead(t, env, 1)
		cell.Elem = 42
		got, err := call(v, ch, env)
		if err != nil {
			t.Fatalf("after cell write: %v", err)
		}
		if diff := cmp.Diff(42, got); diff != "" {
			t.Fatalf("after cell write, no rebind needed (-want +got):\n%s", diff)
		}
	})

	t.Run("env guard isolates packages", func(t *testing.T) {
		envA := runtime.NewEnv()
		envA.Set("x", &runtime.Cell{Elem: 1})
		envB := runtime.NewEnv()
		envB.Set("x", &runtime.Cell{Elem: 2})
		v := &VM{}
		ch := chunk()
		got, err := call(v, ch, envA)
		if err != nil {
			t.Fatalf("envA read: %v", err)
		}
		if diff := cmp.Diff(1, got); diff != "" {
			t.Fatalf("envA read (-want +got):\n%s", diff)
		}
		got, err = call(v, ch, envB)
		if err != nil {
			t.Fatalf("envB read: %v", err)
		}
		if diff := cmp.Diff(2, got); diff != "" {
			t.Fatalf("envB read, foreign env must miss the slot (-want +got):\n%s", diff)
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
					if diff := cmp.Diff(7, got); diff != "" {
						errs <- fmt.Errorf("goroutine %d read (-want +got):\n%s", i, diff)
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
