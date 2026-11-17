package minigo

import (
	"path"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installPath(h *hostHelpers) {
	e.Bind("path", map[string]runtime.Value{
		"Base":          h.fn("path.Base", func(a []any) (any, error) { return path.Base(str(a[0])), nil }, path.Base),
		"Clean":         h.fn("path.Clean", func(a []any) (any, error) { return path.Clean(str(a[0])), nil }, path.Clean),
		"Dir":           h.fn("path.Dir", func(a []any) (any, error) { return path.Dir(str(a[0])), nil }, path.Dir),
		"Ext":           h.fn("path.Ext", func(a []any) (any, error) { return path.Ext(str(a[0])), nil }, path.Ext),
		"IsAbs":         h.fn("path.IsAbs", func(a []any) (any, error) { return path.IsAbs(str(a[0])), nil }, path.IsAbs),
		"Join":          h.fn("path.Join", func(a []any) (any, error) { return path.Join(strArgs(a)...), nil }, path.Join),
		"Match":         h.fn2("path.Match", func(a []any) (any, error) { return retErr2(path.Match(str(a[0]), str(a[1]))) }),
		"ErrBadPattern": errVal(path.ErrBadPattern),
		"Split": h.fn("path.Split", func(a []any) (any, error) {
			d, f := path.Split(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{d, f}}, nil
		}),
	})
}
