// Package modulewalk is the imports-only layer of repo enumeration:
// given a module root it answers "which packages exist, and what does
// each import" without ever building a full AST. It ports the go-scan
// `modulewalker.go` shape — `Walker`/`Visitor`, `Walk`, `FindImporters`,
// a reverse-dependency map and drift detection — onto
// [resolve.ScanRepo] (nested go.mod aware) and [syntax.ParseImports]
// (header-only parse).
//
// It is a tooling layer, not part of the interpreter: nothing here
// touches the VM, and nothing in the interpreter depends on it.
package modulewalk

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/podhmo/minigo/pkg/locator"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/syntax"
	"golang.org/x/mod/modfile"
)

// PackageImports is one package's import table: the package clause name
// plus every import path its files declare, split at the *_test.go
// boundary. It carries the enclosing module's identity so nested go.mod
// trees report their own path, not the walk root's.
type PackageImports struct {
	Name        string              // package clause of the first non-test file
	ImportPath  string              // the enclosing module's spelling for Dir
	Dir         string              // absolute package directory
	ModuleDir   string              // directory of the enclosing go.mod; "" when module-less
	ModulePath  string              // declared path of the enclosing module; "" when module-less
	Imports     []string            // deduped, sorted union over non-test files
	TestImports []string            // deduped, sorted union over *_test.go files
	FileImports map[string][]string // absolute file path -> its import paths (all files)
}

// Visitor receives each package discovered during a [Walker.Walk] and
// returns the imports the walker should follow next. Returning nil (or
// an empty slice) stops traversal from that node; the walk only chases
// what the visitor returns, so stdlib and external imports are pruned
// by the caller, not by the walker.
type Visitor interface {
	Visit(pkg *PackageImports) (importsToFollow []string, err error)
}

// VisitorFunc adapts a function to [Visitor].
type VisitorFunc func(pkg *PackageImports) ([]string, error)

// Visit calls f.
func (f VisitorFunc) Visit(pkg *PackageImports) ([]string, error) { return f(pkg) }

// Walker reads package import tables under a module tree. It memoizes
// each package the first time it is scanned and records every file it
// parsed so [Walker.UnscannedGoFiles] can report drift. A zero Walker
// needs only Locator set.
type Walker struct {
	Locator      *locator.Locator // resolves import paths to directories
	IncludeTests bool             // include *_test.go in Imports (TestImports always fills)

	mu      sync.RWMutex
	fset    *token.FileSet
	cache   map[scanKey]*PackageImports // (dir, importPath) — a replace can point two paths at one dir
	scanned map[string]bool             // every file handed to ParseImports
}

// scanKey keys the walk cache: two checkouts may share an import path,
// and a module replace can point two paths at one dir — neither alone
// is unique.
type scanKey struct {
	dir, importPath string
}

func (w *Walker) init() {
	if w.fset == nil {
		w.fset = token.NewFileSet()
	}
	if w.cache == nil {
		w.cache = map[scanKey]*PackageImports{}
	}
	if w.scanned == nil {
		w.scanned = map[string]bool{}
	}
}

// Imports returns the import table of the package at importPath — the
// go-scan `ScanPackageFromFilePathImports` equivalent. The result is
// cached for the walker's lifetime.
func (w *Walker) Imports(ctx context.Context, importPath string) (*PackageImports, error) {
	w.mu.Lock()
	w.init()
	w.mu.Unlock()
	if w.Locator == nil {
		return nil, fmt.Errorf("modulewalk: no locator; cannot resolve %q", importPath)
	}
	dir, err := w.Locator.FindPackageDir(importPath)
	if err != nil {
		return nil, fmt.Errorf("modulewalk: no directory for %s: %w", importPath, err)
	}
	return w.scanDir(ctx, dir, importPath)
}

// ScanDir reads the import table of the package in dir, spelled
// importPath — the entry [Walker.WalkRepo] and [Walker.FindImporters]
// use for directories [resolve.ScanRepo] already enumerated. The result
// is cached under (dir, importPath).
func (w *Walker) ScanDir(ctx context.Context, dir, importPath string) (*PackageImports, error) {
	return w.scanDir(ctx, dir, importPath)
}

func (w *Walker) scanDir(ctx context.Context, dir, importPath string) (*PackageImports, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo := &resolve.RepoPackage{ImportPath: importPath, Dir: dir}
	repo.ModuleDir, repo.ModulePath = moduleOf(dir)
	if err := listPackageFiles(repo); err != nil {
		return nil, err
	}
	return w.scanRepoPackage(ctx, repo)
}

// moduleOf reports the enclosing module's directory and declared path —
// the nearest ancestor of dir holding a go.mod, or ("", "") when none.
func moduleOf(dir string) (modDir, modPath string) {
	for d := dir; ; {
		modfilePath := filepath.Join(d, "go.mod")
		data, err := os.ReadFile(modfilePath)
		if err == nil {
			if mf, perr := modfile.Parse(modfilePath, data, nil); perr == nil && mf.Module != nil {
				return d, mf.Module.Mod.Path
			}
			return d, ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// listPackageFiles fills GoFiles/TestFiles for a bare directory — the
// same split [resolve.ScanRepo] reports — when the caller has a dir
// rather than an enumerated RepoPackage.
func listPackageFiles(repo *resolve.RepoPackage) error {
	entries, err := os.ReadDir(repo.Dir)
	if err != nil {
		return fmt.Errorf("modulewalk: list %s: %w", repo.Dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name[0] == '.' || name[0] == '_' {
			continue
		}
		abs := filepath.Join(repo.Dir, name)
		if strings.HasSuffix(name, "_test.go") {
			repo.TestFiles = append(repo.TestFiles, abs)
		} else {
			repo.GoFiles = append(repo.GoFiles, abs)
		}
	}
	sort.Strings(repo.GoFiles)
	sort.Strings(repo.TestFiles)
	repo.HasTests = len(repo.TestFiles) > 0
	return nil
}

func (w *Walker) scanRepoPackage(ctx context.Context, repo *resolve.RepoPackage) (*PackageImports, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.init()
	w.mu.Unlock()
	w.mu.RLock()
	cached, ok := w.cache[scanKey{repo.Dir, repo.ImportPath}]
	w.mu.RUnlock()
	if ok {
		return cached, nil
	}
	pi := &PackageImports{
		ImportPath:  repo.ImportPath,
		Dir:         repo.Dir,
		ModuleDir:   repo.ModuleDir,
		ModulePath:  repo.ModulePath,
		FileImports: map[string][]string{},
	}
	seen := map[string]bool{}
	seenTest := map[string]bool{}
	addFile := func(path string, test bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		name, imports, err := syntax.ParseImports(w.fset, path, nil)
		if err != nil {
			return fmt.Errorf("modulewalk: %s: %w", path, err)
		}
		w.mu.Lock()
		w.scanned[path] = true
		w.mu.Unlock()
		if pi.Name == "" && !test {
			pi.Name = name
		}
		var paths []string
		for _, imp := range imports {
			paths = append(paths, imp.Path)
			if test {
				if !seenTest[imp.Path] {
					seenTest[imp.Path] = true
					pi.TestImports = append(pi.TestImports, imp.Path)
				}
			} else if !seen[imp.Path] {
				seen[imp.Path] = true
				pi.Imports = append(pi.Imports, imp.Path)
			}
		}
		pi.FileImports[path] = paths
		return nil
	}
	for _, f := range repo.GoFiles {
		if err := addFile(f, false); err != nil {
			return nil, err
		}
	}
	for _, f := range repo.TestFiles {
		if err := addFile(f, true); err != nil {
			return nil, err
		}
	}
	sort.Strings(pi.Imports)
	sort.Strings(pi.TestImports)
	w.mu.Lock()
	w.cache[scanKey{repo.Dir, repo.ImportPath}] = pi
	w.mu.Unlock()
	return pi, nil
}

// Walk performs a BFS over the import graph starting from patterns —
// literal import paths or "./..."-style directory patterns relative to
// the locator's root — calling visitor.Visit on each package once and
// following only the imports it returns.
func (w *Walker) Walk(ctx context.Context, visitor Visitor, patterns ...string) error {
	queue, err := w.resolvePatterns(ctx, patterns)
	if err != nil {
		return err
	}
	visited := map[string]bool{}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true

		pi, err := w.Imports(ctx, cur)
		if err != nil {
			return err
		}
		follow, err := visitor.Visit(pi)
		if err != nil {
			return fmt.Errorf("modulewalk: visitor failed for %s: %w", cur, err)
		}
		queue = append(queue, follow...)
	}
	return nil
}

// resolvePatterns expands "./..."-style patterns (relative to the
// locator's root) to the import paths of every package beneath them;
// patterns without "..." are taken as literal import paths.
func (w *Walker) resolvePatterns(ctx context.Context, patterns []string) ([]string, error) {
	if w.Locator == nil {
		return nil, fmt.Errorf("modulewalk: no locator; cannot resolve patterns")
	}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		if !strings.Contains(pattern, "...") {
			seen[pattern] = true
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(pattern, "..."), "/")
		abs := base
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(w.Locator.RootDir(), base)
		}
		baseMod, _ := moduleOf(abs)
		err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			// the go tool's exclusions: vendor, testdata, dot- and
			// underscore-prefixed dirs never hold packages
			if name := d.Name(); path != abs && (name == "vendor" || name == "testdata" || name[0] == '.' || name[0] == '_') {
				return filepath.SkipDir
			}
			// a nested module is its own tree — ./... never crosses it,
			// the same carve-out the go tool applies
			if modDir, _ := moduleOf(path); modDir == path && path != baseMod {
				return filepath.SkipDir
			}
			ok, err := hasGoFiles(path)
			if err != nil {
				return nil // unreadable dir: skip
			}
			if ok {
				ip, err := w.Locator.PathToImport(path)
				if err == nil {
					seen[ip] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("modulewalk: pattern %q: %w", pattern, err)
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// WalkRepo enumerates every package under root with [resolve.ScanRepo]
// — nested go.mod trees included — and visits each once in directory
// order. Unlike [Walker.Walk] it is not import-graph driven: the
// visitor's follow list is ignored.
func (w *Walker) WalkRepo(ctx context.Context, root string, visitor Visitor) error {
	pkgs, err := resolve.ScanRepo(root)
	if err != nil {
		return err
	}
	return w.walkRepoPackages(ctx, pkgs, visitor)
}

func (w *Walker) walkRepoPackages(ctx context.Context, pkgs []*resolve.RepoPackage, visitor Visitor) error {
	for _, repo := range pkgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		pi, err := w.scanRepoPackage(ctx, repo)
		if err != nil {
			return err
		}
		if _, err := visitor.Visit(pi); err != nil {
			return fmt.Errorf("modulewalk: visitor failed for %s: %w", repo.ImportPath, err)
		}
	}
	return nil
}

// FindImporters scans the locator's root module and returns the
// packages importing targetImportPath — the reverse-dependency query.
// Nested modules under the root report their own import paths; with
// IncludeTests, test-file imports count too.
func (w *Walker) FindImporters(ctx context.Context, targetImportPath string) ([]*PackageImports, error) {
	if w.Locator == nil {
		return nil, fmt.Errorf("modulewalk: no locator")
	}
	var out []*PackageImports
	err := w.WalkRepo(ctx, w.Locator.RootDir(), VisitorFunc(func(pi *PackageImports) ([]string, error) {
		for _, imp := range pi.Imports {
			if imp == targetImportPath {
				out = append(out, pi)
				return nil, nil
			}
		}
		if w.IncludeTests {
			for _, imp := range pi.TestImports {
				if imp == targetImportPath {
					out = append(out, pi)
					return nil, nil
				}
			}
		}
		return nil, nil
	}))
	return out, err
}

// ReverseDependencyMap scans the locator's root module and maps each
// imported path to the sorted list of packages importing it.
func (w *Walker) ReverseDependencyMap(ctx context.Context) (map[string][]string, error) {
	if w.Locator == nil {
		return nil, fmt.Errorf("modulewalk: no locator")
	}
	deps := map[string][]string{}
	err := w.WalkRepo(ctx, w.Locator.RootDir(), VisitorFunc(func(pi *PackageImports) ([]string, error) {
		seen := map[string]bool{}
		for _, imp := range pi.Imports {
			deps[imp] = append(deps[imp], pi.ImportPath)
			seen[imp] = true
		}
		if w.IncludeTests {
			for _, imp := range pi.TestImports {
				if seen[imp] { // a normal file already counts this import
					continue
				}
				deps[imp] = append(deps[imp], pi.ImportPath)
			}
		}
		return nil, nil
	}))
	for _, list := range deps {
		sort.Strings(list)
	}
	return deps, err
}

// UnscannedGoFiles reports the .go files in the package at
// pathOrImportPath — a directory (absolute or relative) or an import
// path the locator resolves — that this walker has not parsed, so a
// caller doing iterative scans can spot drift. *_test.go files count
// only when IncludeTests is set.
func (w *Walker) UnscannedGoFiles(pathOrImportPath string) ([]string, error) {
	dir := ""
	if abs, err := filepath.Abs(pathOrImportPath); err == nil {
		if st, statErr := os.Stat(abs); statErr == nil && st.IsDir() {
			dir = abs
		}
	}
	if dir == "" {
		if w.Locator == nil {
			return nil, fmt.Errorf("modulewalk: no locator; cannot resolve %q", pathOrImportPath)
		}
		var err error
		dir, err = w.Locator.FindPackageDir(pathOrImportPath)
		if err != nil {
			return nil, fmt.Errorf("modulewalk: %q is neither a directory nor a resolvable import path: %w", pathOrImportPath, err)
		}
	}
	repo := &resolve.RepoPackage{Dir: dir}
	if err := listPackageFiles(repo); err != nil {
		return nil, err
	}
	var out []string
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, f := range repo.GoFiles {
		if !w.scanned[f] {
			out = append(out, f)
		}
	}
	if w.IncludeTests {
		for _, f := range repo.TestFiles {
			if !w.scanned[f] {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// DiscoverModules finds the Go modules under root: the `use`
// directories of a go.work when root has one, else every directory
// holding a go.mod beneath it (vendor and dot-prefixed dirs skipped,
// plus any basenames in excludeDirs). This is the find-orphans
// `discoverModules` shape — the entry point for multi-module sweeps.
func DiscoverModules(root string, excludeDirs []string) ([]string, error) {
	workPath := filepath.Join(root, "go.work")
	if _, err := os.Stat(workPath); err == nil {
		data, err := os.ReadFile(workPath)
		if err != nil {
			return nil, fmt.Errorf("modulewalk: read go.work: %w", err)
		}
		wf, err := modfile.ParseWork(workPath, data, nil)
		if err != nil {
			return nil, fmt.Errorf("modulewalk: parse go.work: %w", err)
		}
		var out []string
		for _, use := range wf.Use {
			// a use path is relative to the go.work dir unless absolute
			dir := filepath.FromSlash(use.Path)
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			out = append(out, dir)
		}
		sort.Strings(out)
		return out, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	excluded := map[string]bool{"vendor": true, "testdata": true}
	for _, d := range excludeDirs {
		excluded[d] = true
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if excluded[name] || (name != "." && strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			out = append(out, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("modulewalk: walk %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

func hasGoFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}
