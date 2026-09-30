package goscan

import (
	"bytes"
	"context"
	"fmt"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/podhmo/minigo/examples/convert-define/pkg/locator"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

// Config holds shared configuration and state for both Scanner and ModuleWalker.
// It is intended to be embedded in both structs.
type Config struct {
	workDir             string
	locator             *locator.Locator
	scanner             *scanner.Scanner // low-level scanner
	fset                *token.FileSet
	useGoModuleResolver bool
	IncludeTests        bool
	DryRun              bool
	Inspect             bool
	Logger              *slog.Logger
	overlay             scanner.Overlay
}

// ModuleWalker is responsible for lightweight, dependency-focused scanning operations.
// It primarily deals with parsing package imports and building dependency graphs,
// without parsing the full details of type and function bodies.
type ModuleWalker struct {
	*Config
	packageImportsCache map[string]*scanner.PackageImports
	reverseDepCache     map[string][]string
	mu                  sync.RWMutex
}

// ScanPackageFromFilePathImports scans a single Go package identified by its import path,
// parsing only the package clause and import declarations for efficiency.
// It returns a lightweight PackageImports struct containing the package name
// and a list of its direct dependencies.
// Results are cached in memory for the lifetime of the ModuleWalker instance.
func (w *ModuleWalker) ScanPackageFromFilePathImports(ctx context.Context, importPath string) (*scanner.PackageImports, error) {
	w.mu.RLock()
	cachedPkg, found := w.packageImportsCache[importPath]
	w.mu.RUnlock()
	if found {
		slog.DebugContext(ctx, "ScanPackageFromFilePathImports CACHE HIT", slog.String("importPath", importPath))
		return cachedPkg, nil
	}
	slog.DebugContext(ctx, "ScanPackageFromFilePathImports CACHE MISS", slog.String("importPath", importPath))

	pkgDirAbs, err := w.locator.FindPackageDir(importPath)
	if err != nil {
		return nil, fmt.Errorf("could not find directory for import path %s: %w", importPath, err)
	}

	allGoFilesInPkg, err := listGoFilesForWalker(pkgDirAbs, w.IncludeTests)
	if err != nil {
		return nil, fmt.Errorf("ScanPackageFromFilePathImports: failed to list go files in %s: %w", pkgDirAbs, err)
	}

	if len(allGoFilesInPkg) == 0 {
		// If a directory for an import path exists but has no .go files, cache an empty PackageImports.
		pkgInfo := &scanner.PackageImports{
			ImportPath: importPath,
			Name:       filepath.Base(pkgDirAbs), // Best guess for name
			Imports:    []string{},
		}
		w.mu.Lock()
		w.packageImportsCache[importPath] = pkgInfo
		w.mu.Unlock()
		return pkgInfo, nil
	}

	pkgImports, err := w.scanner.ScanPackageFromFilePathImports(ctx, allGoFilesInPkg, pkgDirAbs, importPath)
	if err != nil {
		return nil, fmt.Errorf("ScanPackageFromFilePathImports: scanning imports for %s failed: %w", importPath, err)
	}

	w.mu.Lock()
	w.packageImportsCache[importPath] = pkgImports
	w.mu.Unlock()

	return pkgImports, nil
}

// FindImporters scans the entire module to find packages that import the targetImportPath.
// It performs an efficient, imports-only scan of all potential package directories in the module.
// The result is a list of packages that have a direct dependency on the target.
func (w *ModuleWalker) FindImporters(ctx context.Context, targetImportPath string) ([]*PackageImports, error) {
	rootDir := w.locator.RootDir()
	modulePath := w.locator.ModulePath()

	if rootDir == "" {
		return nil, fmt.Errorf("module root directory not found, cannot perform reverse dependency search")
	}

	var importers []*PackageImports

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			return nil
		}

		// Skip hidden directories and vendor
		if d.Name() == "vendor" || (len(d.Name()) > 1 && d.Name()[0] == '.') {
			return filepath.SkipDir
		}

		// path is a directory. Let's see if it's a package.
		// We can check for .go files inside it.
		goFiles, err := listGoFilesForWalker(path, w.IncludeTests) // listGoFiles is an existing helper in goscan.go
		if err != nil {
			slog.WarnContext(ctx, "could not list go files in directory, skipping", slog.String("path", path), slog.Any("error", err))
			return nil // continue walking
		}

		if len(goFiles) == 0 {
			return nil // Not a package, continue
		}

		// We have a package. Determine its import path.
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			slog.WarnContext(ctx, "could not determine relative path for package, skipping", slog.String("path", path), slog.Any("error", err))
			return nil // Continue walking
		}

		currentPkgImportPath := filepath.ToSlash(filepath.Join(modulePath, relPath))
		if relPath == "." {
			currentPkgImportPath = modulePath
		}

		// Now we can use the existing efficient scanner method.
		pkgImports, err := w.ScanPackageFromFilePathImports(ctx, currentPkgImportPath)
		if err != nil {
			slog.WarnContext(ctx, "failed to scan package imports, skipping", "importPath", currentPkgImportPath, "error", err)
			return nil // continue
		}

		// Check if it imports our target
		for _, imp := range pkgImports.Imports {
			if imp == targetImportPath {
				importers = append(importers, pkgImports)
				break // Found it, no need to check other imports
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error walking module directory for importers: %w", err)
	}

	// Sort for deterministic output
	sort.Slice(importers, func(i, j int) bool {
		return importers[i].ImportPath < importers[j].ImportPath
	})

	return importers, nil
}

// FindImportersAggressively scans the module using `git grep` to quickly find files
// that likely import the targetImportPath, then confirms them. This can be much
// faster than walking the entire directory structure in large repositories.
// A git repository is required.
func (w *ModuleWalker) FindImportersAggressively(ctx context.Context, targetImportPath string) ([]*PackageImports, error) {
	rootDir := w.locator.RootDir()
	modulePath := w.locator.ModulePath()

	if rootDir == "" {
		return nil, fmt.Errorf("module root directory not found, cannot perform aggressive reverse dependency search")
	}

	// Pattern to find import statements. We just look for the quoted import path.
	// This is a broad but effective pattern for `git grep`, as the results are
	// verified by a proper Go parser anyway. This correctly handles both
	// `import "..."` and `import ( ... "..." ... )` forms.
	pattern := fmt.Sprintf(`"%s"`, targetImportPath)

	cmd := exec.CommandContext(ctx, "git", "grep", "-l", "-F", pattern, "--", "*.go")
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.DebugContext(ctx, "executing git grep", slog.String("dir", cmd.Dir), slog.Any("args", cmd.Args))

	if err := cmd.Run(); err != nil {
		// git grep exits with 1 if no matches are found, which is not an error for us.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// No files found, return empty list.
			return nil, nil
		}
		return nil, fmt.Errorf("git grep failed: %w\n%s", err, stderr.String())
	}

	potentialFiles := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(potentialFiles) == 0 || (len(potentialFiles) == 1 && potentialFiles[0] == "") {
		return nil, nil // No matches
	}

	// Group files by directory (package)
	packagesToScan := make(map[string]struct{})
	for _, fileRelPath := range potentialFiles {
		if fileRelPath == "" {
			continue
		}
		dir := filepath.Dir(fileRelPath)
		packagesToScan[dir] = struct{}{}
	}

	var importers []*PackageImports
	for relDir := range packagesToScan {
		var currentPkgImportPath string
		if relDir == "." {
			currentPkgImportPath = modulePath
		} else {
			currentPkgImportPath = filepath.ToSlash(filepath.Join(modulePath, relDir))
		}

		// Now we can use the existing efficient scanner method to confirm.
		pkgImports, err := w.ScanPackageFromFilePathImports(ctx, currentPkgImportPath)
		if err != nil {
			slog.WarnContext(ctx, "failed to scan potential importer package, skipping", "importPath", currentPkgImportPath, "error", err)
			continue
		}

		// Check if it really imports our target
		for _, imp := range pkgImports.Imports {
			if imp == targetImportPath {
				importers = append(importers, pkgImports)
				break
			}
		}
	}

	// Sort for deterministic output
	sort.Slice(importers, func(i, j int) bool {
		return importers[i].ImportPath < importers[j].ImportPath
	})

	return importers, nil
}

// BuildReverseDependencyMap scans the entire module to build a map of reverse dependencies.
// The key of the map is an import path, and the value is a list of packages that import it.
// The result is cached within the scanner instance.
func (w *ModuleWalker) BuildReverseDependencyMap(ctx context.Context) (map[string][]string, error) {
	w.mu.RLock()
	if w.reverseDepCache != nil {
		w.mu.RUnlock()
		return w.reverseDepCache, nil
	}
	w.mu.RUnlock()

	rootDir := w.locator.RootDir()
	modulePath := w.locator.ModulePath()

	if rootDir == "" {
		return nil, fmt.Errorf("module root directory not found, cannot build reverse dependency map")
	}

	reverseDeps := make(map[string][]string)

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "vendor" || (len(d.Name()) > 1 && d.Name()[0] == '.') {
			return filepath.SkipDir
		}
		goFiles, err := listGoFilesForWalker(path, w.IncludeTests)
		if err != nil {
			slog.WarnContext(ctx, "could not list go files in directory, skipping", "path", path, "error", err)
			return nil
		}
		if len(goFiles) == 0 {
			return nil
		}
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			slog.WarnContext(ctx, "could not determine relative path for package, skipping", "path", path, "error", err)
			return nil
		}
		currentPkgImportPath := filepath.ToSlash(filepath.Join(modulePath, relPath))
		if relPath == "." {
			currentPkgImportPath = modulePath
		}
		pkgImports, err := w.ScanPackageFromFilePathImports(ctx, currentPkgImportPath)
		if err != nil {
			slog.WarnContext(ctx, "failed to scan package imports, skipping", "importPath", currentPkgImportPath, "error", err)
			return nil
		}
		for _, imp := range pkgImports.Imports {
			reverseDeps[imp] = append(reverseDeps[imp], currentPkgImportPath)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error walking module directory for reverse dependency map: %w", err)
	}

	// Sort for deterministic output
	for _, importers := range reverseDeps {
		sort.Strings(importers)
	}

	w.mu.Lock()
	w.reverseDepCache = reverseDeps
	w.mu.Unlock()

	return reverseDeps, nil
}

// Walk performs a dependency graph traversal starting from a set of root packages
// identified by the input patterns.
// It uses the efficient ScanPackageFromFilePathImports method to fetch dependencies at each step.
// The provided Visitor's Visit method is called for each discovered package,
// allowing the caller to inspect the package and control which of its dependencies
// are followed next.
// Patterns can include the `...` wildcard to specify all packages under a directory.
func (w *ModuleWalker) Walk(ctx context.Context, visitor Visitor, patterns ...string) error {
	initialQueue, err := w.resolvePatternsToImportPaths(ctx, patterns)
	if err != nil {
		return fmt.Errorf("could not resolve initial patterns for walk: %w", err)
	}

	queue := initialQueue
	visited := make(map[string]struct{})

	for len(queue) > 0 {
		currentImportPath := queue[0]
		queue = queue[1:]

		if _, ok := visited[currentImportPath]; ok {
			continue
		}
		visited[currentImportPath] = struct{}{}

		pkgImports, err := w.ScanPackageFromFilePathImports(ctx, currentImportPath)
		if err != nil {
			// For a visualization tool, it might be better to log and continue.
			// However, for a generic utility, failing fast is safer.
			return fmt.Errorf("error scanning imports for %s: %w", currentImportPath, err)
		}

		importsToFollow, err := visitor.Visit(pkgImports)
		if err != nil {
			return fmt.Errorf("visitor failed for package %s: %w", currentImportPath, err)
		}

		for _, imp := range importsToFollow {
			if _, ok := visited[imp]; !ok {
				queue = append(queue, imp)
			}
		}
	}
	return nil
}

// resolvePatternsToImportPaths resolves filesystem patterns (including `...` wildcards)
// into a list of unique, sorted Go import paths.
func (w *ModuleWalker) resolvePatternsToImportPaths(ctx context.Context, patterns []string) ([]string, error) {
	rootPaths := make(map[string]struct{})

	for _, pattern := range patterns {
		if strings.Contains(pattern, "...") {
			baseDir := strings.TrimSuffix(pattern, "...")
			baseDir = strings.TrimSuffix(baseDir, "/")

			absBasePath := baseDir
			if !filepath.IsAbs(baseDir) {
				absBasePath = filepath.Join(w.workDir, baseDir)
			}

			walkErr := filepath.WalkDir(absBasePath, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					return nil
				}
				// Check if the directory contains any .go files.
				ok, err := hasGoFiles(path)
				if err != nil {
					slog.DebugContext(ctx, "cannot check for go files, skipping", "path", path, "error", err)
					return nil
				}

				if ok {
					importPath, err := w.locator.PathToImport(path)
					if err != nil {
						slog.WarnContext(ctx, "could not resolve import path, skipping", "path", path, "error", err)
						return nil
					}
					rootPaths[importPath] = struct{}{}
				}
				return nil
			})
			if walkErr != nil {
				return nil, fmt.Errorf("error walking for pattern %q: %w", pattern, walkErr)
			}
		} else {
			// Assume non-wildcard patterns are literal import paths.
			rootPaths[pattern] = struct{}{}
		}
	}

	// Convert map to slice for the queue
	pathList := make([]string, 0, len(rootPaths))
	for path := range rootPaths {
		pathList = append(pathList, path)
	}
	sort.Strings(pathList) // Sort for deterministic walk start
	return pathList, nil
}

func hasGoFiles(dirPath string) (bool, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}

// listGoFilesForWalker lists all .go files in a directory.
// If includeTests is false, it excludes _test.go files.
// It returns a list of absolute file paths.
func listGoFilesForWalker(dirPath string, includeTests bool) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("listGoFiles: failed to read dir %s: %w", dirPath, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}

		absPath, err := filepath.Abs(filepath.Join(dirPath, name))
		if err != nil {
			return nil, fmt.Errorf("listGoFiles: could not get absolute path for %s: %w", name, err)
		}
		files = append(files, absPath)
	}
	return files, nil
}
