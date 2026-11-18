package runtime

import (
	"go/token"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// PackageImage is a source-built package's immutable definition (M5-1):
// the parsed files, their declaration index, the FileSet their
// positions resolve in, and the absolute-path file table — whose
// `syntax.File.Imports` are the per-file import *specs* (path/alias
// text). Once published it is shareable across Sessions.
//
// It must never carry execution state: Globals cells, init results,
// runtime ImportRefs (their sync.Once caches a per-Session
// resolution), materialized values, or host closures. Those belong to
// the instance that owns the Session ([Package]); internal/archcheck's
// shared-fields check guards the boundary.
//
// Identity (Path/Name/Dir/Standard) stays on [Package]: pre-1.27 Go
// cannot write promoted fields in struct literals, and the intrinsic
// stubs construct Packages that way.
type PackageImage struct {
	Fset  *token.FileSet
	Files []*syntax.File
	Index *index.Index

	// FileByName maps an absolute source path to its file — used to
	// recover per-declaration file context from instruction positions
	// (the synthetic __init__ chunk mixes decls from several files).
	FileByName map[string]*syntax.File
}
