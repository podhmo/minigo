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

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
