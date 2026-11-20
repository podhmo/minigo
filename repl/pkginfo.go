package repl

// pkginfo.go — REPL-side package introspection (:imports, :importers):
// the imports-only answers [pkg/modulewalk] gives over the session's
// module tree. Nothing here loads or interprets the target package;
// these are pure filesystem scans.

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/podhmo/minigo/pkg/locator"
	"github.com/podhmo/minigo/pkg/modulewalk"
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
	root := r.engine.Cwd
	if root == "" {
		root = "."
	}
	loc, err := locator.New(root, locator.WithGoModuleResolver())
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
		if p.Dir != "" { // a dir-loaded package spells its import path through its own module
			ref = p.Dir
		} else {
			ref = p.Path
		}
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
	root := r.engine.Cwd
	if root == "" {
		root = "."
	}
	loc, err := locator.New(root, locator.WithGoModuleResolver())
	if err != nil {
		return nil, fmt.Errorf("imports: %w", err)
	}
	w := &modulewalk.Walker{Locator: loc}
	return w.Imports(ctx, ref)
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
		if p.Dir != "" {
			ref = p.Dir
		} else {
			return p.Path, nil
		}
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
