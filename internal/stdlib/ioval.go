package stdlib

import (
	"github.com/podhmo/minigo/runtime"
)

// BorrowBytes marshals a script value to []byte for a host call that
// writes into the buffer: the returned commit writes the bytes back
// into the script slice's elements. It must see the arg before hostbridge.GoNative
// flattens it — an []any has lost the script slice it came from. Values
// already sharing their backing (a host []byte inside a GoValue) need
// no commit; read-only consumers keep using ByteSlice.
func BorrowBytes(v runtime.Value) ([]byte, func()) {
	u := v
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		if _, isGV := u.(*runtime.GoValue); isGV {
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			break
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	switch x := u.(type) {
	case *runtime.Slice:
		bs := ByteSlice(x)
		return bs, func() {
			for i := range bs {
				x.SetElem(int64(i), runtime.ByteValue(bs[i]))
			}
		}
	case *runtime.GoValue:
		if bs, ok := x.V.([]byte); ok {
			// shared backing — host writes are already visible
			return bs, func() {}
		}
	}
	return ByteSlice(v), func() {}
}

// HostBoxOf resolves a Named box (e.g. a `&strings.Builder{}`), which
// rides as Named{HostNew typedef, GoValue}, to its GoValue box, so io
// helpers hand the real host value to the host call instead of adapting
// it through the script-callback proxy — the proxy converts every chunk
// to a script []byte and back.
func HostBoxOf(orig, v any) any {
	// a pointer converted to a script-defined type, (*MyB)(&sb), keeps
	// that type's method set: its Write must not be bypassed.
	if t, ok := orig.(*runtime.Named); ok && (t.Typ == nil || t.Typ.HostNew == nil) {
		return v
	}
	if n, ok := v.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostNew != nil {
		if g, ok := n.V.(*runtime.GoValue); ok {
			return g
		}
	}
	return v
}
