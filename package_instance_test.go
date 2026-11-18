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
	"github.com/podhmo/minigo/index"
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

// M5-2b: the runtime import refs and the materialize caches live on the
// instance — an import's first reference loads through its OWN session
// (never the sibling's package), a member built concurrently lands in
// one shared slot, and another session's package cannot be called here.
func TestPackageInstanceOwnership(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/m/dep"

func Report() int64 { return int64(dep.Next()) }
`,
		"dep/dep.go": `package dep

var n = 10

func Next() int { n++; return n }

// never referenced by main — its MatCache slot stays empty
func Unused() int { return -1 }
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
	sa, sb := e.NewSession(), e.NewSession()
	for _, s := range []*Engine{sa, sb} {
		if _, err := s.Run(ctx, dir, "Report"); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}
	pa, _ := sa.loadPath(ctx, "example.com/m/dep")
	pb, _ := sb.loadPath(ctx, "example.com/m/dep")

	// a session's own import ref materializes to ITS dep package —
	// the ref's Load runs through the owning session's caches, so
	// session A's ref cannot reach session B's package
	ma, mb := sa.pkgs["example.com/m"], sb.pkgs["example.com/m"]
	if diff := cmp.Diff(len(ma.Files) > 0 && len(mb.Files) > 0, true); diff != "" {
		t.Fatalf("main package files (-want +got):\n%s", diff)
	}
	depA, err := ma.Scopes[ma.Files[0]]["dep"].Materialize()
	if err != nil {
		t.Fatalf("session A import ref: %v", err)
	}
	depB, err := mb.Scopes[mb.Files[0]]["dep"].Materialize()
	if err != nil {
		t.Fatalf("session B import ref: %v", err)
	}
	if diff := cmp.Diff(depA == pa && depB == pb, true); diff != "" {
		t.Fatalf("import refs resolve outside their session (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(depA == depB, false); diff != "" {
		t.Fatalf("import refs of two sessions alias (-want +got):\n%s", diff)
	}

	// another session's package is not callable here — the Owner guard
	// keeps one instance's state from leaking into a sibling
	if _, err := sb.Call(ctx, pa, "Next"); err == nil {
		t.Fatal("session B called session A's dep.Next without an error")
	}

	// concurrent first references land in one cache slot: every caller
	// gets the same materialized value
	var d *index.Decl
	for _, decl := range pa.Index.Decls {
		if decl.Name == "Unused" {
			d = decl
		}
	}
	if d == nil {
		t.Fatal("dep has no Unused decl")
	}
	sentinel := &runtime.GoValue{V: 42}
	got := make(chan runtime.Value, 8)
	for i := 0; i < 8; i++ {
		go func() {
			v, err := pa.MatCache(d, func(*runtime.Package, *index.Decl) (runtime.Value, error) {
				return sentinel, nil
			})
			if err != nil {
				t.Errorf("MatCache: %v", err)
				return
			}
			got <- v
		}()
	}
	for i := 0; i < 8; i++ {
		if diff := cmp.Diff(<-got == sentinel, true); diff != "" {
			t.Fatalf("MatCache gave a different value (-want +got):\n%s", diff)
		}
	}
}
