package minigo

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

// writeMultiFilePkg writes a package of nFiles .go files, each holding
// nFuncs small functions plus a type and a var, so the parse+index+init
// pipeline sees a realistic amount of syntax per load.
func writeMultiFilePkg(tb testing.TB, dir string, nFiles, nFuncs int) {
	tb.Helper()
	for f := range nFiles {
		w := []byte("package forkpkg\n\n")
		w = fmt.Appendf(w, "type T%d struct { A int; B string }\n", f)
		w = fmt.Appendf(w, "var G%d = %d\n\n", f, f)
		for i := range nFuncs {
			w = fmt.Appendf(w, `func F%d_%d(x int) int {
	y := x + %d
	if y > 100 {
		y = y - 100
	}
	m := map[string]int{"a": y, "b": x}
	s := 0
	for _, v := range m {
		s += v
	}
	return s + y
}

`, f, i, i)
		}
		name := filepath.Join(dir, fmt.Sprintf("file%02d.go", f))
		if err := os.WriteFile(name, w, 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	// an entry point for Call
	main := "package forkpkg\n\nfunc main() {}\nfunc Entry() int { return F0_0(1) }\n"
	if err := os.WriteFile(filepath.Join(dir, "entry.go"), []byte(main), 0o644); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkSessionForkReload measures the storybook preview pattern:
// a base engine keeps its binds warm while every request forks a
// session and re-loads the same source package from scratch.
func BenchmarkSessionForkReload(b *testing.B) {
	dir := b.TempDir()
	writeMultiFilePkg(b, dir, 12, 20)
	base := NewEngine(dir)
	ctx := context.Background()

	// warm the resolver's directory scan once so the numbers reflect
	// parse+index+init, not first-touch file listing
	if _, err := base.Package(ctx, dir); err != nil {
		b.Fatal(err)
	}

	b.Run("PackageOnly", func(b *testing.B) {
		for b.Loop() {
			s := base.NewSession()
			if _, err := s.Package(ctx, dir); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PackageAndCall", func(b *testing.B) {
		for b.Loop() {
			s := base.NewSession()
			p, err := s.Package(ctx, dir)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := s.Call(ctx, p, "Entry"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkReloadStages splits the per-fork reload into its pipeline
// stages: resolver locate, syntax parse, index build, and the init
// chunk compile that a first call triggers.
func BenchmarkReloadStages(b *testing.B) {
	dir := b.TempDir()
	writeMultiFilePkg(b, dir, 12, 20)
	base := NewEngine(dir)
	ctx := context.Background()

	meta, err := base.resolver.LocateDir(ctx, dir)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("LocateDir", func(b *testing.B) {
		for b.Loop() {
			if _, err := base.resolver.LocateDir(ctx, dir); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Parse", func(b *testing.B) {
		for b.Loop() {
			fset := token.NewFileSet()
			if _, _, err := parsePackageFiles(fset, meta.GoFiles); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Index", func(b *testing.B) {
		fset := token.NewFileSet()
		files, _, err := parsePackageFiles(fset, meta.GoFiles)
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for b.Loop() {
			s := base.NewSession()
			p := s.newPackage(meta.ImportPath, meta.Name, meta.Dir)
			if err := s.indexFiles(p, files, false); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Session", func(b *testing.B) {
		for b.Loop() {
			_ = base.NewSession()
		}
	})
}

// TestSessionForkSeesEdits verifies the parse cache's invalidation
// contract: two sessions forked from one base share an unchanged
// file's *syntax.File, and a file rewritten on disk is re-parsed so the
// next session's call observes the new code — never the old.
func TestSessionForkSeesEdits(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "v.go")
	write := func(body string) {
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package p\nfunc V() int { return 1 }\n")

	base := NewEngine(dir)
	ctx := context.Background()

	load := func(s *Engine) *runtime.Package {
		t.Helper()
		p, err := s.Package(ctx, dir)
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		return p
	}

	s1 := base.NewSession()
	p1 := load(s1)
	s2 := base.NewSession()
	p2 := load(s2)
	if p1.Files[0] != p2.Files[0] {
		t.Fatal("unchanged file must share its *syntax.File across sessions")
	}
	if v, err := s2.Call(ctx, p2, "V"); err != nil || v != int64(1) {
		t.Fatalf("Call before edit: %v, %v", v, err)
	}

	// an edit invalidates by content hash: the new session re-parses
	// the changed file and runs the new code.
	write("package p\nfunc V() int { return 2 }\n")
	s3 := base.NewSession()
	p3 := load(s3)
	if p3.Files[0] == p1.Files[0] {
		t.Fatal("edited file must be re-parsed, not served from cache")
	}
	if v, err := s3.Call(ctx, p3, "V"); err != nil || v != int64(2) {
		t.Fatalf("Call after edit: %v, %v, want 2", v, err)
	}
}

// TestSessionForkConcurrentLoads hammers the shared cache: sessions
// forked on many goroutines load the same package at once — the race
// detector owns the verdict.
func TestSessionForkConcurrentLoads(t *testing.T) {
	dir := t.TempDir()
	writeMultiFilePkg(t, dir, 6, 10)
	base := NewEngine(dir)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				s := base.NewSession()
				p, err := s.Package(ctx, dir)
				if err != nil {
					t.Errorf("Package: %v", err)
					return
				}
				if _, err := s.Call(ctx, p, "Entry"); err != nil {
					t.Errorf("Call: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
