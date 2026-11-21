package repl

// pkginfo.go — REPL-side package introspection (:imports, :importers):
// the imports-only answers [pkg/modulewalk] gives over the session's
// module tree. Nothing here loads or interprets the target package;
// these are pure filesystem scans.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/podhmo/minigo/pkg/locator"
	"github.com/podhmo/minigo/pkg/modulewalk"
	"github.com/podhmo/minigo/runtime"
)

// Imports answers "what does this package import" (:imports). ref
// resolves like :ls — a session-import name, a directory anchored at
// the engine's start directory, or an import path; empty means the
// entered package. The result is one line per import, package header
// first, test-file imports suffixed " (test)".
func (r *REPL) Imports(ctx context.Context, ref string) ([]string, error) {
	pi, err := r.packageImports(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := []string{fmt.Sprintf("%s (%s)", pi.Name, pi.ImportPath)}
	out = append(out, pi.Imports...)
	for _, imp := range pi.TestImports {
		out = append(out, imp+" (test)")
	}
	return out, nil
}

// Importers answers "who imports this" (:importers): the packages in
// the module containing the session's start directory that import ref,
// one "name (path)" line each. ref resolves like :ls; test-file
// importers are suffixed " (test)". A session rooted outside any
// module scans the start directory itself.
func (r *REPL) Importers(ctx context.Context, ref string) ([]string, error) {
	ip, err := r.importPathForScan(ref)
	if err != nil {
		return nil, err
	}
	loc, err := r.sessionLocator()
	if err != nil {
		return nil, fmt.Errorf("importers: %w", err)
	}
	w := &modulewalk.Walker{Locator: loc, IncludeTests: true}
	pkgs, err := w.FindImporters(ctx, ip)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pi := range pkgs {
		suffix := ""
		if !slices.Contains(pi.Imports, ip) { // reached only through a test file
			suffix = " (test)"
		}
		out = append(out, fmt.Sprintf("%s (%s)%s", pi.Name, pi.ImportPath, suffix))
	}
	return out, nil
}

// packageImports resolves ref and reads its import table — a directory
// gets a locator rooted inside its own module so a nested go.mod claims
// it; an import path resolves against the session's module.
func (r *REPL) packageImports(ctx context.Context, ref string) (*modulewalk.PackageImports, error) {
	ref = unquoteRef(ref)
	if path, ok := r.ImportPathOf(ref); ok {
		ref = path
	}
	if ref == "" {
		p := r.Current()
		if p == nil {
			return nil, fmt.Errorf("imports: no package to inspect — :cd into one first")
		}
		loc, err := r.sessionLocator()
		if err != nil {
			return nil, fmt.Errorf("imports: %w", err)
		}
		ref = scanRefOf(loc, p)
	}
	if dir := r.anchor(ref); dirExists(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		loc, err := locator.New(abs, locator.WithGoModuleResolver())
		if err != nil {
			return nil, err
		}
		ip, err := loc.PathToImport(abs)
		if err != nil {
			return nil, fmt.Errorf("imports: %w", err)
		}
		w := &modulewalk.Walker{Locator: loc}
		return w.ScanDir(ctx, abs, ip)
	}
	loc, err := r.sessionLocator()
	if err != nil {
		return nil, fmt.Errorf("imports: %w", err)
	}
	w := &modulewalk.Walker{Locator: loc}
	return w.Imports(ctx, ref)
}

// scanRefOf resolves the entered package to what a scan chases — the
// spelling importers write. A local replace claims the package's
// directory first: importers write the replaced (OldPath) spelling.
// Otherwise a real import path wins over re-deriving it from p.Dir — a
// package pulled in through a replace into another module's tree would
// otherwise be spelled by the replacement's module path, which nothing
// imports — unless the directory sits behind a go.mod boundary inside
// the session's module (a nested module or a workspace member): then
// p.Path is a parent-relative spelling importers cannot write, and the
// directory answers so its own module names it. Synthetic <dir>/<file>
// spellings and pathless packages go through the directory too.
func scanRefOf(loc *locator.Locator, p *runtime.Package) string {
	if p.Dir == "" {
		return p.Path
	}
	if rp := loc.ReplacedImportPath(p.Dir); rp != "" {
		return rp
	}
	if p.Path != "" && !syntheticImportPath(p.Path) && !nestedModuleDir(loc.RootDir(), p.Dir) {
		return p.Path
	}
	return p.Dir
}

// nestedModuleDir reports whether dir sits under a go.mod boundary
// below root.
func nestedModuleDir(root, dir string) bool {
	inside := func(d string) string {
		rel, err := filepath.Rel(root, d)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ""
		}
		return rel
	}
	if root == "" || inside(dir) == "" {
		return false
	}
	// dir's own go.mod is a boundary too (dir is the nested module's
	// root) — unless dir is the session root itself.
	if inside(dir) != "." {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			return true
		}
	}
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		rel := inside(d)
		if rel == "" || rel == "." {
			return false
		}
		if st, err := os.Stat(filepath.Join(d, "go.mod")); err == nil && !st.IsDir() {
			return true
		}
	}
}

// sessionLocator builds a module locator anchored at the session's
// start directory — the root the importers scan and path resolution
// share.
func (r *REPL) sessionLocator() (*locator.Locator, error) {
	root := r.engine.Cwd
	if root == "" {
		root = "."
	}
	return locator.New(root, locator.WithGoModuleResolver())
}

func syntheticImportPath(path string) bool {
	return strings.HasPrefix(path, "<dir>") || strings.HasPrefix(path, "<file>")
}

// importPathForScan spells ref as the import path an importers query
// chases: a directory goes through its own module's locator, anything
// else is already a path.
func (r *REPL) importPathForScan(ref string) (string, error) {
	ref = unquoteRef(ref)
	if path, ok := r.ImportPathOf(ref); ok {
		ref = path
	}
	if ref == "" {
		p := r.Current()
		if p == nil {
			return "", fmt.Errorf("importers: no package to inspect — :cd into one first")
		}
		loc, err := r.sessionLocator()
		if err != nil {
			return "", err
		}
		ref = scanRefOf(loc, p)
	}
	if dir := r.anchor(ref); dirExists(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		loc, err := locator.New(abs, locator.WithGoModuleResolver())
		if err != nil {
			return "", err
		}
		return loc.PathToImport(abs)
	}
	return ref, nil
}
