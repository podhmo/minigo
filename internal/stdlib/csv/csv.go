package csv

import (
	"encoding/csv"

	"github.com/podhmo/minigo/internal/stdlib"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	"github.com/podhmo/minigo/runtime"
)

// Install binds encoding/csv: NewReader/NewWriter box the real host
// *csv.Reader/*csv.Writer so Read/ReadAll and field tuning
// (Comma/FieldsPerRecord via host field writes) work.
func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters) {
	bind("encoding/csv", map[string]runtime.Value{
		"NewReader": h.Fn1("csv.NewReader", func(a []any) (any, error) {
			r, err := ad.AsReader(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewReader(r)}, nil
		}, csv.NewReader),
		"NewWriter": h.Fn1("csv.NewWriter", func(a []any) (any, error) {
			w, err := ad.AsWriter(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewWriter(w)}, nil
		}, csv.NewWriter),
	})
}
