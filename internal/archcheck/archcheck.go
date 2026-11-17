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

// pkgDecls is one parsed package: named type declarations plus its
// import alias table, for resolving SelectorExpr types.
type pkgDecls struct {
	types   map[string]*ast.TypeSpec
	imports map[string]string
}

// loadPkg parses every non-test .go file in root/pkgRel and returns its
// type and import tables.
func loadPkg(root, pkgRel string) (*pkgDecls, error) {
	p := &pkgDecls{types: map[string]*ast.TypeSpec{}, imports: map[string]string{}}
	ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(pkgRel)))
	if err != nil {
		return nil, err
	}
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(pkgRel), name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			alias := ""
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias == "" {
				alias = path[strings.LastIndex(path, "/")+1:]
			}
			if alias != "." && alias != "_" {
				p.imports[alias] = path
			}
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					p.types[ts.Name.Name] = ts
				}
			}
		}
	}
	return p, nil
}

// CheckSharedFields walks every field type reachable from the struct
// typeName in package pkgDir (following named types into other
// module-internal packages) and reports execution-state leaks:
//
//   - a field whose type is in forbidden ("pkg/path.Type" entries)
//   - a function-typed field, unless its "StructName.FieldName" path is
//     in exempt
//
// Field names use the declaring struct's name, so a nested violation
// reports the struct that holds it.
func CheckSharedFields(root, pkgDir, typeName string, forbidden, exempt map[string]bool) ([]Violation, error) {
	modPath, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	type ref struct{ pkg, name string }
	seen := map[ref]bool{}
	queue := []ref{{pkgDir, typeName}}
	pkgs := map[string]*pkgDecls{}
	load := func(pkg string) (*pkgDecls, error) {
		if d, ok := pkgs[pkg]; ok {
			return d, nil
		}
		d, err := loadPkg(root, pkg)
		if err != nil {
			return nil, err
		}
		pkgs[pkg] = d
		return d, nil
	}
	var out []Violation
	report := func(pkg, strct, field, fq string) {
		if exempt[strct+"."+field] {
			return
		}
		out = append(out, Violation{pkg, fq, fmt.Sprintf("%s.%s holds forbidden shared state: field %s has type %s", strct, field, field, fq)})
	}
	var walkType func(pkg, strct, field string, expr ast.Expr) error
	walkType = func(pkg, strct, field string, expr ast.Expr) error {
		d, err := load(pkg)
		if err != nil {
			return err
		}
		switch t := expr.(type) {
		case *ast.Ident:
			if _, ok := d.types[t.Name]; ok {
				r := ref{pkg, t.Name}
				if !seen[r] {
					seen[r] = true
					queue = append(queue, r)
				}
			}
		case *ast.SelectorExpr:
			id, ok := t.X.(*ast.Ident)
			if !ok {
				return nil
			}
			path, ok := d.imports[id.Name]
			if !ok {
				return nil
			}
			fq := path + "." + t.Sel.Name
			if forbidden[fq] {
				report(pkg, strct, field, fq)
				return nil
			}
			if rel, ok := strings.CutPrefix(path, modPath+"/"); ok {
				r := ref{rel, t.Sel.Name}
				if !seen[r] {
					seen[r] = true
					queue = append(queue, r)
				}
			}
		case *ast.StarExpr:
			return walkType(pkg, strct, field, t.X)
		case *ast.ArrayType:
			return walkType(pkg, strct, field, t.Elt)
		case *ast.MapType:
			if err := walkType(pkg, strct, field, t.Key); err != nil {
				return err
			}
			return walkType(pkg, strct, field, t.Value)
		case *ast.ChanType:
			return walkType(pkg, strct, field, t.Value)
		case *ast.IndexExpr:
			return walkType(pkg, strct, field, t.X)
		case *ast.IndexListExpr:
			return walkType(pkg, strct, field, t.X)
		case *ast.FuncType:
			key := strct + "." + field
			if !exempt[key] {
				out = append(out, Violation{pkg, "", fmt.Sprintf("%s.%s is a function-typed field; shared state must not hold code", strct, field)})
			}
		}
		return nil
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		d, err := load(r.pkg)
		if err != nil {
			return nil, err
		}
		ts, ok := d.types[r.name]
		if !ok {
			continue // external shape we cannot see; interface types land here too
		}
		switch t := ts.Type.(type) {
		case *ast.StructType:
			for _, f := range t.Fields.List {
				name := ""
				if len(f.Names) > 0 {
					name = f.Names[0].Name
				} else {
					// embedded field: name is the type's own
					name = embeddedName(f.Type)
				}
				if err := walkType(r.pkg, r.name, name, f.Type); err != nil {
					return nil, err
				}
			}
		case *ast.FuncType:
			if !exempt[r.name] {
				out = append(out, Violation{r.pkg, "", fmt.Sprintf("shared state reaches function type %s", r.name)})
			}
		case *ast.Ident, *ast.SelectorExpr, *ast.StarExpr, *ast.MapType, *ast.ArrayType, *ast.IndexExpr, *ast.IndexListExpr:
			if err := walkType(r.pkg, r.name, r.name, t); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return "_"
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
