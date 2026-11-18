package minigo

// M5-3a: source-version ownership at the engine level. A changed file
// means a new parse/index version — sessions already holding the old
// version keep it — and a changed build configuration selects a
// different file set, which is itself a new version.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

func writeModule(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceVersionContentEdit(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/m/dep"

func Report() int64 { return int64(dep.V()) }
`,
		"dep/dep.go": "package dep\n\nfunc V() int { return 1 }\n",
	})

	e := NewEngine(dir)
	ctx := context.Background()

	sa := e.NewSession()
	v1, err := sa.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(1), v1); diff != "" {
		t.Fatalf("v1 (-want +got):\n%s", diff)
	}

	// edit the dep's source: a fresh session parses and indexes the new
	// version, the session that already loaded it keeps the old
	if err := os.WriteFile(filepath.Join(dir, "dep", "dep.go"),
		[]byte("package dep\n\nfunc V() int { return 2 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sb := e.NewSession()
	v2, err := sb.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(2), v2); diff != "" {
		t.Fatalf("v2 (-want +got):\n%s", diff)
	}

	v1b, err := sa.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(1), v1b); diff != "" {
		t.Fatalf("old session must keep the old version (-want +got):\n%s", diff)
	}
}

func TestSourceVersionBuildConfig(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/m/dep"

func Report() int64 { return int64(dep.V()) }
`,
		"dep/plain.go": "//go:build !feat\n\npackage dep\n\nfunc V() int { return 1 }\n",
		"dep/feat.go":  "//go:build feat\n\npackage dep\n\nfunc V() int { return 2 }\n",
	})

	ctx := context.Background()

	plain := NewEngine(dir, WithBuildConfig(resolve.BuildConfig{}))
	feat := NewEngine(dir, WithBuildConfig(resolve.BuildConfig{Tags: []string{"feat"}}))

	v1, err := plain.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := feat.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(1), v1); diff != "" {
		t.Fatalf("default build config (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(int64(2), v2); diff != "" {
		t.Fatalf("feat-tagged build config must select the tagged file set (-want +got):\n%s", diff)
	}
}

// M5-4a: an array length spelled by a const in another file folds per
// version — the file declaring it refolds per load, so a session's
// fold rides on ITS parse of the declaration and a session that loaded
// the package before the const edit keeps the old length.
func TestSourceVersionArrayLenFold(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"main.go": `package main

import "example.com/m/dep"

func Report() int64 { return int64(dep.Len()) }
`,
		"dep/const.go": "package dep\n\nconst N = 3\n",
		"dep/dep.go": `package dep

type A [N]int

func Len() int { var a A; return len(a) }
`,
	})

	e := NewEngine(dir)
	ctx := context.Background()

	sa := e.NewSession()
	v1, err := sa.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(3), v1); diff != "" {
		t.Fatalf("v1 (-want +got):\n%s", diff)
	}

	if err := os.WriteFile(filepath.Join(dir, "dep", "const.go"),
		[]byte("package dep\n\nconst N = 5\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sb := e.NewSession()
	v2, err := sb.Run(ctx, dir, "Report")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(5), v2); diff != "" {
		t.Fatalf("v2 (-want +got):\n%s", diff)
	}

	// concurrent references: each session keeps reporting its own
	// version — the folds live on per-version AST nodes, so neither
	// session's answer leaks into the other's.
	var wg sync.WaitGroup
	got := make(chan runtime.Value, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(s *Engine) {
			defer wg.Done()
			for j := 0; j < 2; j++ {
				v, err := s.Run(ctx, dir, "Report")
				if err != nil {
					got <- err
					return
				}
				got <- v
			}
		}([]*Engine{sa, sb}[i%2])
	}
	wg.Wait()
	close(got)
	var old, nu int
	for v := range got {
		if err, isErr := v.(error); isErr {
			t.Fatal(err)
		}
		switch v {
		case int64(3):
			old++
		case int64(5):
			nu++
		default:
			t.Fatalf("unexpected value: %v", v)
		}
	}
	if diff := cmp.Diff(4, old); diff != "" {
		t.Errorf("old-version answers (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(4, nu); diff != "" {
		t.Errorf("new-version answers (-want +got):\n%s", diff)
	}
}
