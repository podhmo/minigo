package execution

import "sync"

// Intern is a session-scoped identity → descriptor table (M4-3b): one
// canonical descriptor per type identity. It is generic over key and
// value so internal/execution stays free of runtime — runtime imports
// this package, and a cycle would form.
//
// sync.Map gives atomic load-or-store: goroutines racing the same key
// observe the same stored descriptor and the losers' candidates are
// dropped — interning is what makes `td1 == td2` a correct type check.
// A session forks create a fresh table rather than sharing: a type
// re-declared in the forked session must not alias the parent's
// descriptor, and the key's source-version component would not protect
// a table shared across sessions holding different parses of one file.
type Intern[K comparable, V any] struct {
	m sync.Map
}

// NewIntern returns an empty intern table.
func NewIntern[K comparable, V any]() *Intern[K, V] {
	return &Intern[K, V]{}
}

// Of returns the descriptor stored for k — v when present, otherwise
// v after storing it. Callers hold ONE canonical descriptor per key.
func (t *Intern[K, V]) Of(k K, v V) V {
	if t == nil {
		return v
	}
	got, _ := t.m.LoadOrStore(k, v)
	return got.(V)
}

// Store replaces the descriptor stored for k: a re-registration (a
// re-Bind of the same type) makes the newer descriptor canonical,
// while Of keeps the first.
func (t *Intern[K, V]) Store(k K, v V) {
	if t == nil {
		return
	}
	t.m.Store(k, v)
}

// Lookup returns the stored descriptor without storing.
func (t *Intern[K, V]) Lookup(k K) (V, bool) {
	if t == nil {
		var zero V
		return zero, false
	}
	got, ok := t.m.Load(k)
	if !ok {
		var zero V
		return zero, false
	}
	return got.(V), true
}

// Len reports how many keys are stored — diagnostics and tests only.
func (t *Intern[K, V]) Len() int {
	if t == nil {
		return 0
	}
	n := 0
	t.m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// Replace stores v for k unconditionally — callers upgrade the
// canonical descriptor in place when a later arrival carries more
// (e.g. a host box a name-only tag lacked).
func (t *Intern[K, V]) Replace(k K, v V) {
	if t == nil {
		return
	}
	t.m.Store(k, v)
}
