package minigo

// P2-12: bound packages build on first use, not at engine construction.

import (
	"testing"
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
