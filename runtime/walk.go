package runtime

// Walk carries the guards one deep value-to-host walk shares: seen
// folds a container back to the output already being built for it — a
// cyclic or shared subcontainer resolves to the partial result — and
// peeled stops a reference chain that loops back on itself (a cell
// handed its own referrer). The maps allocate lazily: a scalar-only
// walk never pays for them. DeepUnwrap, the vm package's host
// marshal, and the root package's fmt-bound walk all run their
// container arms through it; each keeps its own leaf contract
// (structs, references, constants stay caller-side).
type Walk struct {
	seen   map[Value]any
	peeled map[Value]struct{}
}

// NewWalk starts a walk's shared guards.
func NewWalk() *Walk { return &Walk{} }

// Container walks the container arms shared by the deep-value walks:
// a *Slice becomes []any (a virtual slice expands its zero fill to
// the logical length) and a *Map becomes map[any]any — each
// registered in seen before its elements are produced by `each`, the
// caller's own recurse, so a repeat visit folds to the partially
// built container. Map keys go through `key` — UnwrapKey keeps them
// comparable where a full-walk key would panic hashing an expanded
// []any. The bool reports whether v was a container.
func (w *Walk) Container(v Value, each func(Value) any, key func(Value) any) (any, bool) {
	switch x := v.(type) {
	case *Slice:
		if old, ok := w.Seen(x); ok {
			return old, true
		}
		n := int(x.Len())
		xs := make([]any, n)
		w.Register(x, xs)
		if x.Virtual() {
			zero := each(x.Zero)
			for i := range xs {
				xs[i] = zero
			}
			return xs, true
		}
		for i, e := range x.Elems {
			xs[i] = each(e)
		}
		return xs, true
	case *Map:
		if old, ok := w.Seen(x); ok {
			return old, true
		}
		m := make(map[any]any, x.Len())
		w.Register(x, m)
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			m[key(k)] = each(e)
		}
		return m, true
	}
	return nil, false
}

// Seen reports the output already registered for v by a previous
// visit — the container fold itself, for shapes Container does not
// cover (a caller's own projection type).
func (w *Walk) Seen(v Value) (any, bool) {
	if w.seen == nil {
		return nil, false
	}
	old, ok := w.seen[v]
	return old, ok
}

// Register enters v's output in seen before its elements are walked,
// so a repeat visit folds to it.
func (w *Walk) Register(v Value, out any) {
	if w.seen == nil {
		w.seen = map[Value]any{}
	}
	w.seen[v] = out
}

// Peel marks a reference value as peeled once: the first call answers
// true, a repeat answers false — the caller stops the chain there.
func (w *Walk) Peel(v Value) bool {
	if w.peeled == nil {
		w.peeled = map[Value]struct{}{}
	}
	if _, dup := w.peeled[v]; dup {
		return false
	}
	w.peeled[v] = struct{}{}
	return true
}

// UnwrapKey unwraps a map key through only the layers that keep the
// result comparable — Named/ref peels, GoValue's host value, UConst's
// native scalar — and leaves script containers boxed. A script array
// key ([1]int{1}) survives as its *Slice box: pointer-comparable,
// where expanding it to []any panics on "hash of unhashable type" —
// the host answer stays a Go map that mirrors the script one's
// cardinality.
func UnwrapKey(v Value) any {
	peeled := map[Value]struct{}{}
	for {
		switch x := v.(type) {
		case *Named:
			v = x.V
			continue
		case *Cell, *FieldRef, *IndexRef, *DerefRef:
			if _, dup := peeled[v]; dup {
				return v
			}
			peeled[v] = struct{}{}
			d, ok := Deref(v)
			if !ok {
				return v
			}
			v = d
		case *GoValue:
			return x.V
		case *UConst:
			if nv, err := UConstNative(x); err == nil {
				return nv
			}
			return x
		}
		break
	}
	switch v.(type) {
	case nil, Nil, *IfaceNil, *TypedNil:
		return nil
	}
	return v
}
