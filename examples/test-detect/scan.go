package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// module is a Go module rooted at dir, discovered by its go.mod.
type module struct {
	dir  string // absolute directory containing go.mod
	path string // module path declared in go.mod
}

// pkg is a directory-level package node: a directory's .go files
// (including _test.go files and the external `foo_test` package) share
// one node, matching the granularity of `go test`.
type pkg struct {
	importPath string
	dir        string // absolute
	moduleDir  string // absolute
	files      []string
	hasTests   bool
	imports    map[string]struct{} // all imports seen in the directory's files
}

// graph is the repository's internal import graph plus lookups.
type graph struct {
	modules  []*module
	byDir    map[string]*pkg   // abs dir -> pkg
	rev      map[string][]*pkg // imported path -> importing packages (internal only)
	paths    []string          // known module path prefixes
	warnings []string          // non-fatal scan diagnostics (parse errors)
}

// scanRepo walks every Go module under root and builds the reverse
// dependency graph. Each .go file is parsed with parser.ImportsOnly:
// the package clause and import declarations only — no type checking,
// no module resolution, no network.
func scanRepo(root string) (*graph, error) {
	mods, err := findModules(root)
	if err != nil {
		return nil, err
	}
	if len(mods) == 0 {
		return nil, fmt.Errorf("%s: no go.mod found", root)
	}
	g := &graph{
		modules: mods,
		byDir:   map[string]*pkg{},
		rev:     map[string][]*pkg{},
	}
	for _, m := range mods {
		g.paths = append(g.paths, m.path)
	}
	for _, m := range mods {
		if err := scanModule(g, m); err != nil {
			return nil, err
		}
	}

	// Build reverse edges once all packages are known: for each internal
	// import, record who imports it.
	for _, p := range g.byDir {
		for imp := range p.imports {
			if g.internal(imp) {
				g.rev[imp] = append(g.rev[imp], p)
			}
		}
	}
	return g, nil
}

// findModules returns every go.mod under root, skipping directories the
// go tool ignores plus vendor/.
func findModules(root string) ([]*module, error) {
	var mods []*module
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			mp, err := modulePathOf(path)
			if err != nil {
				return err
			}
			mods = append(mods, &module{dir: filepath.Dir(path), path: mp})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Deepest modules first so callers can match the nearest enclosing one.
	sort.Slice(mods, func(i, j int) bool { return len(mods[i].dir) > len(mods[j].dir) })
	return mods, nil
}

// modulePathOf reads the `module <path>` line from a go.mod file.
// go.mod allows a `//` line comment and a quoted module path; both are
// handled here (a module directive never appears inside a block).
func modulePathOf(goModPath string) (string, error) {
	f, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(cutComment(sc.Text()))
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", goModPath, err)
	}
	return "", fmt.Errorf("%s: no module directive", goModPath)
}

// cutComment drops a `//` line comment that is not inside double quotes.
func cutComment(s string) string {
	inQuotes := false
	for i := 0; i+1 < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case '/':
			if !inQuotes && s[i+1] == '/' {
				return s[:i]
			}
		}
	}
	return s
}

// scanModule walks one module's directory and parses every .go file.
// Subdirectories containing their own go.mod are skipped — they belong
// to a nested module that gets its own walk.
func scanModule(g *graph, m *module) error {
	return filepath.WalkDir(m.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != m.dir {
				if skipDir(d.Name()) {
					return fs.SkipDir
				}
				// A nested module's files are not part of this module.
				// The go.mod must be a file — a subdirectory named
				// go.mod does not declare a module.
				if info, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		return addFile(g, m, path)
	})
}

// addFile parses one .go file for its imports and merges it into its
// directory's package node. A parse failure still keeps whatever imports
// were recovered (ImportsOnly only reads the header, so failures live in
// the package/import declarations) and records a warning — silently
// dropping the file would silently drop test coverage.
func addFile(g *graph, m *module, path string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		g.warnings = append(g.warnings, fmt.Sprintf("%s: parse error (%v); imports may be incomplete", path, err))
		if f == nil {
			return nil
		}
	}
	dir := filepath.Dir(path)
	p, ok := g.byDir[dir]
	if !ok {
		rel, err := filepath.Rel(m.dir, dir)
		if err != nil {
			return err
		}
		imp := m.path
		if rel != "." {
			imp += "/" + filepath.ToSlash(rel)
		}
		p = &pkg{
			importPath: imp,
			dir:        dir,
			moduleDir:  m.dir,
			imports:    map[string]struct{}{},
		}
		g.byDir[dir] = p
	}
	p.files = append(p.files, path)
	if strings.HasSuffix(path, "_test.go") {
		p.hasTests = true
	}
	mergeImports(p, f)
	return nil
}

// mergeImports unions a parsed file's imports into the package's set —
// test-file imports included, so test-only dependencies create edges.
func mergeImports(p *pkg, f *ast.File) {
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		p.imports[path] = struct{}{}
	}
}

// internal reports whether an import path belongs to any discovered
// module (the exact module path or a package beneath it).
func (g *graph) internal(imp string) bool {
	for _, m := range g.paths {
		if imp == m || strings.HasPrefix(imp, m+"/") {
			return true
		}
	}
	return false
}

// skipDir reports whether a directory name is one the go tool ignores
// (or vendored dependencies): testdata, hidden, underscore-prefixed.
func skipDir(name string) bool {
	return name == "vendor" || name == "testdata" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}
