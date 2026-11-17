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

// PackageAtIndexed answers the package ref materializes to, at the
// Indexed stage. It is the adapter the compiler's fact sites call
// instead of ImportRef.Materialize directly: IndexedOnly only peeks
// (Deferred when nothing is materialized yet), AllowIndex loads.
// Found carries the indexed package, Deferred means no package is
// indexed (not an error), Failed carries the load error.
func PackageAtIndexed(ref *runtime.ImportRef, pol semantics.Policy) semantics.Result[*runtime.Package] {
	if ref == nil {
		return semantics.Result[*runtime.Package]{Kind: semantics.Deferred}
	}
	p, ok := ref.Peek()
	if !ok {
		if pol == semantics.IndexedOnly {
			return semantics.Result[*runtime.Package]{Kind: semantics.Deferred}
		}
		var err error
		p, err = ref.Materialize()
		if err != nil {
			return semantics.Result[*runtime.Package]{Kind: semantics.Failed, Err: err}
		}
	}
	if p == nil {
		return semantics.Result[*runtime.Package]{Kind: semantics.Deferred}
	}
	// A package answered mid-indexing (import cycle) is not yet
	// "indexed": under IndexedOnly that is Deferred, while AllowIndex
	// waits for FinishIndexing since it asked for the finished answer.
	if !p.IndexReady() {
		if pol == semantics.IndexedOnly {
			return semantics.Result[*runtime.Package]{Kind: semantics.Deferred}
		}
		p.WaitIndexed()
	}
	return semantics.Result[*runtime.Package]{Kind: semantics.Found, Val: p}
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
	// Materialize returning only means Load returned — a package
	// answered mid-indexing (import cycle) is still being built, so it
	// is not "already indexed": report it absent rather than wait, so
	// callers under IndexedOnly correctly see Deferred.
	if !p.IndexReady() {
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
