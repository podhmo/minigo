package minigo

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// TestIfaceMemoMethodSetEpoch pins that a VM's interface-satisfaction
// memo drops its answers when a method set is edited in place. Each
// Engine.Call runs on a fresh VM, so the case needs one long-lived VM:
// the same VM asks before and after a method is grafted onto G (the
// REPL's pin-mode graft does the same edit, then MethodSetsChanged).
func TestIfaceMemoMethodSetEpoch(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	p, err := e.Package(ctx, "./testdata/ifacememo")
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	vmm := e.newVM()
	vmm.EnsureProc()
	defer vmm.ReleaseProc()
	member := func(name string) runtime.Value {
		t.Helper()
		v, err := p.MemberV(name, e.materialize, func(fn *runtime.Function) error {
			_, err := vmm.Call(fn, nil)
			return err
		})
		if err != nil {
			t.Fatalf("member %s: %v", name, err)
		}
		return v
	}
	call := func(name string, args ...runtime.Value) runtime.Value {
		t.Helper()
		v, err := vmm.Call(member(name), args)
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		return runtime.Unwrap(v)
	}

	g := call("MakeG")
	got := []any{call("IsHasM", g)}

	gtd := member("G").(*runtime.TypeDef)
	htd := member("H").(*runtime.TypeDef)
	if gtd.Methods == nil {
		gtd.Methods = map[string]*runtime.Function{}
	}
	gtd.Methods["M"] = htd.Methods["M"]
	runtime.MethodSetsChanged()
	got = append(got, call("IsHasM", g))

	if diff := cmp.Diff([]any{false, true}, got); diff != "" {
		t.Fatalf("IsHasM before/after graft (-want +got):\n%s", diff)
	}
}
