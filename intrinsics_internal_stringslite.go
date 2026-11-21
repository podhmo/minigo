package minigo

import (
	"strings"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInternalStringslite(h *hostHelpers) {
	e.Bind("internal/stringslite", map[string]runtime.Value{
		"HasPrefix": h.fn2("stringslite.HasPrefix", func(a []any) (any, error) { return strings.HasPrefix(str(a[0]), str(a[1])), nil }),
		"HasSuffix": h.fn2("stringslite.HasSuffix", func(a []any) (any, error) { return strings.HasSuffix(str(a[0]), str(a[1])), nil }),
		"IndexByte": h.fn2("stringslite.IndexByte", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"Index":     h.fn2("stringslite.Index", func(a []any) (any, error) { return int(strings.Index(str(a[0]), str(a[1]))), nil }),
		"Cut": h.fn2("stringslite.Cut", func(a []any) (any, error) {
			b, f, ok := strings.Cut(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{b, f, ok}}, nil
		}),
		"CutPrefix": h.fn2("stringslite.CutPrefix", func(a []any) (any, error) {
			r, ok := strings.CutPrefix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}),
		"CutSuffix": h.fn2("stringslite.CutSuffix", func(a []any) (any, error) {
			r, ok := strings.CutSuffix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}),
		"TrimPrefix":      h.fn2("stringslite.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(str(a[0]), str(a[1])), nil }),
		"TrimSuffix":      h.fn2("stringslite.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(str(a[0]), str(a[1])), nil }),
		"Clone":           h.fn1("stringslite.Clone", func(a []any) (any, error) { return strings.Clone(str(a[0])), nil }),
		"IndexByteString": h.fn2("stringslite.IndexByteString", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
	})
}
