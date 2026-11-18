package minigo

// M5-1b: both load paths assemble the same PackageImage — declarations,
// the Lang/GoFiles build configuration, and the still-lazy init state
// agree between the strict package cache and inspect's lenient view.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func imageDeclNames(p *runtime.Package) []string {
	var names []string
	for _, d := range p.Index.Decls {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

func TestPackageImageBothLoadPaths(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"p/p.go": `package p

var V int

func F() int { return V }

type S struct{ A int }
`,
		// excluded by its build constraint — GoFiles is the selected set
		"p/skip.go": "//go:build never_this_tag\n\npackage p\n",
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
	pkgdir := filepath.Join(dir, "p")

	sp, err := e.loadDir(context.Background(), pkgdir)
	if err != nil {
		t.Fatalf("strict load: %v", err)
	}
	lp, err := e.loadDirLenient(context.Background(), pkgdir)
	if err != nil {
		t.Fatalf("lenient load: %v", err)
	}

	if diff := cmp.Diff(sp == lp, false); diff != "" {
		t.Errorf("strict and lenient caches returned the same package object")
	}

	// declarations, build config and positions agree across both paths
	if diff := cmp.Diff(imageDeclNames(sp), imageDeclNames(lp)); diff != "" {
		t.Errorf("decl names differ (-strict +lenient):\n%s", diff)
	}
	if diff := cmp.Diff(sp.GoFiles, lp.GoFiles); diff != "" {
		t.Errorf("GoFiles differ (-strict +lenient):\n%s", diff)
	}
	if diff := cmp.Diff(len(sp.GoFiles), 1); diff != "" {
		t.Errorf("build-constraint selection missing from image (-got +want):\n%s", diff)
	}
	if diff := cmp.Diff(filepath.Base(sp.GoFiles[0]), "p.go"); diff != "" {
		t.Errorf("GoFiles (-got +want):\n%s", diff)
	}
	for name, p := range map[string]*runtime.Package{"strict": sp, "lenient": lp} {
		if diff := cmp.Diff(p.Lang, "1.24"); diff != "" {
			t.Errorf("%s: Lang (-got +want):\n%s", name, diff)
		}
		if diff := cmp.Diff(p.Fset == e.fset, true); diff != "" {
			t.Errorf("%s: image does not carry the engine's FileSet", name)
		}
		// initialization stays lazy: parsing and indexing a package
		// never runs its initializer
		if diff := cmp.Diff(p.State(), runtime.Indexed); diff != "" {
			t.Errorf("%s: State (-got +want):\n%s", name, diff)
		}
	}
}
