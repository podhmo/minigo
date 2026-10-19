package gentest

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The FileWriter seam is modeled on goscan.FileWriter (go-scan writer.go;
// see pkg/SOURCE.md).

// FileWriter receives the writes [WriteFile] makes with a context from
// [WithFileWriter], instead of the filesystem. [MemoryFileWriter] and
// [FileWriterFunc] implement it.
type FileWriter interface {
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
}

// FileWriterFunc adapts a plain function to [FileWriter] — useful for
// failure injection (return an error for a chosen path) without
// declaring a writer type.
type FileWriterFunc func(ctx context.Context, path string, data []byte, perm fs.FileMode) error

// WriteFile implements FileWriter.
func (f FileWriterFunc) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	return f(ctx, path, data, perm)
}

// fileWriterKey is the context key WithFileWriter installs under.
type fileWriterKey struct{}

// WithFileWriter returns a context under which [WriteFile] calls go to w.
func WithFileWriter(ctx context.Context, w FileWriter) context.Context {
	return context.WithValue(ctx, fileWriterKey{}, w)
}

// WriteFile is os.WriteFile unless ctx carries a [FileWriter] from
// [WithFileWriter]; then the write goes to that writer instead of disk,
// and a [Run] over the same directory does not see it.
func WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if w, ok := ctx.Value(fileWriterKey{}).(FileWriter); ok {
		return w.WriteFile(ctx, path, data, perm)
	}
	return os.WriteFile(path, data, perm)
}

// MemoryFileWriter is a [FileWriter] that captures writes in Outputs,
// keyed by path relative to BaseDir; when BaseDir is empty or the path
// cannot be made relative to it, the path is kept as given. It is safe
// for concurrent use; a later write to a path replaces the earlier one.
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
