package runtime

// Walk is the state of one recursive conversion of a script value to Go
// values, such as [DeepUnwrap]: it remembers the output already built for
// each container, so cyclic and shared containers convert to the same Go
// value, and the references already followed, so a pointer cycle ends.
// Create one with [NewWalk] per conversion; [Walk.Container] converts
// slices and maps, and the caller handles every other value.
type Walk struct {
	// The maps allocate lazily: a scalar-only walk never pays for them.
	// DeepUnwrap, the vm package's host marshal, and the root package's
	// fmt-bound walk all run their container arms through it.
	seen   map[Value]any
	peeled map[Value]struct{}
}

// NewWalk returns an empty [Walk].
func NewWalk() *Walk { return &Walk{} }

// Container converts a [*Slice] to []any and a [*Map] to map[any]any,
// converting elements with each and map keys with key (typically
// [UnwrapKey], which keeps keys hashable). A container seen before in
// this walk returns its earlier, possibly still incomplete, output. ok is
// false when v is neither.
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

// Seen returns the output [Walk.Register] recorded for v, for containers
// that [Walk.Container] does not handle.
func (w *Walk) Seen(v Value) (any, bool) {
	if w.seen == nil {
		return nil, false
	}
	old, ok := w.seen[v]
	return old, ok
}

// Register records out as the output for v. Call it before converting
// v's elements, so a cycle back to v finds out through [Walk.Seen].
func (w *Walk) Register(v Value, out any) {
	if w.seen == nil {
		w.seen = map[Value]any{}
	}
	w.seen[v] = out
}

// Peel records that the walk follows the reference v. It reports false
// when v was already followed, meaning the caller has found a cycle and
// should stop.
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

// UnwrapKey converts a script map key to a Go map key, unwrapping only
// what keeps it hashable: [Named] tags, pointers, a [GoValue]'s value and
// an untyped constant's value. Script containers stay as they are — an
// array key remains a *Slice rather than an unhashable []any — and nil
// values give nil.
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
