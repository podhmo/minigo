// Package archcheck inspects the module's internal import graph using
// only go/parser — no go/types, go/packages, or go list. The graph is
// checked against an allow list (Rules) that acts as a ratchet: today
// it pins the current graph, later phases tighten it per
// docs/next/ja/references/archcheck.md.
package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Graph maps a module-internal package (slash path relative to the
// module root, "." for the root package) to the sorted list of internal
// packages it imports. Test files count toward the importing package.
type Graph map[string][]string

// Build walks root, parses every .go file (ImportsOnly) and returns the
// graph of module-internal imports. Directories that contain their own
// go.mod are module boundaries and are skipped (examples/, testdata
// fixtures); testdata and hidden directories are skipped too.
func Build(root string) (Graph, error) {
	modPath, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	g := Graph{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root {
			base := d.Name()
			if strings.HasPrefix(base, ".") || base == "testdata" {
				return fs.SkipDir
			}
			if exists(filepath.Join(path, "go.mod")) {
				return fs.SkipDir
			}
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		imports := map[string]bool{}
		hasGo := false
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".go") {
				continue
			}
			hasGo = true
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(path, name), nil, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("archcheck: parse %s: %w", path, err)
			}
			for _, spec := range f.Imports {
				p, _ := strconv.Unquote(spec.Path.Value)
				if p == modPath {
					imports["."] = true
				} else if rel, ok := strings.CutPrefix(p, modPath+"/"); ok {
					imports[rel] = true
				}
			}
		}
		if hasGo {
			rel := rel(root, path)
			// An external test package (foo_test) importing foo is not a
			// dependency edge; drop self-edges entirely.
			delete(imports, rel)
			list := []string{}
			for imp := range imports {
				list = append(list, imp)
			}
			sort.Strings(list)
			g[rel] = list
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// Violation is one broken rule.
type Violation struct {
	Pkg     string // importing package ("." for root)
	Dep     string // imported internal package
	Message string
}

// Check compares g against the allow rules and returns one violation
// per edge not permitted, plus one per allowed edge that no longer
// exists in the code (the table must stay in sync with reality).
func Check(g Graph, rules map[string][]string) []Violation {
	var out []Violation
	for pkg, deps := range g {
		allowed := map[string]bool{}
		for _, d := range rules[pkg] {
			allowed[d] = true
		}
		for _, dep := range deps {
			if !allowed[dep] {
				out = append(out, Violation{pkg, dep, fmt.Sprintf("%s imports %s, which the arch allow list does not permit", pkg, dep)})
			}
		}
	}
	for pkg, deps := range rules {
		have := map[string]bool{}
		for _, d := range g[pkg] {
			have[d] = true
		}
		for _, dep := range deps {
			if !have[dep] {
				out = append(out, Violation{pkg, dep, fmt.Sprintf("%s no longer imports %s; drop the stale allow-list entry", pkg, dep)})
			}
		}
	}
	return out
}

// SelectorReport is a banned selector use inside a package's files.
type SelectorReport struct {
	Pkg  string
	File string
	Line int
	Name string // e.g. "Materialize"
}

// BannedSelectors scans the named package directories under root for
// selector expressions `.Name` whose name is in banned, and reports each
// use. Name matching cannot prove the receiver's type; it is a ratchet
// aid, not a proof (docs/next/ja/references/archcheck.md).
func BannedSelectors(root string, pkgDirs []string, banned map[string]bool) ([]SelectorReport, error) {
	var out []SelectorReport
	for _, dir := range pkgDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && banned[sel.Sel.Name] {
					out = append(out, SelectorReport{dir, path, fset.Position(sel.Pos()).Line, sel.Sel.Name})
				}
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func modulePath(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(p), nil
		}
	}
	return "", fmt.Errorf("archcheck: no module line in %s", gomod)
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
