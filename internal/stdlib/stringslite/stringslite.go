package stringslite

import (
	"strings"

	"github.com/podhmo/minigo/internal/stdlib"

	"github.com/podhmo/minigo/runtime"
)

// Install binds the internal/stringslite stub surface (pure helpers GOROOT sources call).
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	bind("internal/stringslite", map[string]runtime.Value{
		"HasPrefix": h.Fn2("stringslite.HasPrefix", func(a []any) (any, error) { return strings.HasPrefix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.HasPrefix),
		"HasSuffix": h.Fn2("stringslite.HasSuffix", func(a []any) (any, error) { return strings.HasSuffix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.HasSuffix),
		"IndexByte": h.Fn2("stringslite.IndexByte", func(a []any) (any, error) {
			return int(strings.IndexByte(stdlib.Str(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, strings.IndexByte),
		"Index": h.Fn2("stringslite.Index", func(a []any) (any, error) { return int(strings.Index(stdlib.Str(a[0]), stdlib.Str(a[1]))), nil }, strings.Index),
		"Cut": h.Fn2("stringslite.Cut", func(a []any) (any, error) {
			b, f, ok := strings.Cut(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{b, f, ok}}, nil
		}, strings.Cut),
		"CutPrefix": h.Fn2("stringslite.CutPrefix", func(a []any) (any, error) {
			r, ok := strings.CutPrefix(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}, strings.CutPrefix),
		"CutSuffix": h.Fn2("stringslite.CutSuffix", func(a []any) (any, error) {
			r, ok := strings.CutSuffix(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}, strings.CutSuffix),
		"TrimPrefix": h.Fn2("stringslite.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimPrefix),
		"TrimSuffix": h.Fn2("stringslite.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimSuffix),
		"Clone":      h.Fn1("stringslite.Clone", func(a []any) (any, error) { return strings.Clone(stdlib.Str(a[0])), nil }, strings.Clone),
		"IndexByteString": h.Fn2("stringslite.IndexByteString", func(a []any) (any, error) {
			return int(strings.IndexByte(stdlib.Str(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, strings.IndexByte),
	})
}
