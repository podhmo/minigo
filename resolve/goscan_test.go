package resolve_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/podhmo/minigo/pkg/locator"
	"github.com/podhmo/minigo/resolve"
)

// A cyclic replace graph is "found but broken", not a miss: Locate
// surfaces the configuration error without the ErrUnresolved mark, so
// the Lookup* soft loaders propagate it instead of answering nil.
func TestLocateBrokenReplaceGraph(t *testing.T) {
	dir := t.TempDir()
	gomod := `module cycmod

go 1.24

require example.com/a v0.0.0
require example.com/b v0.0.0

replace example.com/a => example.com/b v0.0.0
replace example.com/b => example.com/a v0.0.0
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := resolve.NewGoScanResolver(dir, resolve.BuildConfig{})
	if err != nil {
		t.Fatalf("NewGoScanResolver: %v", err)
	}
	_, err = r.Locate(context.Background(), "", "example.com/a")
	if err == nil {
		t.Fatal("Locate: want error for the cyclic replace graph, got nil")
	}
	if errors.Is(err, resolve.ErrUnresolved) {
		t.Errorf("Locate: a broken graph must not carry ErrUnresolved: %v", err)
	}
	if !errors.Is(err, locator.ErrBrokenConfig) {
		t.Errorf("Locate: want ErrBrokenConfig in the chain, got %v", err)
	}

	// control: a plain miss still carries ErrUnresolved.
	if _, err := r.Locate(context.Background(), "", "example.com/nothing"); !errors.Is(err, resolve.ErrUnresolved) {
		t.Errorf("Locate(miss): want ErrUnresolved, got %v", err)
	}
}
