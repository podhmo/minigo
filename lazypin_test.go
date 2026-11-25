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
// verification; every path queried here is expected to have been built.
func pkgState(t *testing.T, e *Engine, path string) runtime.State {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pkgs[path]
	if !ok {
		t.Fatalf("package %s was not built", path)
	}
	return p.State()
}

// TestLazyInitPin pins the per-path laziness (M0-4 / M4-8):
//
//   - host-side queries (Engine.Package, index lookups) index but never
//     initialize — a panicking init stays inert;
//   - an imported package on an untaken branch is indexed but never
//     initialized;
//   - a TYPE member touch only materializes the declaration — it never
//     initializes (`var t depb.T`, `pkg.Member("T")`);
//   - a VALUE member touch (var/const read, func touch) initializes the
//     package fully, so a panicking init surfaces as BOOM.
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

	t.Run("TypeMemberTouchStaysLazy", func(t *testing.T) {
		e := NewEngine(".")
		pkg, err := e.Package(ctx, "./testdata/lazypin/depb")
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		// a type member touch materializes the decl without the init.
		if _, err := pkg.Member("T", stub); err != nil {
			t.Fatalf("Member(T): %v", err)
		}
		if st := pkg.State(); st != runtime.Indexed {
			t.Errorf("depb state after type member touch = %v, want Indexed", st)
		}
	})

	t.Run("ValueMemberTouchInitializes", func(t *testing.T) {
		e := NewEngine(".")
		pkg, err := e.Package(ctx, "./testdata/lazypin/depb")
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		// a var member touch runs the package initializer first.
		if _, err := pkg.Member("Touched", stub); err == nil || !strings.Contains(err.Error(), "BOOM") {
			t.Fatalf("Member(Touched): expected init BOOM panic, got %v", err)
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
			states[path] = pkgState(t, e, path)
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
		if st := pkgState(t, e, depc); st != runtime.Indexed {
			t.Errorf("depc state = %v, want Indexed (indexed but never initialized)", st)
		}
		if st := pkgState(t, e, depa); st != runtime.Ready {
			t.Errorf("depa state = %v, want Ready", st)
		}
	})

	t.Run("ScriptTypeMemberTouchStaysLazy", func(t *testing.T) {
		// a type-only member touch in script code — `var t depb.T` —
		// materializes the decl and leaves depb's initializer unrun,
		// under both the normal and the strict-lazy compile.
		for name, opts := range map[string][]Option{
			"normal": nil,
			"strict": {WithStrictLazyCompile()},
		} {
			e := NewEngine(".", opts...)
			if _, err := e.Run(ctx, "./testdata/lazypin/main", "TypeQueryOnly"); err != nil {
				t.Fatalf("%s TypeQueryOnly: %v", name, err)
			}
			if st := pkgState(t, e, depb); st != runtime.Indexed {
				t.Errorf("%s: depb state = %v, want Indexed", name, st)
			}
		}
	})

	t.Run("ScriptMethodTouchInitializes", func(t *testing.T) {
		// calling a method declared in a package is a value touch:
		// the method body may read package vars, so depb's initializer
		// runs first — under both compiles.
		for name, opts := range map[string][]Option{
			"normal": nil,
			"strict": {WithStrictLazyCompile()},
		} {
			e := NewEngine(".", opts...)
			_, err := e.Run(ctx, "./testdata/lazypin/main", "MethodTouch")
			if err == nil || !strings.Contains(err.Error(), "BOOM") {
				t.Fatalf("%s MethodTouch: expected init BOOM panic, got %v", name, err)
			}
			if st := pkgState(t, e, depb); st != runtime.Failed {
				t.Errorf("%s: depb state = %v, want Failed", name, st)
			}
		}
	})

	t.Run("ScriptValueMemberTouchInitializes", func(t *testing.T) {
		// reading a var member in script code initializes the package.
		for name, opts := range map[string][]Option{
			"normal": nil,
			"strict": {WithStrictLazyCompile()},
		} {
			e := NewEngine(".", opts...)
			_, err := e.Run(ctx, "./testdata/lazypin/main", "ValueMemberTouch")
			if err == nil || !strings.Contains(err.Error(), "BOOM") {
				t.Fatalf("%s ValueMemberTouch: expected init BOOM panic, got %v", name, err)
			}
			if st := pkgState(t, e, depb); st != runtime.Failed {
				t.Errorf("%s: depb state = %v, want Failed", name, st)
			}
		}
	})
}
