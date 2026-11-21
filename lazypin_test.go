package minigo

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// pkgState reports a built package's loading stage for per-path
// verification; ok=false means the run never built it.
func pkgState(e *Engine, path string) (runtime.State, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pkgs[path]
	if !ok {
		return runtime.Unseen, false
	}
	return p.State(), true
}

// TestLazyInitPin pins the current per-path laziness (M0-4):
//
//   - host-side queries (Engine.Package, index lookups) index but never
//     initialize — a panicking init stays inert;
//   - an imported package on an untaken branch is indexed but never
//     initialized;
//   - a member touch DOES initialize today, even when the member is a
//     type (`var t depb.T`) — that eager init is the gap the
//     rearchitecture's IndexedOnly/AllowIndex queries close later, so
//     it is pinned as the current behavior rather than as the goal.
func TestLazyInitPin(t *testing.T) {
	const (
		depa = "github.com/podhmo/minigo/testdata/lazypin/depa"
		depb = "github.com/podhmo/minigo/testdata/lazypin/depb"
		depc = "github.com/podhmo/minigo/testdata/lazypin/depc"
	)
	ctx := context.Background()
	stub := func(p *runtime.Package, d *index.Decl) (runtime.Value, error) {
		return runtime.NIL, nil
	}

	t.Run("HostTypeQueryNeverInitializes", func(t *testing.T) {
		e := NewEngine(".")
		pkg, err := e.Package(ctx, "./testdata/lazypin/depb")
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		if st := pkg.State(); st != runtime.Indexed {
			t.Fatalf("depb state after Package = %v, want Indexed", st)
		}
		// index-level type query: depb.T is visible without init.
		var found bool
		for _, d := range pkg.Index.Decls {
			if d.Name == "T" {
				found = true
			}
		}
		if !found {
			t.Fatal("depb.T not in index")
		}
		if st := pkg.State(); st != runtime.Indexed {
			t.Errorf("depb state after index query = %v, want Indexed", st)
		}
	})

	t.Run("MemberTouchInitializes", func(t *testing.T) {
		e := NewEngine(".")
		pkg, err := e.Package(ctx, "./testdata/lazypin/depb")
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		// current behavior: even a type member touch runs the init.
		if _, err := pkg.Member("T", stub); err == nil || !strings.Contains(err.Error(), "BOOM") {
			t.Fatalf("Member(T): expected init BOOM panic, got %v", err)
		}
		if st := pkg.State(); st != runtime.Failed {
			t.Errorf("depb state after panicking init = %v, want Failed", st)
		}
	})

	t.Run("PerPathStatesAfterRun", func(t *testing.T) {
		e := NewEngine(".")
		// BranchNotTaken(false) calls depa.Next, leaves depb's type
		// declared in TypeQueryOnly alone, and never reaches depc.Get.
		if _, err := e.Run(ctx, "./testdata/lazypin/main", "UseOnlyA"); err != nil {
			t.Fatalf("Run(UseOnlyA): %v", err)
		}
		states := map[string]runtime.State{}
		for _, path := range []string{depa, depb, depc} {
			st, _ := pkgState(e, path)
			states[path] = st
		}
		want := map[string]runtime.State{
			depa: runtime.Ready,
			depb: runtime.Indexed,
			depc: runtime.Indexed,
		}
		if diff := cmp.Diff(want, states); diff != "" {
			t.Errorf("package states mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("UntakenBranchStaysUninitialized", func(t *testing.T) {
		e := NewEngine(".")
		if _, err := e.Run(ctx, "./testdata/lazypin/main", "BranchNotTaken", false); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if st, _ := pkgState(e, depc); st != runtime.Indexed {
			t.Errorf("depc state = %v, want Indexed (indexed but never initialized)", st)
		}
		if st, _ := pkgState(e, depa); st != runtime.Ready {
			t.Errorf("depa state = %v, want Ready", st)
		}
	})

	t.Run("ScriptTypeMemberTouchInitializes", func(t *testing.T) {
		// pins the current eager init on a type-only member touch in
		// script code: `var t depb.T` runs depb's initializer.
		e := NewEngine(".")
		_, err := e.Run(ctx, "./testdata/lazypin/main", "TypeQueryOnly")
		if err == nil || !strings.Contains(err.Error(), "BOOM") {
			t.Fatalf("TypeQueryOnly: expected init BOOM panic, got %v", err)
		}
		if st, _ := pkgState(e, depb); st != runtime.Failed {
			t.Errorf("depb state = %v, want Failed", st)
		}
	})
}
