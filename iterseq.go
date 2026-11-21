package minigo

import (
	"context"

	"github.com/podhmo/minigo/runtime"
)

type iterSeqKey struct{ a, b *runtime.TypeDef }

// iterSeqBaseKey keys the generic iter.Seq / iter.Seq2 typedefs in the
// same cache.
type iterSeqBaseKey string

func sharedTd(td *runtime.TypeDef) bool {
	return td != nil && td.Name != "" && len(td.Binds) == 0 && td.TParams == nil
}

// iterSeqTd instantiates iter.Seq[V] (one elem) or iter.Seq2[K, V] (two)
// over resolved element typedefs, so an intrinsic seq spells and infers
// like one a script returns: %T prints iter.Seq[string], a script
// `func f[T any](iter.Seq[T])` binds T from it, and slices.Sorted reads
// the element type back off it. nil when an element is unknown or iter
// does not load (a deny policy, no GOROOT) — the seq then stays an
// untyped callable.
func (e *Engine) iterSeqTd(elems ...*runtime.TypeDef) *runtime.TypeDef {
	key := iterSeqKey{}
	switch len(elems) {
	case 1:
		key.a = elems[0]
	case 2:
		key.a, key.b = elems[0], elems[1]
		if key.b == nil {
			return nil
		}
	default:
		return nil
	}
	if key.a == nil {
		return nil
	}
	cacheable := sharedTd(key.a) && (key.b == nil || sharedTd(key.b))
	if cacheable {
		if td, ok := e.iterSeqTds.Load(key); ok {
			return td.(*runtime.TypeDef)
		}
	}
	base := e.iterSeqBase(len(elems))
	if base == nil {
		return nil
	}
	binds := make(map[string]runtime.Value, len(elems))
	for i, tp := range base.TParams {
		binds[tp] = elems[i]
	}
	td := e.instantiateBinds(base, binds)
	if cacheable {
		if prev, loaded := e.iterSeqTds.LoadOrStore(key, td); loaded {
			return prev.(*runtime.TypeDef)
		}
	}
	return td
}

// iterSeqBase is the generic iter.Seq (n=1) or iter.Seq2 (n=2) typedef,
// loaded from the iter package once per engine; nil when it does not
// load.
func (e *Engine) iterSeqBase(n int) *runtime.TypeDef {
	name := "Seq"
	if n == 2 {
		name = "Seq2"
	}
	if td, ok := e.iterSeqTds.Load(iterSeqBaseKey(name)); ok {
		return td.(*runtime.TypeDef)
	}
	p, err := e.loadPath(context.Background(), "iter")
	if err != nil {
		return nil
	}
	m, err := p.Member(name, e.materialize)
	if err != nil {
		return nil
	}
	base, ok := m.(*runtime.TypeDef)
	if !ok || len(base.TParams) != n {
		return nil
	}
	e.iterSeqTds.Store(iterSeqBaseKey(name), base)
	return base
}

// iterSeqElems reads the element typedefs off an iter.Seq / iter.Seq2
// value (an intrinsic's or a script's): [V] or [K, V]; nil for anything
// that is not typed as one.

// typedSeq tags an intrinsic seq callable with its iter.Seq / Seq2 type
// when the element typedefs are known.
func (e *Engine) typedSeq(fn *runtime.BuiltinFunc, elems ...*runtime.TypeDef) runtime.Value {
	if td := e.iterSeqTd(elems...); td != nil {
		return runtime.Tag(td, fn)
	}
	return fn
}
