// Package source adapts the runtime loader's import refs into the
// semantics.Source view. Its method names carry the stage they reach —
// IndexAtIndexed and LoadToIndexed — so nothing on this side can reach
// package initialization (Package.EnsureReady and friends are simply
// not on the adapter). archcheck pins the import direction:
// internal/source may see runtime and internal/semantics; semantics
// sees neither.
package source

import (
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
)

// View is a semantics.Source over the engine's import refs. RefsFor
// resolves an import path to its ref (the engine's per-file import
// table); a missing ref answers as "no view of that package".
type View struct {
	refsFor func(path string) (*runtime.ImportRef, bool)
}

var _ semantics.Source = (*View)(nil)

// New builds a Source reading import refs through refsFor.
func New(refsFor func(path string) (*runtime.ImportRef, bool)) *View {
	return &View{refsFor: refsFor}
}

// Indexed returns the package's index when the import has already been
// materialized to the Indexed stage — never triggering a load.
func (v *View) Indexed(path string) *index.Index {
	ref, ok := v.refsFor(path)
	if !ok {
		return nil
	}
	p, ok := ref.Peek()
	if !ok {
		return nil
	}
	return p.Index
}

// Load materializes the import up to the Indexed stage and returns its
// index. Callers gate this behind semantics.AllowIndex; initialization
// is not reachable from here.
func (v *View) Load(path string) (*index.Index, error) {
	ref, ok := v.refsFor(path)
	if !ok {
		return nil, nil
	}
	p, err := ref.Materialize()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	p.WaitIndexed()
	return p.Index, nil
}
