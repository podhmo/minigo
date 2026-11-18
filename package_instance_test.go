package minigo

// M5-2a: a package's execution state — its globals and its init state —
// belongs to the session that built it, not to the shared source.
// Two sessions over the same module see independent globals, and each
// runs the package's init exactly once.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func TestPackageInstanceSessionsIndependent(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/m/dep"

// next-next-inits packed into one int: dep.Next() bumps a package
// global; dep.Inits counts how many times dep's init ran here.
func Report() int64 {
	return int64(dep.Next())*1000000 + int64(dep.Next())*1000 + int64(dep.Inits)
}
`,
		"dep/dep.go": `package dep

var n = 10
var Inits int

func init() { Inits++ }

func Next() int { n++; return n }
`,
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	e := NewEngine(dir)
	ctx := context.Background()

	// both sessions run dep's init once and see the SAME sequence from a
	// fresh counter: 11*1000000 + 12*1000 + 1 — neither observes the
	// other's globals
	want := int64(11012001)
	sa := e.NewSession()
	gotA, err := sa.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatalf("session A Report: %v", err)
	}
	if diff := cmp.Diff(want, gotA); diff != "" {
		t.Fatalf("session A Report (-want +got):\n%s", diff)
	}
	sb := e.NewSession()
	gotB, err := sb.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatalf("session B Report: %v", err)
	}
	if diff := cmp.Diff(want, gotB); diff != "" {
		t.Fatalf("session B Report (-want +got):\n%s", diff)
	}

	// the two sessions hold distinct package objects — and thus distinct
	// PackageInstances — over the shared source
	pa, errA := sa.loadPath(ctx, "example.com/m/dep")
	pb, errB := sb.loadPath(ctx, "example.com/m/dep")
	if errA != nil || errB != nil {
		t.Fatalf("load dep: %v / %v", errA, errB)
	}
	if diff := cmp.Diff(pa == pb, false); diff != "" {
		t.Fatalf("sessions share the package object (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&pa.PackageInstance == &pb.PackageInstance, false); diff != "" {
		t.Fatalf("sessions share the package instance (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(pa.Globals == pb.Globals, false); diff != "" {
		t.Fatalf("sessions share globals (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(pa.State(), runtime.Ready); diff != "" {
		t.Fatalf("session A dep not Ready (-want +got):\n%s", diff)
	}
}
