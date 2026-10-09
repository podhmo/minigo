package locator

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
)

// Overlay provides a way to replace the contents of a file with alternative
// content. Vendored from github.com/podhmo/go-scan/scanner (Overlay is
// map[string][]byte there; see pkg/SOURCE.md).
type Overlay map[string][]byte

// ReplaceDirective represents a single replace directive in a go.mod file.
type ReplaceDirective struct {
	OldPath    string
	OldVersion string // Empty if not specified
	NewPath    string
	NewVersion string // Empty if it's a local path or not specified
	IsLocal    bool
}

// Locator helps find the module root and resolve package import paths.
type Locator struct {
	modulePath string
	rootDir    string
	replaces   []ReplaceDirective
	overlay    Overlay

	// Options for advanced resolution
	UseGoModuleResolver bool
	goRoot              string
	goModCache          string
	requires            map[string]string // module path -> version
}

// Option is a functional option for configuring the Locator.
type Option func(*Locator)

// WithOverlay provides in-memory file content for go.mod.
func WithOverlay(overlay Overlay) Option {
	return func(l *Locator) {
		if l.overlay == nil {
			l.overlay = make(Overlay)
		}
		for k, v := range overlay {
			l.overlay[k] = v
		}
	}
}

// WithGoModuleResolver enables resolving packages from GOROOT and the module cache.
func WithGoModuleResolver() Option {
	return func(l *Locator) {
		l.UseGoModuleResolver = true
	}
}

// New creates a new Locator by searching for a go.mod file.
// It starts searching from startPath and moves up the directory tree.
func New(startPath string, options ...Option) (*Locator, error) {
	l := &Locator{
		requires: make(map[string]string),
	}
	for _, opt := range options {
		opt(l)
	}

	absPath, err := filepath.Abs(startPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", startPath, err)
	}

	rootDir, err := findModuleRoot(absPath)
	if err != nil {
		// If resolver is enabled, not finding a go.mod is not a fatal error
		// as we might be resolving stdlib packages.
		if !l.UseGoModuleResolver {
			return nil, err
		}
		// We can proceed without a module root, but some features will be limited.
		// Let's assign rootDir to startPath to have a reference point.
		rootDir = absPath
	}
	l.rootDir = rootDir

	var goModContent []byte
	if l.overlay != nil {
		if content, ok := l.overlay["go.mod"]; ok {
			goModContent = content
		}
	}

	if goModContent == nil && l.rootDir != "" {
		goModFilePath := filepath.Join(l.rootDir, "go.mod")
		// It's okay if go.mod doesn't exist, especially if UseGoModuleResolver is true
		if content, readErr := os.ReadFile(goModFilePath); readErr == nil {
			goModContent = content
		}
	}

	if len(goModContent) > 0 {
		modPath, err := getModulePathFromBytes(goModContent)
		if err != nil {
			return nil, fmt.Errorf("failed to get module path from go.mod content: %w", err)
		}
		l.modulePath = modPath

		replaces, err := getReplaceDirectivesFromBytes(goModContent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not parse replace directives in go.mod: %v\n", err)
		}
		l.replaces = replaces

		if l.UseGoModuleResolver {
			requires, err := getRequireDirectivesFromBytes(goModContent)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not parse require directives in go.mod: %v\n", err)
			}
			l.requires = requires
		}
	}

	if l.UseGoModuleResolver {
		goRoot, err := getGoRoot()
		if err != nil {
			return nil, fmt.Errorf("could not determine GOROOT: %w", err)
		}
		l.goRoot = goRoot
		cache, err := getGoModCache()
		if err != nil {
			return nil, fmt.Errorf("could not determine go mod cache location: %w", err)
		}
		l.goModCache = cache
	}

	return l, nil
}

// RootDir returns the project's root directory (where go.mod is located).
func (l *Locator) RootDir() string {
	return l.rootDir
}

// ModulePath returns the module path from go.mod.
func (l *Locator) ModulePath() string {
	return l.modulePath
}

// Requires returns the go.mod require directives as module path -> version.
// The map is a copy; it is empty when module resolution is off or no
// go.mod was found.
func (l *Locator) Requires() map[string]string {
	out := make(map[string]string, len(l.requires))
	for k, v := range l.requires {
		out[k] = v
	}
	return out
}

// FindPackageDir converts an import path to a physical directory path.
func (l *Locator) FindPackageDir(importPath string) (string, error) {
	return l.FindPackageDirFrom("", importPath)
}

// FindPackageDirFrom resolves importPath as imported by the package in
// fromDir. When the importer lives inside GOROOT/src, vendored
// dependencies are searched through the ancestor vendor directories
// first — the same rule the toolchain applies to standard-library code
// (e.g. net/http importing golang.org/x/net/http/httpguts resolves to
// $GOROOT/src/vendor/golang.org/x/net/http/httpguts). A fromDir outside
// GOROOT skips the vendor search entirely.
func (l *Locator) FindPackageDirFrom(fromDir, importPath string) (string, error) {
	// 0. Vendored dependencies of a GOROOT importer.
	if dir, ok := l.findInVendor(fromDir, importPath); ok {
		return dir, nil
	}

	return l.resolveImport(importPath, 0)
}

// claimKind ranks which module claims an import path when several module
// paths match it at the same length.
type claimKind int

const (
	claimNone claimKind = iota
	claimRequire
	claimReplace
	claimMain // a replace cannot shadow the main module
)

// matchModulePath reports whether importPath is mod or lives under it.
func matchModulePath(mod, importPath string) bool {
	return mod != "" && (importPath == mod || strings.HasPrefix(importPath, mod+"/"))
}

// resolveImport maps importPath to a directory through the module that
// claims it. Module paths claim imports by longest match — the rule the
// toolchain itself uses to pick an owning module. The claimants are the
// main module, each replace's old path, and each required module; ties
// break main > replace > require. Once claimed, resolution goes through
// that module alone: a missing directory is an error, never a silent
// read of another tree. (Before this, a replace on a path longer than —
// or unrelated to — the main module's own prefix could win resolution
// for imports the main module also matches, e.g. a self-replace of the
// parent module shadowing a nested module's own packages.)
func (l *Locator) resolveImport(importPath string, depth int) (string, error) {
	if depth > 8 {
		return "", fmt.Errorf("import path %q could not be resolved: replace directives form a cycle", importPath)
	}

	best := ""
	bestClaim := claimNone
	bestReplace := -1
	consider := func(mod string, kind claimKind, repIdx int) {
		if !matchModulePath(mod, importPath) {
			return
		}
		if len(mod) > len(best) || (len(mod) == len(best) && kind > bestClaim) {
			best, bestClaim, bestReplace = mod, kind, repIdx
		}
	}
	consider(l.modulePath, claimMain, -1)
	for i := range l.replaces {
		consider(l.replaces[i].OldPath, claimReplace, i)
	}
	for mod := range l.requires {
		consider(mod, claimRequire, -1)
	}

	switch bestClaim {
	case claimMain:
		relPath := strings.TrimPrefix(importPath, best)
		candidatePath := filepath.Join(l.rootDir, relPath)
		if stat, err := os.Stat(candidatePath); err == nil && stat.IsDir() {
			return candidatePath, nil
		}
		return "", fmt.Errorf("import path %q resolved inside module %q but the directory %s does not exist", importPath, best, candidatePath)

	case claimReplace:
		r := l.replaces[bestReplace]
		remainingPath := strings.TrimPrefix(strings.TrimPrefix(importPath, best), "/")
		if r.IsLocal {
			var localCandidatePath string
			if filepath.IsAbs(r.NewPath) {
				localCandidatePath = filepath.Join(r.NewPath, remainingPath)
			} else {
				localCandidatePath = filepath.Join(l.rootDir, r.NewPath, remainingPath)
			}
			absLocalCandidatePath, err := filepath.Abs(localCandidatePath)
			if err != nil {
				return "", fmt.Errorf("import path %q resolved through a broken replace directive: %w", importPath, err)
			}
			if stat, statErr := os.Stat(absLocalCandidatePath); statErr == nil && stat.IsDir() {
				return absLocalCandidatePath, nil
			}
			return "", fmt.Errorf("import path %q resolved through replace %q => %q but the directory %s does not exist", importPath, r.OldPath, r.NewPath, absLocalCandidatePath)
		}
		// Module-to-module replace: re-resolve the mapped path through
		// the same claim logic (the new path may land in this module, in
		// a required module's cache entry, or nowhere).
		newImportPath := r.NewPath
		if remainingPath != "" {
			newImportPath = r.NewPath + "/" + remainingPath
		}
		return l.resolveImport(newImportPath, depth+1)

	case claimRequire:
		if l.goModCache != "" {
			// Path in cache is ${GOMODCACHE}/${module}@${version}/${subpath}
			// Module paths with uppercase letters are encoded.
			if escapedMod, err := module.EscapePath(best); err == nil {
				baseDir := filepath.Join(l.goModCache, escapedMod+"@"+l.requires[best])
				remainingPath := strings.TrimPrefix(importPath, best)
				candidatePath := filepath.Join(baseDir, remainingPath)
				if stat, err := os.Stat(candidatePath); err == nil && stat.IsDir() {
					return candidatePath, nil
				}
				return "", fmt.Errorf("import path %q resolved inside module %q but the directory %s does not exist", importPath, best+"@"+l.requires[best], candidatePath)
			}
		}
	}

	// No module claimed the import — it may be standard library.
	if l.UseGoModuleResolver && l.goRoot != "" {
		candidatePath := filepath.Join(l.goRoot, "src", importPath)
		if stat, err := os.Stat(candidatePath); err == nil && stat.IsDir() {
			return candidatePath, nil
		}
	}

	if l.modulePath != "" {
		return "", fmt.Errorf("import path %q could not be resolved. Current module is %q (root: %s)", importPath, l.modulePath, l.rootDir)
	}
	return "", fmt.Errorf("import path %q could not be resolved", importPath)
}

// findInVendor searches vendor directories for importPath, walking from
// fromDir up to the source root — mirroring the toolchain's vendoring:
// each ancestor dir's vendor/ subtree is tried in order. The search only
// applies when the importer lives inside GOROOT/src, the one place
// vendored stdlib dependencies (golang.org/x/net, golang.org/x/crypto,
// ...) are resolvable. Module-level vendor/ trees are out of scope.
func (l *Locator) findInVendor(fromDir, importPath string) (string, bool) {
	if l.goRoot == "" || fromDir == "" {
		return "", false
	}
	srcRoot := filepath.Join(l.goRoot, "src")
	if !isWithinDir(srcRoot, fromDir) {
		return "", false
	}
	for d := fromDir; ; d = filepath.Dir(d) {
		candidate := filepath.Join(d, "vendor", filepath.FromSlash(importPath))
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate, true
		}
		if d == srcRoot {
			return "", false
		}
	}
}

// isWithinDir reports whether path is root itself or lives under it.
func isWithinDir(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// findModuleRoot searches for any go.mod starting from a given directory and moving upwards.
func findModuleRoot(dir string) (string, error) {
	currentDir := dir
	for {
		goModPath := filepath.Join(currentDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return currentDir, nil
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return "", fmt.Errorf("go.mod not found in or above %s", dir)
		}
		currentDir = parentDir
	}
}

// getModulePathFromBytes reads the module path from go.mod content.
func getModulePathFromBytes(content []byte) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("go.mod content is empty")
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "module") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				return parts[1], nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading go.mod content: %w", err)
	}

	return "", fmt.Errorf("module path not found in go.mod content")
}

// getReplaceDirectivesFromBytes reads replace directives from go.mod content.
func getReplaceDirectivesFromBytes(content []byte) ([]ReplaceDirective, error) {
	if len(content) == 0 {
		return nil, nil // No directives in empty file
	}
	var directives []ReplaceDirective
	scanner := bufio.NewScanner(bytes.NewReader(content))
	inReplaceBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "//") { // Skip comments
			continue
		}

		if line == "" { // Skip empty lines
			continue
		}

		if strings.HasPrefix(line, "replace") {
			if strings.Contains(line, "(") {
				inReplaceBlock = true
				line = strings.TrimSpace(strings.TrimPrefix(line, "replace"))
				line = strings.TrimSpace(strings.TrimPrefix(line, "("))
				// Process first line if it's not just "replace ("
				if line != "" {
					directive, err := parseReplaceLine(line)
					if err != nil {
						// TODO: Log or handle individual line parsing errors more gracefully
						// For now, skip malformed lines.
						fmt.Fprintf(os.Stderr, "warning: skipping malformed replace directive line: %q in go.mod: %v\n", line, err)
						continue
					}
					directives = append(directives, directive)
				}
				continue
			} else {
				// Single line replace
				contentParts := strings.Fields(line) // line is "replace old [v] => new [v]"
				if len(contentParts) < 1 {           // Should not happen if HasPrefix("replace") is true and line is trimmed
					continue
				}
				directiveLine := strings.Join(contentParts[1:], " ") // "old [v] => new [v]"

				// parseReplaceLine will check for "=>"
				directive, err := parseReplaceLine(directiveLine)
				if err != nil {
					// Log the original line for better context if parsing fails
					fmt.Fprintf(os.Stderr, "warning: skipping malformed single-line replace directive content: %q (from line: %q) in go.mod: %v\n", directiveLine, line, err)
					continue
				}
				directives = append(directives, directive)
				// No need for 'continue' here as it's the end of the 'if strings.HasPrefix(line, "replace")' block's else path
			}
		} else if inReplaceBlock { // Ensure this is 'else if' or structure appropriately
			if line == ")" {
				inReplaceBlock = false
				continue
			}
			directive, err := parseReplaceLine(line)
			if err != nil {
				// fmt.Fprintf(os.Stderr, "warning: skipping malformed replace directive line: %q in go.mod: %v\n", line, err)
				continue
			}
			directives = append(directives, directive)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading go.mod content: %w", err)
	}

	return directives, nil
}

// getGoRoot finds the GOROOT of the local toolchain by calling `go env GOROOT`.
func getGoRoot() (string, error) {
	cmd := exec.Command("go", "env", "GOROOT")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run 'go env GOROOT': %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getGoModCache finds the path to the module cache directory by calling `go env GOMODCACHE`.
func getGoModCache() (string, error) {
	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run 'go env GOMODCACHE': %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getRequireDirectivesFromBytes reads require directives from go.mod content.
func getRequireDirectivesFromBytes(content []byte) (map[string]string, error) {
	if len(content) == 0 {
		return nil, nil
	}
	requires := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	inRequireBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "//") {
			continue
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "require") {
			if strings.Contains(line, "(") {
				inRequireBlock = true
				// Potentially a require statement on the same line as `require (`
				line = strings.TrimSpace(strings.TrimPrefix(line, "require"))
				line = strings.TrimSpace(strings.TrimPrefix(line, "("))
			} else {
				// Single line require
				parts := strings.Fields(line)
				if len(parts) == 3 { // require <path> <version>
					requires[parts[1]] = parts[2]
				}
				continue
			}
		}

		if inRequireBlock {
			if line == ")" {
				inRequireBlock = false
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 { // <path> <version>
				// Handle potential // indirect comments
				version := parts[1]
				if len(parts) > 2 && parts[2] == "//" {
					// it's indirect, but we still record it
				}
				requires[parts[0]] = version
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading go.mod content for require directives: %w", err)
	}

	return requires, nil
}

// parseReplaceLine parses a single line of a replace directive.
// Example inputs:
// "old.module/path => new.module/path v1.2.3"
// "old.module/path v0.0.0 => new.module/path v1.2.3"
// "old.module/path => ./local/path"
// "old.module/path v1.0.0 => ./local/path"
func parseReplaceLine(line string) (ReplaceDirective, error) {
	parts := strings.Fields(line)
	arrowIndex := -1
	for i, p := range parts {
		if p == "=>" {
			arrowIndex = i
			break
		}
	}

	if arrowIndex == -1 || arrowIndex == 0 || arrowIndex == len(parts)-1 {
		return ReplaceDirective{}, fmt.Errorf("malformed replace directive line: %q (missing or misplaced '=>')", line)
	}

	var dir ReplaceDirective
	oldParts := parts[:arrowIndex]
	newParts := parts[arrowIndex+1:]

	dir.OldPath = oldParts[0]
	if len(oldParts) > 1 {
		dir.OldVersion = oldParts[1]
	}

	newPathOrModule := newParts[0]
	if strings.HasPrefix(newPathOrModule, "./") || strings.HasPrefix(newPathOrModule, "../") || filepath.IsAbs(newPathOrModule) {
		dir.IsLocal = true
		dir.NewPath = newPathOrModule
		if len(newParts) > 1 {
			// This case should ideally not happen for local paths as per go.mod spec,
			// but we'll capture it if present.
			// The go command itself might error on such go.mod.
			return ReplaceDirective{}, fmt.Errorf("local replacement path %q should not have a version: %q", dir.NewPath, line)
		}
	} else {
		dir.IsLocal = false
		dir.NewPath = newPathOrModule
		if len(newParts) > 1 {
			dir.NewVersion = newParts[1]
		} else {
			// If it's not local and no version is specified, it's an error according to go.mod replace spec
			// unless it's a wildcard replacement (oldpath => newpath vX.Y.Z),
			// but our parsing targets specific versions or local paths.
			// For "oldmodule => newmodule", a version is required for newmodule.
			return ReplaceDirective{}, fmt.Errorf("non-local replacement path %q requires a version: %q", dir.NewPath, line)
		}
	}

	return dir, nil
}

// PathToImport converts an absolute directory path to its corresponding Go import path.
// It considers the module's own path and any `replace` directives.
func (l *Locator) PathToImport(absPath string) (string, error) {
	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for %s: %w", absPath, err)
	}

	// 1. Check if it's inside the main module root.
	if strings.HasPrefix(absPath, l.rootDir) {
		relPath, err := filepath.Rel(l.rootDir, absPath)
		if err != nil {
			return "", fmt.Errorf("failed to get relative path for %s from root %s: %w", absPath, l.rootDir, err)
		}
		if relPath == "." {
			return l.modulePath, nil
		}
		return filepath.ToSlash(filepath.Join(l.modulePath, relPath)), nil
	}

	// 2. Check if it's inside a replaced local directory.
	for _, r := range l.replaces {
		if !r.IsLocal {
			continue
		}
		var replacedDirAbs string
		if filepath.IsAbs(r.NewPath) {
			replacedDirAbs = r.NewPath
		} else {
			replacedDirAbs = filepath.Join(l.rootDir, r.NewPath)
		}

		if strings.HasPrefix(absPath, replacedDirAbs) {
			relPath, err := filepath.Rel(replacedDirAbs, absPath)
			if err != nil {
				return "", fmt.Errorf("failed to get relative path for %s from replaced dir %s: %w", absPath, replacedDirAbs, err)
			}
			if relPath == "." {
				return r.OldPath, nil
			}
			return filepath.ToSlash(filepath.Join(r.OldPath, relPath)), nil
		}
	}

	return "", fmt.Errorf("could not determine import path for directory %s", absPath)
}

// ResolvePkgPath converts a file path to a full Go package path.
// If the path exists on the filesystem, it is treated as a file path and resolved.
// If it does not exist, it's assumed to be a package path unless it has a relative
// path prefix (like `./`), in which case it's an error.
func ResolvePkgPath(ctx context.Context, path string) (string, error) {
	isFilePathLike := strings.HasPrefix(path, ".") || filepath.IsAbs(path)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if isFilePathLike {
				// It looks like a file path but doesn't exist. This is an error.
				return "", fmt.Errorf("path %q does not exist: %w", path, err)
			}
			// It doesn't look like a file path and doesn't exist. Assume it's a package path.
			return path, nil
		}
		// Other stat error.
		return "", fmt.Errorf("error checking path %q: %w", path, err)
	}

	// Path exists, so it's a file/dir path.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("could not get absolute path for %q: %w", path, err)
	}

	searchDir := absPath
	if !info.IsDir() {
		searchDir = filepath.Dir(absPath)
	}

	modRoot, err := findModuleRoot(searchDir)
	if err != nil {
		// Re-wrap the error for more context.
		return "", fmt.Errorf("could not find go.mod for path %q: %w", path, err)
	}
	goModPath := filepath.Join(modRoot, "go.mod")

	modBytes, err := os.ReadFile(goModPath)
	if err != nil {
		return "", fmt.Errorf("could not read go.mod at %s: %w", goModPath, err)
	}

	modulePath, err := getModulePathFromBytes(modBytes)
	if err != nil {
		return "", fmt.Errorf("could not parse module path from %s: %w", goModPath, err)
	}

	// Use searchDir to get the relative path for the package, not the file.
	relPath, err := filepath.Rel(modRoot, searchDir)
	if err != nil {
		return "", fmt.Errorf("could not determine relative path of %s from %s: %w", searchDir, modRoot, err)
	}

	pkgPath := filepath.ToSlash(relPath)
	if pkgPath == "." {
		return modulePath, nil
	}
	return modulePath + "/" + pkgPath, nil
}
