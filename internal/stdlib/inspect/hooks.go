package stdinspect

import (
	"context"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// Hooks carries the engine services the inspect views call back into:
// package loading (strict and lenient), declaration materialization,
// and the bound/loaded package lookups. The engine fills it once in
// the install closure.
type Hooks struct {
	LoadPath        func(ctx context.Context, path string) (*runtime.Package, error)
	LoadPathLenient func(ctx context.Context, path string) (*runtime.Package, error)
	LoadDirLenient  func(ctx context.Context, dir string) (*runtime.Package, error)
	LoadFileLenient func(ctx context.Context, filename string) (*runtime.Package, error)
	FSPath          func(p string) (string, error)
	Materialize     func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)
	BoundOf         func(path string) *runtime.Package
	SourceOf        func(ctx context.Context, path string) (*runtime.Package, error)
}
