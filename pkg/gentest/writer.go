package gentest

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileWriter is the seam between a generator and the filesystem: host
// code routes its output writes through WriteFile, and a test installs
// a FileWriter on the context to capture them instead of touching disk.
// Modeled on goscan.FileWriter (go-scan writer.go; see pkg/SOURCE.md).
type FileWriter interface {
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
}

// FileWriterFunc adapts a plain function to FileWriter — useful for
// failure injection (return an error for a chosen path) without
// declaring a writer type.
type FileWriterFunc func(ctx context.Context, path string, data []byte, perm fs.FileMode) error

// WriteFile implements FileWriter.
func (f FileWriterFunc) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	return f(ctx, path, data, perm)
}

// fileWriterKey is the context key WithFileWriter installs under.
type fileWriterKey struct{}

// WithFileWriter returns a context whose WriteFile calls dispatch to w.
func WithFileWriter(ctx context.Context, w FileWriter) context.Context {
	return context.WithValue(ctx, fileWriterKey{}, w)
}

// WriteFile writes data to path with perm. When the context carries a
// FileWriter the write goes to it instead of disk — that is the point:
// captured writes never reach the filesystem, so a Run's Result (a real
// filesystem diff) stays empty and the capture lives in the writer.
// With no FileWriter installed it is os.WriteFile unchanged.
func WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if w, ok := ctx.Value(fileWriterKey{}).(FileWriter); ok {
		return w.WriteFile(ctx, path, data, perm)
	}
	return os.WriteFile(path, data, perm)
}

// MemoryFileWriter captures writes in memory, keyed by path relative to
// BaseDir. Paths outside BaseDir keep their original form.
type MemoryFileWriter struct {
	BaseDir string
	Outputs map[string][]byte
	mu      sync.Mutex
}

// WriteFile implements FileWriter.
func (w *MemoryFileWriter) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Outputs == nil {
		w.Outputs = make(map[string][]byte)
	}
	key := path
	if w.BaseDir != "" {
		if rel, err := filepath.Rel(w.BaseDir, path); err == nil {
			key = rel
		}
	}
	w.Outputs[key] = data
	return nil
}
