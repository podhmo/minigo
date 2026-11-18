package minigo

import (
	"encoding/csv"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingCsv(h *hostHelpers) {
	// encoding/csv: NewReader binds the real *csv.Reader so Read/ReadAll and
	// field tuning (Comma/FieldsPerRecord via host field writes) work.
	e.bindBound("encoding/csv", map[string]runtime.Value{
		"NewReader": h.fn1("csv.NewReader", func(a []any) (any, error) {
			r, err := asReader(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewReader(r)}, nil
		}, csv.NewReader),
		"NewWriter": h.fn1("csv.NewWriter", func(a []any) (any, error) {
			w, err := asWriter(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewWriter(w)}, nil
		}, csv.NewWriter),
	})
}
