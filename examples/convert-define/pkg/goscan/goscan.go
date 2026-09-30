// Package goscan is a slimmed-down vendored copy of go-scan's top-level
// package. It keeps only the pieces convert-define uses: constructing a
// Scanner over a module directory, scanning a package by import path, and
// resolving field types on demand.
package goscan

import (
	"context"
	"fmt"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/podhmo/minigo/examples/convert-define/pkg/locator"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

// Scanner is the main entry point for the type scanning library.
// It combines a locator for finding packages and an internal scanner for
// parsing them. Scanned packages are memoized per Scanner instance so
// repeated resolutions share the same *scanner.PackageInfo.
type Scanner struct {
	fset                  *token.FileSet
	workDir               string
	useGoModuleResolver   bool
	locator               *locator.Locator
	scanner               *scanner.Scanner
	ExternalTypeOverrides scanner.ExternalTypeOverride

	mu           sync.RWMutex
	packageCache map[string]*scanner.PackageInfo // key is import path
	visitedFiles map[string]struct{}             // parsed file absolute paths
}

// ScannerOption is a function that configures a Scanner.
type ScannerOption func(*Scanner) error

// WithWorkDir sets the working directory for the scanner.
func WithWorkDir(path string) ScannerOption {
	return func(s *Scanner) error {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("getting absolute path for workdir %q: %w", path, err)
		}
		s.workDir = absPath
		return nil
	}
}

// WithGoModuleResolver enables the scanner to find packages in the Go module cache and GOROOT.
func WithGoModuleResolver() ScannerOption {
	return func(s *Scanner) error {
		s.useGoModuleResolver = true
		return nil
	}
}

// WithExternalTypeOverrides sets the external type override map for the scanner.
func WithExternalTypeOverrides(overrides scanner.ExternalTypeOverride) ScannerOption {
	return func(s *Scanner) error {
		if s.ExternalTypeOverrides == nil {
			s.ExternalTypeOverrides = make(scanner.ExternalTypeOverride)
		}
		for k, v := range overrides {
			s.ExternalTypeOverrides[k] = v
		}
		return nil
	}
}

// New creates a new Scanner. It finds the module root starting from the given path.
func New(options ...ScannerOption) (*Scanner, error) {
	s := &Scanner{
		fset:                  token.NewFileSet(),
		packageCache:          make(map[string]*scanner.PackageInfo),
		visitedFiles:          make(map[string]struct{}),
		ExternalTypeOverrides: make(scanner.ExternalTypeOverride),
	}

	for _, option := range options {
		if err := option(s); err != nil {
			return nil, err
		}
	}

	if s.workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
		s.workDir = cwd
	}

	var locatorOpts []locator.Option
	if s.useGoModuleResolver {
		locatorOpts = append(locatorOpts, locator.WithGoModuleResolver())
	}
	loc, err := locator.New(s.workDir, locatorOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize locator: %w", err)
	}
	s.locator = loc

	internalScanner, err := scanner.New(s.fset, s.ExternalTypeOverrides, loc.ModulePath(), loc.RootDir(), s)
	if err != nil {
		return nil, fmt.Errorf("failed to create internal scanner: %w", err)
	}
	s.scanner = internalScanner

	return s, nil
}

// ScanPackageFromImportPath scans a single Go package identified by its import path.
func (s *Scanner) ScanPackageFromImportPath(ctx context.Context, importPath string) (*scanner.PackageInfo, error) {
	pkgDirAbs, err := s.locator.FindPackageDir(importPath)
	if err != nil {
		return nil, fmt.Errorf("could not find directory for import path %s: %w", importPath, err)
	}
	return s.privateScan(ctx, pkgDirAbs, importPath)
}

// ResolveType starts the type resolution process for a given field type.
func (s *Scanner) ResolveType(ctx context.Context, fieldType *scanner.FieldType) (*scanner.TypeInfo, error) {
	if s.scanner == nil {
		return nil, fmt.Errorf("internal scanner is not initialized")
	}
	return s.scanner.ResolveType(ctx, fieldType)
}

// privateScan is the core scanning logic. The caller resolves the absolute
// directory path and the canonical import path; this memoizes by import path
// and parses each file at most once per Scanner instance.
func (s *Scanner) privateScan(ctx context.Context, pkgDirAbs string, importPath string) (*scanner.PackageInfo, error) {
	s.mu.RLock()
	cachedPkg, found := s.packageCache[importPath]
	s.mu.RUnlock()
	if found {
		return cachedPkg, nil
	}

	allGoFilesInPkg, err := listGoFiles(pkgDirAbs)
	if err != nil {
		return nil, fmt.Errorf("privateScan: failed to list go files in %s: %w", pkgDirAbs, err)
	}

	if len(allGoFilesInPkg) == 0 {
		pkgInfo := &scanner.PackageInfo{
			ID:         importPath,
			Path:       pkgDirAbs,
			ImportPath: importPath,
		}
		s.mu.Lock()
		s.packageCache[importPath] = pkgInfo
		s.mu.Unlock()
		return pkgInfo, nil
	}

	// Filter out files that have already been visited by this scanner instance.
	var filesToParseThisCall []string
	s.mu.RLock()
	for _, fp := range allGoFilesInPkg {
		if _, visited := s.visitedFiles[fp]; !visited {
			filesToParseThisCall = append(filesToParseThisCall, fp)
		}
	}
	s.mu.RUnlock()

	var pkgInfo *scanner.PackageInfo
	if len(filesToParseThisCall) > 0 {
		if strings.HasPrefix(pkgDirAbs, s.locator.RootDir()) {
			pkgInfo, err = s.scanner.ScanFiles(ctx, filesToParseThisCall, pkgDirAbs)
		} else {
			// Packages outside the module root (GOROOT, module cache,
			// replaced local modules) cannot derive their import path
			// from the module root, so it is passed in explicitly.
			pkgInfo, err = s.scanner.ScanFilesWithKnownImportPath(ctx, filesToParseThisCall, pkgDirAbs, importPath)
		}
		if err != nil {
			return nil, fmt.Errorf("privateScan: scanning files for %s failed: %w", importPath, err)
		}

		if pkgInfo != nil {
			s.mu.Lock()
			for _, fp := range pkgInfo.Files {
				s.visitedFiles[fp] = struct{}{}
			}
			s.mu.Unlock()
		}
	}

	if pkgInfo == nil {
		pkgInfo = &scanner.PackageInfo{
			Path:       pkgDirAbs,
			ImportPath: importPath,
		}
	}

	pkgInfo.ImportPath = importPath
	pkgInfo.Path = pkgDirAbs
	if pkgInfo.Name == "main" {
		pkgInfo.ID = importPath + ".main"
	} else {
		pkgInfo.ID = importPath
	}

	s.mu.Lock()
	s.packageCache[importPath] = pkgInfo
	s.mu.Unlock()

	slog.DebugContext(ctx, "privateScan finished", slog.String("importPath", importPath), slog.String("id", pkgInfo.ID))
	return pkgInfo, nil
}

// listGoFiles lists all non-test .go files in a directory as absolute paths.
func listGoFiles(dirPath string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("listGoFiles: failed to read dir %s: %w", dirPath, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
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
