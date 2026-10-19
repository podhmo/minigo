package resolve

// repo.go — repo-wide package enumeration: the counterpart of the
// one-dir-at-a-time Resolver interface. ScanRepo finds every directory
// holding .go files under a root and reports which module claims it,
// so callers that need "all packages" (affected-package detection,
// repo sweeps) do not have to name the packages first.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// RepoPackage is one package directory found by [ScanRepo]: which module
// claims it, the import path that module spells for it, and its files
// split at the *_test.go boundary. Nothing is parsed, so the files are
// not filtered by build constraints or package clause; pass Dir to
// [Resolver.LocateDir] for the build file list.
type RepoPackage struct {
	ImportPath string   // module path + relative subdir; "<dir><abs>" when module-less (same spelling as LocateDir)
	Dir        string   // absolute package directory
	ModuleDir  string   // directory of the enclosing go.mod; "" when no module claims Dir
	ModulePath string   // declared path of the enclosing module; "" when module-less
	GoFiles    []string // sorted absolute paths of non-test .go sources
	TestFiles  []string // sorted absolute paths of *_test.go sources
	HasTests   bool     // len(TestFiles) > 0
}

// ScanRepo walks root and reports every directory holding .go files as a
// [RepoPackage] — claimed by the nearest enclosing go.mod, which may lie
// above root, so nested modules report their own module identity.
// Directories the go tool ignores are skipped below root: vendor,
// testdata, and names starting with "." or "_"; files starting with "."
// or "_" are skipped too. Paths spell root with its symlinks resolved.
// The result is sorted by Dir. A missing root or a go.mod that does not
// parse is an error, not a partial result.
func ScanRepo(root string) ([]*RepoPackage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("entry %q not found: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("entry %q is not a directory", root)
	}
	// WalkDir does not follow a symlinked root — resolve it so the
	// enumerated paths spell the real location, the same spelling the
	// loaders hand back.
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}

	var modules []repoModule
	byDir := map[string]*RepoPackage{}
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != abs && skipRepoDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name == "go.mod" {
			m, err := repoModuleOf(path)
			if err != nil {
				return err
			}
			modules = append(modules, m)
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		dir := filepath.Dir(path)
		p := byDir[dir]
		if p == nil {
			p = &RepoPackage{Dir: dir}
			byDir[dir] = p
		}
		if strings.HasSuffix(name, "_test.go") {
			p.TestFiles = append(p.TestFiles, path)
		} else {
			p.GoFiles = append(p.GoFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// deepest module dir first: a nested module claims its subtree before
	// the parent module's prefix can match it.
	sort.Slice(modules, func(i, j int) bool { return len(modules[i].dir) > len(modules[j].dir) })

	pkgs := make([]*RepoPackage, 0, len(byDir))
	for _, p := range byDir {
		var claim repoModule
		for _, m := range modules {
			if p.Dir == m.dir || strings.HasPrefix(p.Dir, m.dir+string(filepath.Separator)) {
				claim = m
				break
			}
		}
		if claim.dir == "" {
			// no module under the scan root claims it — the governing
			// go.mod may sit above the root (scanning a subdirectory
			// of a module); the nearest ancestor wins there too.
			m, ok, err := ancestorModuleOf(p.Dir)
			if err != nil {
				return nil, err
			}
			if ok {
				claim = m
			}
		}
		if claim.dir != "" {
			p.ModuleDir = claim.dir
			p.ModulePath = claim.path
			if rel, err := filepath.Rel(claim.dir, p.Dir); err == nil && rel != "." {
				p.ImportPath = claim.path + "/" + filepath.ToSlash(rel)
			} else {
				p.ImportPath = claim.path
			}
		} else {
			p.ImportPath = "<dir>" + filepath.ToSlash(p.Dir)
		}
		sort.Strings(p.GoFiles)
		sort.Strings(p.TestFiles)
		p.HasTests = len(p.TestFiles) > 0
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Dir < pkgs[j].Dir })
	return pkgs, nil
}

// repoModule is a discovered go.mod: the directory it governs and the
// module path it declares.
type repoModule struct {
	dir  string
	path string
}

// ancestorModuleOf climbs dir's ancestors for the nearest go.mod —
// used when no module discovered under the scan root claims the dir:
// a subdirectory scan can still belong to a module rooted above it.
// dir itself is never checked: a go.mod there would have registered
// during the walk and claimed it already.
func ancestorModuleOf(dir string) (repoModule, bool, error) {
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		name := filepath.Join(d, "go.mod")
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			m, err := repoModuleOf(name)
			if err != nil {
				return repoModule{}, false, err
			}
			return m, true, nil
		}
		if filepath.Dir(d) == d {
			return repoModule{}, false, nil
		}
	}
}

// repoModuleOf parses a go.mod for its module directive. A file that
// does not parse is an error — silently skipping it would mis-claim
// the subtree under the parent module.
func repoModuleOf(goModPath string) (repoModule, error) {
	b, err := os.ReadFile(goModPath)
	if err != nil {
		return repoModule{}, err
	}
	f, err := modfile.Parse(goModPath, b, nil)
	if err != nil {
		return repoModule{}, fmt.Errorf("%s: %w", goModPath, err)
	}
	if f.Module == nil {
		return repoModule{}, fmt.Errorf("%s: no module directive", goModPath)
	}
	return repoModule{dir: filepath.Dir(goModPath), path: f.Module.Mod.Path}, nil
}

// skipRepoDir mirrors the go tool's ignored-directory rule: vendor and
// testdata subtrees, and any .- or _-prefixed directory, hold no
// packages of the repo.
func skipRepoDir(name string) bool {
	return name == "vendor" || name == "testdata" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}
