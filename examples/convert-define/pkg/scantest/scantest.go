package scantest

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	scan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/locator"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

// memoryFileWriter is an in-memory implementation of scan.FileWriter for testing.
type memoryFileWriter struct {
	mu      sync.Mutex
	Outputs map[string][]byte
	BaseDir string // The root directory for test files.
}

// WriteFile captures the output in memory instead of writing to disk.
func (w *memoryFileWriter) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Outputs == nil {
		w.Outputs = make(map[string][]byte)
	}

	relPath, err := filepath.Rel(w.BaseDir, path)
	if err != nil {
		// If the path is not relative to BaseDir, use the original path.
		// This might happen for unexpected write locations.
		relPath = path
	}

	w.Outputs[relPath] = data
	return nil
}

// ActionFunc is a function that performs a check or an action based on scan results.
// For actions with side effects, it should use go-scan's top-level functions
// (e.g., goscan.WriteFile) to allow the test harness to capture the results.
type ActionFunc func(ctx context.Context, s *scan.Scanner, pkgs []*scan.Package) error

// Result holds the outcome of a Run that has side effects.
type Result struct {
	// Outputs contains the content of newly created files written during the action.
	// The key is the file path, and the value is the content.
	Outputs map[string][]byte
	// Modified contains the content of existing files that were modified during the action.
	// The key is the file path, and the value is the new content.
	Modified map[string][]byte
}

// RunOption configures a test run.
type RunOption func(*runConfig)

type runConfig struct {
	moduleRoot string
	scanner    *scan.Scanner
}

// WithModuleRoot explicitly sets the module root directory for the test run.
// If this option is not used, the search behavior for `go.mod` is described in the `Run` function's documentation.
func WithModuleRoot(path string) RunOption {
	return func(c *runConfig) {
		c.moduleRoot = path
	}
}

// WithScanner provides a pre-configured scanner to the Run function.
// If this is provided, the Run function will not create its own scanner.
func WithScanner(s *scan.Scanner) RunOption {
	return func(c *runConfig) {
		c.scanner = s
	}
}

// Run sets up and executes a test scenario.
// It returns a Result object if the action had side effects captured by the harness.
//
// By default, `Run` determines the module root for the scanner by performing a two-phase search for `go.mod`:
//  1. It first searches from the temporary test directory (`dir`) upwards to the filesystem root.
//     This is useful if the test's file layout (created via `WriteFiles`) constitutes a self-contained module.
//  2. If not found, it searches from the current working directory (`os.Getwd()`) upwards.
//     This allows the scanner to resolve dependencies against the actual project's `go.mod` file.
//
// This default behavior can be overridden by using the `WithModuleRoot()` option to specify an explicit path.
func Run(t *testing.T, ctx context.Context, dir string, patterns []string, action ActionFunc, opts ...RunOption) (*Result, error) {
	t.Helper()

	if ctx == nil {
		ctx = context.Background()
	}

	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Capture initial state of the directory
	initialFiles, err := readFilesInDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scantest: failed to read initial files: %w", err)
	}

	workDir := cfg.moduleRoot
	if workDir == "" {
		// Phase 1: Search up from the temp test directory.
		foundRoot, err := findModuleRoot(dir)
		if err != nil {
			// Phase 2: If not found, search up from the current working directory.
			cwd, err_cwd := os.Getwd()
			if err_cwd != nil {
				return nil, fmt.Errorf("scantest: could not get current working directory: %w", err_cwd)
			}
			foundRoot, err = findModuleRoot(cwd)
			if err != nil {
				return nil, fmt.Errorf("scantest: failed to find go.mod root from temp dir (%s) or cwd (%s)", dir, cwd)
			}
		}
		workDir = foundRoot
	}

	var s *scan.Scanner
	if cfg.scanner != nil {
		s = cfg.scanner
	} else {
		// Automatically handle go.mod replace directives with relative paths.
		overlay, err := createGoModOverlay(workDir)
		if err != nil {
			return nil, fmt.Errorf("creating go.mod overlay: %w", err)
		}

		options := []scan.ScannerOption{
			scan.WithWorkDir(workDir),
			scan.WithGoModuleResolver(), // Automatically enable module resolution.
		}
		if overlay != nil {
			options = append(options, scan.WithOverlay(overlay))
		}

		s, err = scan.New(options...)
		if err != nil {
			return nil, fmt.Errorf("new scanner: %w", err)
		}
	}

	// Distinguish between file paths and import paths.
	processedPatterns := make([]string, len(patterns))
	for i, p := range patterns {
		// Check if the pattern corresponds to an existing file/dir in the test directory.
		// This is more robust than just checking for a "." prefix.
		candidatePath := p
		if !filepath.IsAbs(p) {
			candidatePath = filepath.Join(dir, p)
		}

		if _, err := os.Stat(candidatePath); err == nil {
			// It's a file path, use the absolute path.
			processedPatterns[i] = candidatePath
		} else {
			// It's (presumably) an import path, pass it through as is.
			processedPatterns[i] = p
		}
	}

	pkgs, err := s.Scan(ctx, processedPatterns...)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// The memoryFileWriter is still useful for tools that expect the context writer,
	// but the primary source of truth for file changes will be the directory snapshot.
	writer := &memoryFileWriter{BaseDir: dir}
	actionCtx := context.WithValue(ctx, scan.FileWriterKey, writer)

	if err := action(actionCtx, s, pkgs); err != nil {
		return nil, fmt.Errorf("action: %w", err)
	}

	// Capture final state and compare
	finalFiles, err := readFilesInDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scantest: failed to read final files: %w", err)
	}

	result := &Result{
		Outputs:  make(map[string][]byte),
		Modified: make(map[string][]byte),
	}
	hasChanges := false

	for path, finalContent := range finalFiles {
		initialContent, ok := initialFiles[path]
		if !ok {
			// New file
			result.Outputs[path] = finalContent
			hasChanges = true
		} else if !bytes.Equal(initialContent, finalContent) {
			// Modified file
			result.Modified[path] = finalContent
			hasChanges = true
		}
	}

	// Also account for files created via the in-memory writer that might not be
	// reflected in the final directory scan if the action func doesn't write to disk.
	for path, content := range writer.Outputs {
		if _, existsInFinal := finalFiles[path]; !existsInFinal {
			if _, existsInInitial := initialFiles[path]; !existsInInitial {
				result.Outputs[path] = content
				hasChanges = true
			}
		}
	}

	if !hasChanges {
		return nil, nil
	}

	return result, nil
}

// WriteFiles creates a temporary directory and populates it with initial files.
func WriteFiles(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	dir := t.TempDir()

	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	return dir, func() { /* t.TempDir handles cleanup */ }
}

// readFilesInDir walks the given directory and reads all files, returning a map
// where keys are file paths relative to the root, and values are their content.
func readFilesInDir(root string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path for %s: %w", path, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", path, err)
			}
			files[relPath] = data
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// findModuleRoot searches for a go.mod file starting from startDir and walking
// up the directory tree. It returns the directory containing the go.mod file.
func findModuleRoot(startDir string) (string, error) {
	dir := startDir
	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir { // Reached the root directory
			return "", fmt.Errorf("go.mod not found in or above %s", startDir)
		}
		dir = parent
	}
}

// RunCommand executes a command in the specified directory and fails the test if it fails.
func RunCommand(t *testing.T, dir string, name string, arg ...string) {
	t.Helper()
	cmd := exec.Command(name, arg...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %q failed in %s: %v\noutput:\n%s", strings.Join(append([]string{name}, arg...), " "), dir, err, string(output))
	}
}

// createGoModOverlay reads the go.mod file in the given directory,
// and if it contains `replace` directives with relative file paths,
// it creates an in-memory overlay with those paths converted to absolute paths.
// This is necessary because the `go-scan` tool resolves paths relative
// to the module root, and in tests, the temp directory is not the CWD.
func createGoModOverlay(dir string) (scanner.Overlay, error) {
	goModPath := filepath.Join(dir, "go.mod")
	_, err := os.Stat(goModPath)
	if os.IsNotExist(err) {
		return nil, nil // No go.mod, no overlay needed.
	}
	if err != nil {
		return nil, fmt.Errorf("stat go.mod: %w", err)
	}

	content, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	var modified bool

	inReplaceBlock := false
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		isReplaceLine := strings.HasPrefix(trimmedLine, "replace")
		if !inReplaceBlock && isReplaceLine && strings.HasSuffix(trimmedLine, "(") {
			inReplaceBlock = true
		} else if inReplaceBlock && trimmedLine == ")" {
			inReplaceBlock = false
		}

		if isReplaceLine || inReplaceBlock {
			parts := strings.Fields(trimmedLine)
			// A valid replace line looks like: "replace module/path => ../local/path"
			// or inside a block: "module/path => ../local/path"
			arrowIndex := -1
			for i, p := range parts {
				if p == "=>" {
					arrowIndex = i
					break
				}
			}

			if arrowIndex != -1 && arrowIndex < len(parts)-1 {
				pathPart := parts[len(parts)-1]
				if strings.HasPrefix(pathPart, "./") || strings.HasPrefix(pathPart, "../") {
					// This is a relative path, resolve it against `dir`
					absPath, err := filepath.Abs(filepath.Join(dir, pathPart))
					if err != nil {
						return nil, fmt.Errorf("could not make path absolute for %q: %w", pathPart, err)
					}
					// Reconstruct the line with the absolute path
					parts[len(parts)-1] = absPath
					// Get the original line's indentation
					indent := ""
					if len(line) > len(trimmedLine) {
						indent = line[:strings.Index(line, trimmedLine)]
					}
					line = indent + strings.Join(parts, " ")
					modified = true
				}
			}
		}
		newLines = append(newLines, line)
	}

	if !modified {
		return nil, nil // No changes, no overlay needed.
	}

	overlay := scanner.Overlay{
		"go.mod": []byte(strings.Join(newLines, "\n")),
	}
	return overlay, nil
}

// ToImportPath is a helper function that converts a filesystem path
// (relative or absolute) to its corresponding Go import path.
// This is a convenience wrapper around `locator.ResolvePkgPath` for use in tests.
func ToImportPath(path string) (string, error) {
	return locator.ResolvePkgPath(context.Background(), path)
}
