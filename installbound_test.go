package minigo

// P2-12: bound packages build on first use, not at engine construction.

import (
	"context"
	"sync"
	"testing"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// TestBoundInstallIsLazy pins that a registered stdlib path stays
// uninstalled until a lookup touches it: the names registry reports the
// path as bound from construction, but e.pkgs only gains the package
// after BoundOf (the first use) runs its installer.
func TestBoundInstallIsLazy(t *testing.T) {
	e := NewEngine(".")
	if !e.registered("time") {
		t.Fatal("time is not registered")
	}
	if e.pkgs["time"] != nil {
		t.Fatal("time was installed eagerly")
	}
	if p := e.BoundOf("time"); p == nil {
		t.Fatal("BoundOf(time) after lazy install = nil")
	}
	if e.pkgs["time"] == nil {
		t.Fatal("time missing from e.pkgs after BoundOf")
	}
}

// TestBoundInstallViaHostEntry pins the hostTwin route: the typedef
// lookup entry installs the registered path on first use even when the
// script never imported it.
func TestBoundInstallViaHostEntry(t *testing.T) {
	e := NewEngine(".")
	if e.pkgs["time"] != nil {
		t.Fatal("time was installed eagerly")
	}
	p := e.hostPackageOf("time")
	if p == nil {
		t.Fatal("hostPackageOf(time) = nil after install")
	}
	if _, ok := p.Globals.Get("Time"); !ok {
		t.Fatal("time bound table is empty")
	}
}

// TestBoundPackagesListsPending pins that BoundPackages reports a
// registered path even while its table is still unbuilt.
func TestBoundPackagesListsPending(t *testing.T) {
	e := NewEngine(".")
	found := false
	for _, p := range e.BoundPackages() {
		if p == "time" {
			found = true
		}
	}
	if !found {
		t.Fatal("BoundPackages missing registered (pending) path time")
	}
}

// TestExplicitBindWinsOverLazy pins that an explicit Bind of a
// registered path survives the lazy installer — including when a
// sibling alias of the same package installs first.
func TestExplicitBindWinsOverLazy(t *testing.T) {
	e := NewEngine(".")
	e.Bind("minigo.dev/host", map[string]runtime.Value{"Mine": int64(1)})
	if p := e.BoundOf("github.com/podhmo/minigo/host"); p == nil {
		t.Fatal("sibling alias did not install")
	}
	mine := e.BoundOf("minigo.dev/host")
	if _, ok := mine.Globals.Get("Mine"); !ok {
		t.Fatal("explicit bind clobbered by the sibling alias's install")
	}
}

// TestBoundQueryKeepsSourceCache pins that a bound query still reports
// the bound table while a source-built package keeps answering loads
// from e.pkgs (a ModeSource load keeps its globals' state).
func TestBoundQueryKeepsSourceCache(t *testing.T) {
	e := NewEngine(".")
	src := &runtime.Package{Path: "time", Name: "time", Index: &index.Index{}}
	e.mu.Lock()
	e.pkgs["time"] = src
	e.mu.Unlock()
	if p := e.BoundOf("time"); p == nil {
		t.Fatal("BoundOf lost the bound table")
	}
	e.mu.Lock()
	got := e.pkgs["time"]
	e.mu.Unlock()
	if got != src {
		t.Fatal("source-built package clobbered by the bound install")
	}
}

// TestDirRefInstallsBound pins that a directory ref resolving to a
// bound module path answers the bound package, not the source stub.
func TestDirRefInstallsBound(t *testing.T) {
	e := NewEngine(".")
	p, err := e.Package(context.Background(), "./host")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Globals.Get("Args"); !ok {
		t.Fatal("./host did not answer the bound host package")
	}
}

// TestConcurrentSourceAndBoundQuery exercises the registered/bound
// reads against concurrent installs; run under -race to flag map races.
func TestConcurrentSourceAndBoundQuery(t *testing.T) {
	e := NewEngine(".")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = e.SourceOf(context.Background(), "strings")
			} else {
				_, _ = e.Package(context.Background(), "strings")
			}
		}(i)
	}
	wg.Wait()
}
