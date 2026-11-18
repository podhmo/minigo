package source

import (
	"go/ast"
	"sync"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
)

// IndexSource is a semantics.Source over engine-level index access:
// Indexed peeks at already-indexed packages and Load builds one (locate,
// parse, index — never initialization). The callbacks keep the engine's
// loader out of this package while semantics sees only the index view.
type IndexSource struct {
	IndexedFunc func(path string) *index.Index
	LoadFunc    func(path string) (*index.Index, error)
}

var _ semantics.Source = (*IndexSource)(nil)

func (s *IndexSource) Indexed(path string) *index.Index {
	if s.IndexedFunc == nil {
		return nil
	}
	return s.IndexedFunc(path)
}

func (s *IndexSource) Load(path string) (*index.Index, error) {
	if s.LoadFunc == nil {
		return nil, nil
	}
	return s.LoadFunc(path)
}

// FieldPathKind mirrors semantics.ResultKind without naming it — the
// engine ("." may not import internal/semantics) maps answers through
// this neutral enum.
type FieldPathKind int

const (
	// FieldPathFound: Index holds the unique winning field path
	// (reflect's Field.Index convention).
	FieldPathFound FieldPathKind = iota
	// FieldPathMissing: the name is no field anywhere in the declared
	// shape — decided, a member search may still proceed.
	FieldPathMissing
	// FieldPathDeferred: no static answer yet — the caller should keep
	// its own search.
	FieldPathDeferred
	// FieldPathFailed: the query failed; Err carries the cause.
	FieldPathFailed
)

// FieldPathAnswer is the engine-facing shape of a FieldPathOf result.
type FieldPathAnswer struct {
	Kind  FieldPathKind
	Index []int
	Err   error
}

// FieldPaths answers selector field paths for runtime typedefs — the
// runtime.TypeDef -> semantics.TypeRef bridge.
type FieldPaths struct {
	svc   *semantics.Service
	cache sync.Map // fieldPathKey -> FieldPathAnswer (M4-1d)
}

// fieldPathKey is the memo key for a decided answer: the caller passes
// the typedef's identity (its TypeID) and the selector name.
type fieldPathKey struct {
	id   string
	name string
}

// NewFieldPaths builds the adapter over src.
func NewFieldPaths(src semantics.Source) *FieldPaths {
	return &FieldPaths{svc: semantics.New(src)}
}

// Of answers where the field name lands inside td's declared struct
// shape — uncached; OfKeyed is the memoizing form.
func (fp *FieldPaths) Of(td *runtime.TypeDef, name string) FieldPathAnswer {
	return fp.OfKeyed(td, name, "")
}

// OfKeyed is Of with an identity key for memoization (M4-1d): a decided
// answer (Found or Missing) is stored under (key, name) so a selector
// re-querying the same type never re-explores the declaration graph.
// The caller computes the key — the typedef's TypeID, which carries the
// source version — so a re-parsed declaration keys differently and a
// stale path can never hit. Deferred and Failed answers are never
// stored, and an empty key means uncached. Function-local typedefs are
// invisible to package indexes, so a td carrying LocalTypes reports
// Deferred — the value walk owns those. Loading errors and ambiguous
// selectors come back Failed.
func (fp *FieldPaths) OfKeyed(td *runtime.TypeDef, name, key string) FieldPathAnswer {
	cacheable := key != "" && fp != nil
	if cacheable {
		if v, ok := fp.cache.Load(fieldPathKey{id: key, name: name}); ok {
			return v.(FieldPathAnswer)
		}
	}
	ans := fp.of(td, name)
	if cacheable && (ans.Kind == FieldPathFound || ans.Kind == FieldPathMissing) {
		// the answer is handed out by reference — copy the path so a
		// caller mutating its slice cannot corrupt the memo.
		cp := ans
		cp.Index = append([]int(nil), ans.Index...)
		fp.cache.Store(fieldPathKey{id: key, name: name}, cp)
		return FieldPathAnswer{Kind: ans.Kind, Index: append([]int(nil), ans.Index...), Err: ans.Err}
	}
	return ans
}

// of is the uncached answer body Of/OfKeyed share.
func (fp *FieldPaths) of(td *runtime.TypeDef, name string) FieldPathAnswer {
	if td == nil || fp == nil || fp.svc == nil {
		return FieldPathAnswer{Kind: FieldPathDeferred}
	}
	expr := td.TypeExpr()
	if expr == nil || len(td.LocalTypes) > 0 {
		return FieldPathAnswer{Kind: FieldPathDeferred}
	}
	var binds map[string]ast.Expr
	if len(td.Binds) > 0 {
		// only the bound names matter — the values are runtime
		// typedefs the declaration view never inspects.
		binds = make(map[string]ast.Expr, len(td.Binds))
		for k := range td.Binds {
			binds[k] = ast.NewIdent(k)
		}
	}
	var pkg string
	if td.Pkg != nil {
		pkg = td.Pkg.Path
	}
	res := fp.svc.FieldPathOf(
		semantics.TypeRef{
			Expr: expr,
			Ctx:  semantics.DeclContext{Pkg: pkg, File: td.File, Binds: binds},
		},
		name,
		semantics.AllowIndex,
	)
	switch res.Kind {
	case semantics.Found:
		return FieldPathAnswer{Kind: FieldPathFound, Index: res.Val.Index}
	case semantics.Missing:
		return FieldPathAnswer{Kind: FieldPathMissing}
	case semantics.Deferred:
		return FieldPathAnswer{Kind: FieldPathDeferred}
	default:
		return FieldPathAnswer{Kind: FieldPathFailed, Err: res.Err}
	}
}
