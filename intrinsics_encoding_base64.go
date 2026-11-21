package minigo

import (
	"encoding/base64"
	"fmt"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingBase64(h *hostHelpers) {
	tbl := encodingBase64GenEntries()

	// the constructors adapt script io.Writer/io.Reader values — the
	// Encoding argument is a GoValue holding the real *Encoding.
	tbl["NewEncoder"] = h.fnvcRaw("base64.NewEncoder", 2, func(vc runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		enc, ok := a[0].(*base64.Encoding)
		if !ok {
			return nil, fmt.Errorf("base64.NewEncoder: arg 1 must be an *Encoding, got %T", a[0])
		}
		w, err := asWriterVM(vc, a[1])
		if err != nil {
			return nil, fmt.Errorf("base64.NewEncoder: %w", err)
		}
		return &runtime.GoValue{V: base64.NewEncoder(enc, w)}, nil
	}, base64.NewEncoder)
	tbl["NewDecoder"] = h.fnvcRaw("base64.NewDecoder", 2, func(_ runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		enc, ok := a[0].(*base64.Encoding)
		if !ok {
			return nil, fmt.Errorf("base64.NewDecoder: arg 1 must be an *Encoding, got %T", a[0])
		}
		r, err := asReader(a[1])
		if err != nil {
			return nil, fmt.Errorf("base64.NewDecoder: %w", err)
		}
		return &runtime.GoValue{V: base64.NewDecoder(enc, r)}, nil
	}, base64.NewDecoder)
	e.Bind("encoding/base64", tbl)
}
