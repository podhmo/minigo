package minigo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelfBoundEngineNewSession pins the embedding API inside a script:
// importing github.com/podhmo/minigo must use the host embedding API
// rather than recursively interpreting the engine implementation. The
// NewSession call exercises the construction invariants missing in the
// storybook regression.
func TestSelfBoundEngineNewSession(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedSession")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("NestedSession() = %v (%T), want ok", got, got)
	}
}

func TestSelfBoundEngineInheritsHostPolicy(t *testing.T) {
	e := NewEngine(".", WithHostPolicy(func(path, sym string) bool {
		return !(path == "os" && sym == "Getenv")
	}))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedHostPolicy")
	if err != nil {
		t.Fatal(err)
	}
	if got != "denied" {
		t.Fatalf("NestedHostPolicy() = %v (%T), want denied", got, got)
	}
}

func TestSelfBoundEngineDeniedUnderAllowedRoots(t *testing.T) {
	root, err := filepath.Abs("./testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(".", WithAllowedRoots(root))
	p := e.BoundOf("github.com/podhmo/minigo")
	if p == nil {
		t.Fatal("restricted self-import must still be a bound package")
	}
	if _, ok := p.Globals.Get("NewEngine"); ok {
		t.Fatal("NewEngine must be absent under AllowedRoots")
	}
	_, err = e.Run(context.Background(), "./testdata/selfbind", "NestedSession")
	if err == nil || !strings.Contains(err.Error(), "NewEngine") {
		t.Fatalf("minigo.NewEngine must be unbound under AllowedRoots, got %v", err)
	}
}
