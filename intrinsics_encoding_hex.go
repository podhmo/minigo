//go:generate go run ./internal/cmd/genbind

package minigo

import (
	"encoding/hex"
	"fmt"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingHex(h *hostHelpers) {
	tbl := encodingHexGenEntries()

	// dst-writeback funcs: a bound slice is copied on the way in, so the
	// result must be written back into the script slice's elements —
	// cells unwrap to the box they reference.
	writeBack := func(flat runtime.Value, b []byte) error {
		if c, ok := flat.(*runtime.Cell); ok {
			flat = c.Elem
		}
		s, ok := flat.(*runtime.Slice)
		if !ok {
			return fmt.Errorf("dst must be a []byte, got %T", flat)
		}
		if len(s.Elems) < len(b) {
			return fmt.Errorf("dst has %d bytes, need %d", len(s.Elems), len(b))
		}
		for i, by := range b {
			s.Elems[i] = runtime.ByteValue(by)
		}
		return nil
	}
	tbl["Encode"] = h.fnvcRaw("hex.Encode", 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
		src := byteSlice(a[1])
		dst := make([]byte, hex.EncodedLen(len(src)))
		n := hex.Encode(dst, src)
		if err := writeBack(flat[0], dst); err != nil {
			return nil, fmt.Errorf("hex.Encode: %w", err)
		}
		return n, nil
	})
	tbl["Decode"] = h.fnvcRaw("hex.Decode", 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
		src := byteSlice(a[1])
		dst := make([]byte, hex.DecodedLen(len(src)))
		n, err := hex.Decode(dst, src)
		if werr := writeBack(flat[0], dst); werr != nil {
			return nil, fmt.Errorf("hex.Decode: %w", werr)
		}
		return &runtime.Tuple{Elems: []runtime.Value{n, errVal(err)}}, nil
	})
	tbl["NewEncoder"] = h.fnvcRaw("hex.NewEncoder", 1, func(vc runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		w, err := asWriterVM(vc, a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.NewEncoder: %w", err)
		}
		return &runtime.GoValue{V: hex.NewEncoder(w)}, nil
	})
	tbl["NewDecoder"] = h.fnvcRaw("hex.NewDecoder", 1, func(_ runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		r, err := asReader(a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.NewDecoder: %w", err)
		}
		return &runtime.GoValue{V: hex.NewDecoder(r)}, nil
	})
	tbl["Dumper"] = h.fnvcRaw("hex.Dumper", 1, func(vc runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		w, err := asWriterVM(vc, a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.Dumper: %w", err)
		}
		return &runtime.GoValue{V: hex.Dumper(w)}, nil
	})
	e.Bind("encoding/hex", tbl)
}
