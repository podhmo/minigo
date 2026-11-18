package execution

// Registry is a named-handler table for special forms — generic over the
// key and handler types so internal/execution can own the mechanism
// without importing runtime (which already depends on this package).
// It carries no locking of its own: the engine guards it with the same
// mutex that serializes its other shared tables, and a session forks
// share the pointer exactly as they shared the map.
type Registry[K comparable, V any] struct {
	m map[K]V
}

// NewRegistry returns an empty registry.
func NewRegistry[K comparable, V any]() *Registry[K, V] {
	return &Registry[K, V]{m: map[K]V{}}
}

// Register binds k to v, replacing any previous handler.
func (r *Registry[K, V]) Register(k K, v V) { r.m[k] = v }

// Lookup returns the handler bound to k.
func (r *Registry[K, V]) Lookup(k K) (V, bool) {
	if r == nil {
		var zero V
		return zero, false
	}
	v, ok := r.m[k]
	return v, ok
}

// Has reports whether k is registered.
func (r *Registry[K, V]) Has(k K) bool {
	if r == nil {
		return false
	}
	_, ok := r.m[k]
	return ok
}

// Len reports how many handlers are registered. A nil registry is empty.
func (r *Registry[K, V]) Len() int {
	if r == nil {
		return 0
	}
	return len(r.m)
}
