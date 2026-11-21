package minigo

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/podhmo/minigo/internal/source"
	"github.com/podhmo/minigo/resolve"
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
			if _, _, err := source.ParsePackageFiles(fset, meta.GoFiles); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Index", func(b *testing.B) {
		fset := token.NewFileSet()
		files, _, err := source.ParsePackageFiles(fset, meta.GoFiles)
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

// TestSessionForkArrayLenFold covers the runtime's process-global
// array-length fold table: it is keyed by the *ast.ArrayType node, so
// a `[N]int` node shared across sessions would keep the length folded
// under the first session's const values even after the const's file
// is edited. Files containing such types skip the shared parse cache
// entirely, and this test proves the new session folds anew.
func TestSessionForkArrayLenFold(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("const.go", "package p\nconst N = 3\n")
	write("arr.go", "package p\nvar Arr [N]int\nfunc Size() int { return len(Arr) }\n")

	base := NewEngine(dir)
	ctx := context.Background()

	size := func(s *Engine) int64 {
		t.Helper()
		p, err := s.Package(ctx, dir)
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		v, err := s.Call(ctx, p, "Size")
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		return v.(int64)
	}

	if got := size(base.NewSession()); got != 3 {
		t.Fatalf("first load: Size = %d, want 3", got)
	}
	write("const.go", "package p\nconst N = 5\n")
	if got := size(base.NewSession()); got != 5 {
		t.Fatalf("after editing the const file: Size = %d, want 5 (stale array-length fold)", got)
	}

	// the refold path re-parses at the registered Pos range — the
	// shared FileSet must not grow on repeated loads.
	fcount := func() int {
		n := 0
		base.fset.Iterate(func(*token.File) bool { n++; return true })
		return n
	}
	count := fcount()
	for range 3 {
		if got := size(base.NewSession()); got != 5 {
			t.Fatalf("reload: Size = %d, want 5", got)
		}
	}
	if got := fcount(); got != count {
		t.Fatalf("FileSet grew on refold loads: %d -> %d", count, got)
	}
}

// TestSessionForkLangChange covers the go.mod `go` directive: a cached
// *syntax.File already carries the language version it was parsed with,
// so a changed directive must re-parse instead of reusing the stale
// LangMod for CheckLang.
func TestSessionForkLangChange(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module forkpkg\n\ngo 1.20\n")
	write("v.go", "package forkpkg\nfunc V() int { return 1 }\n")

	base := NewEngine(dir)
	ctx := context.Background()

	lang := func(s *Engine) string {
		t.Helper()
		p, err := s.Package(ctx, dir)
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		return p.Files[0].LangMod
	}

	first := lang(base.NewSession())
	write("go.mod", "module forkpkg\n\ngo 1.22\n")
	got := lang(base.NewSession())
	want := resolve.ModuleLang(dir)
	if got != want || got == first {
		t.Fatalf("after go.mod change: LangMod = %q, want %q (was %q)", got, want, first)
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

// BenchmarkInstallerFirstUse separates the lazy-installer cost the
// P2-12c move left on first use: every fresh session carries its own
// pending registry, so a new session's first import of a target path
// runs that path's Install (cold), while a repeat in the same session
// hits the installed bound package (warm). Measured per moved
// installer — stdlib lazy docs compare cold-warm-NewSession to
// attribute the delay.
func BenchmarkInstallerFirstUse(b *testing.B) {
	scripts := map[string]string{
		"html":          "package main\n\nimport \"html\"\n\nvar out string\n\nfunc main() { out = html.EscapeString(\"<a>\") }\n",
		"unicode/utf8":  "package main\n\nimport \"unicode/utf8\"\n\nvar n int\n\nfunc main() { n = utf8.RuneCountInString(\"日本語\") }\n",
		"time":          "package main\n\nimport \"time\"\n\nvar d time.Duration\n\nfunc main() { d = time.Second }\n",
		"context":       "package main\n\nimport \"context\"\n\nvar c context.Context\n\nfunc main() { c = context.Background() }\n",
		"text/template": "package main\n\nimport (\n\t\"strings\"\n\t\"text/template\"\n)\n\nvar s string\n\nfunc main() {\n\tt, _ := template.New(\"t\").Parse(\"{{.}}!\")\n\tvar w strings.Builder\n\t_ = t.Execute(&w, \"x\")\n\ts = w.String()\n}\n",
		"html/template": "package main\n\nimport \"html/template\"\n\nvar s string\n\nfunc main() { s = template.HTMLEscapeString(\"<a>\") }\n",
	}
	for name, src := range scripts {
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
				b.Fatal(err)
			}
			base := NewEngine(".")
			ctx := context.Background()
			b.Run("cold", func(b *testing.B) {
				for b.Loop() {
					s := base.NewSession()
					if _, err := s.Run(ctx, dir, "main"); err != nil {
						b.Fatal(err)
					}
				}
			})
			s := base.NewSession()
			if _, err := s.Run(ctx, dir, "main"); err != nil {
				b.Fatal(err)
			}
			b.Run("warm", func(b *testing.B) {
				for b.Loop() {
					if _, err := s.Call(ctx, mustPkg(b, s, ctx, dir), "main"); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func mustPkg(b *testing.B, s *Engine, ctx context.Context, dir string) *runtime.Package {
	b.Helper()
	p, err := s.Package(ctx, dir)
	if err != nil {
		b.Fatal(err)
	}
	return p
}
