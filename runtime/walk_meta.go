package runtime

// Meta returns caller-scoped metadata recorded for key. Unlike [Walk.Seen],
// this does not participate in value conversion; it lets a conversion caller
// keep bookkeeping with exactly the walk's lifetime.
func (w *Walk) Meta(key any) (any, bool) {
	if w.meta == nil {
		return nil, false
	}
	v, ok := w.meta[key]
	return v, ok
}

// SetMeta records caller-scoped metadata for key.
func (w *Walk) SetMeta(key, value any) {
	if w.meta == nil {
		w.meta = map[any]any{}
	}
	w.meta[key] = value
}
