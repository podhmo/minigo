package minigo_test

import (
	"context"
	goruntime "runtime"
	"testing"
	"time"
	"weak"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// loadTemplateSession runs one template-rendering session and drops both
// the base engine and the forked session before returning.
//
//go:noinline
func loadTemplateSession(t *testing.T) weak.Pointer[runtime.Package] {
	t.Helper()
	base := minigo.NewEngine(".")
	s := base.NewSession()
	pkg, err := s.Package(context.Background(), "./testdata/sessionrelease")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Call(context.Background(), pkg, "F")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.(string)
	if !ok {
		t.Fatalf("F = %#v, want string", v)
	}
	if diff := cmp.Diff("ok!", got); diff != "" {
		t.Fatalf("F (-want +got):\n%s", diff)
	}
	return weak.Make(pkg)
}

// A template render marshals script structs and typed containers across
// the host boundary. Caches that remember those host projections must not
// keep the whole forked session alive after the caller drops it.
func TestTemplateSessionReleasesPackage(t *testing.T) {
	wp := loadTemplateSession(t)
	deadline := time.Now().Add(10 * time.Second)
	for wp.Value() != nil {
		if time.Now().After(deadline) {
			t.Fatal("template session package remained reachable after the session was dropped")
		}
		goruntime.GC()
		time.Sleep(5 * time.Millisecond) // cleanups run asynchronously
	}
}
