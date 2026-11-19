package vm

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// TestSelectSiteCache exercises OpSelect's per-site cache: the slot keys
// on the receiver typedef's TypeID, so one instruction resolving the
// same member name on two struct types re-resolves on every swap and
// still answers correctly — and parallel VMs share the slot safely.
func TestSelectSiteCache(t *testing.T) {
	defA := &runtime.TypeDef{Name: "A", Kind: runtime.KindStruct, Fields: []string{"Tag", "N"}}
	defB := &runtime.TypeDef{Name: "B", Kind: runtime.KindStruct, Fields: []string{"N", "Tag"}}
	tids := map[*runtime.TypeDef]runtime.TypeID{defA: "tidA", defB: "tidB"}
	mkVM := func() *VM {
		v := &VM{}
		v.H.TypeIDOf = func(td *runtime.TypeDef) runtime.TypeID { return tids[td] }
		return v
	}

	site := &bytecode.GlobalSite{}
	// each chunk selects N on its own receiver; both share one site,
	// like one instruction under polymorphic dispatch.
	chunk := func(recv runtime.Value) *bytecode.Chunk {
		return &bytecode.Chunk{
			Consts: []any{recv, "N"},
			Sites:  []*bytecode.GlobalSite{site},
			Code: []bytecode.Instruction{
				{Op: bytecode.OpConst, A: 0},
				{Op: bytecode.OpSelect, A: 1, C: 0},
				{Op: bytecode.OpReturn, A: 1},
			},
		}
	}
	a := &runtime.Struct{Def: defA, Fields: []runtime.Value{"x", int64(7)}}
	b := &runtime.Struct{Def: defB, Fields: []runtime.Value{int64(9), "y"}}
	call := func(v *VM, recv runtime.Value) (runtime.Value, error) {
		return v.Call(&runtime.Function{Chunk: chunk(recv), Pkg: &runtime.Package{}}, nil)
	}

	t.Run("alternating typedefs at one site", func(t *testing.T) {
		v := mkVM()
		for i, tc := range []struct {
			recv *runtime.Struct
			want runtime.Value
			tid  runtime.TypeID
		}{
			{a, int64(7), "tidA"}, // miss: resolve, store A's slot
			{b, int64(9), "tidB"}, // tid mismatch: resolve B (N at index 0)
			{a, int64(7), "tidA"}, // mismatch again: back to A (index 1)
		} {
			got, err := call(v, tc.recv)
			if err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("call %d result (-want +got):\n%s", i, diff)
			}
			slot, _ := site.Cache.Load().(*selectSlot)
			if slot == nil {
				t.Fatalf("call %d: no slot stored", i)
			}
			if diff := cmp.Diff(tc.tid, slot.tid); diff != "" {
				t.Fatalf("call %d slot tid (-want +got):\n%s", i, diff)
			}
		}
	})

	t.Run("concurrent selects share the slot", func(t *testing.T) {
		const n = 8
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				v := mkVM()
				recv := &runtime.Struct{Def: defA, Fields: []runtime.Value{"t", int64(i)}}
				for range 200 {
					got, err := call(v, recv)
					if err != nil {
						errs <- err
						return
					}
					if diff := cmp.Diff(int64(i), got); diff != "" {
						errs <- &testErr{msg: diff}
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

// TestInstantiateSite covers the OpInstantiate memo's guards: a chunk
// with no Sites runs without one (hand-built bytecode), and a slot
// pinned on (base, targs) identity still misses once the base's
// declaring env redeclares — instTypeKey reads constraint names that a
// REPL redeclare repoints without changing either pointer.
func TestInstantiateSite(t *testing.T) {
	t.Run("chunk without sites instantiates", func(t *testing.T) {
		gen := &runtime.BuiltinFunc{
			Name: "mk",
			GenFn: func(vm runtime.VMCaller, targs, args []runtime.Value) (runtime.Value, error) {
				return targs[0], nil
			},
		}
		want := &runtime.TypeDef{Name: "int", Kind: runtime.KindNamedBasic}
		chunk := &bytecode.Chunk{
			Consts: []any{gen, want},
			Code: []bytecode.Instruction{
				{Op: bytecode.OpConst, A: 0},
				{Op: bytecode.OpConst, A: 1},
				{Op: bytecode.OpInstantiate, A: 1},
				{Op: bytecode.OpReturn, A: 1},
			},
		}
		got, err := (&VM{}).Call(&runtime.Function{Chunk: chunk, Pkg: &runtime.Package{}}, nil)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		inst, ok := got.(*runtime.BuiltinFunc)
		if !ok {
			t.Fatalf("want *BuiltinFunc, got %T", got)
		}
		r, err := inst.Fn(nil, nil)
		if err != nil {
			t.Fatalf("inst call: %v", err)
		}
		if r != want {
			t.Fatalf("want %p, got %p", want, r)
		}
	})

	t.Run("env redeclare invalidates the slot", func(t *testing.T) {
		env := runtime.NewEnv()
		base := &runtime.TypeDef{Name: "C", Kind: runtime.KindStruct}
		arg := &runtime.TypeDef{Name: "int", Kind: runtime.KindNamedBasic}
		s := &instSiteSlot{base: base, env: env, gen: env.Gen(), targs: []runtime.Value{arg}}
		if !s.match(base, []runtime.Value{arg}) {
			t.Fatal("same gen must match")
		}
		env.Touch() // a constraint redeclare bumps the declaring env's generation
		if s.match(base, []runtime.Value{arg}) {
			t.Fatal("new gen must miss even with identical pointers")
		}
	})
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
