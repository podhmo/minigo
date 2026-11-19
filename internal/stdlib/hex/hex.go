package hex

import (
	"encoding/hex"
	"fmt"

	"github.com/podhmo/minigo/internal/stdlib"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	"github.com/podhmo/minigo/runtime"
)

// Install binds encoding/hex: the table is generated; Encode/Decode do
// dst write-back into the script slice and the encoder constructors
// adapt script io.Writer/io.Reader values.
func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters) {
	tbl := encodingHexGenEntries()

	// dst-writeback funcs: a bound slice is copied on the way in, so the
	// result must be written back into the script slice's elements —
	// cells and named slices unwrap to the box they reference.
	writeBack := func(flat runtime.Value, b []byte) error {
		for {
			switch x := flat.(type) {
			case *runtime.Cell:
				flat = x.Elem
			case *runtime.Named:
				flat = x.V
			default:
				goto unwrapped
			}
		}
	unwrapped:
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
	tbl["Encode"] = h.FnvcRaw("hex.Encode", 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
		src := stdlib.ByteSlice(a[1])
		dst := make([]byte, hex.EncodedLen(len(src)))
		n := hex.Encode(dst, src)
		if err := writeBack(flat[0], dst); err != nil {
			return nil, fmt.Errorf("hex.Encode: %w", err)
		}
		return n, nil
	}, hex.Encode)
	tbl["Decode"] = h.FnvcRaw("hex.Decode", 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
		src := stdlib.ByteSlice(a[1])
		dst := make([]byte, hex.DecodedLen(len(src)))
		n, err := hex.Decode(dst, src)
		// on a malformed src Go writes only the decoded prefix — mirror
		// that so the script buffer's tail is untouched.
		if werr := writeBack(flat[0], dst[:n]); werr != nil {
			return nil, fmt.Errorf("hex.Decode: %w", werr)
		}
		return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(n), stdlib.ErrVal(err)}}, nil
	}, hex.Decode)
	tbl["NewEncoder"] = h.FnvcRaw("hex.NewEncoder", 1, func(vc runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		w, err := ad.AsWriterVM(vc, a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.NewEncoder: %w", err)
		}
		return &runtime.GoValue{V: hex.NewEncoder(w)}, nil
	}, hex.NewEncoder)
	tbl["NewDecoder"] = h.FnvcRaw("hex.NewDecoder", 1, func(_ runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		r, err := ad.AsReader(a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.NewDecoder: %w", err)
		}
		return &runtime.GoValue{V: hex.NewDecoder(r)}, nil
	}, hex.NewDecoder)
	tbl["Dumper"] = h.FnvcRaw("hex.Dumper", 1, func(vc runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		w, err := ad.AsWriterVM(vc, a[0])
		if err != nil {
			return nil, fmt.Errorf("hex.Dumper: %w", err)
		}
		return &runtime.GoValue{V: hex.Dumper(w)}, nil
	}, hex.Dumper)
	bind("encoding/hex", tbl)
}
