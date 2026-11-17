package minigo

import (
	"html"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installHtml(h *hostHelpers) {
	e.Bind("html", map[string]runtime.Value{
		"EscapeString":   h.fn("html.EscapeString", func(a []any) (any, error) { return html.EscapeString(str(a[0])), nil }, html.EscapeString),
		"UnescapeString": h.fn("html.UnescapeString", func(a []any) (any, error) { return html.UnescapeString(str(a[0])), nil }, html.UnescapeString),
	})
}
