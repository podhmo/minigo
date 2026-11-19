// Package htmltemplate is the lazy installer for the bound
// "html/template" package (P2-12c).
package htmltemplate

import (
	"go/ast"
	ht "html/template"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, funcMap *runtime.TypeDef) {
	// html/template is bound like text/template rather than interpreted:
	// its source walks text/template's parse trees and FuncMap, which the
	// bound text/template does not expose. Script data marshals through
	// hostbridge.GoNative the same way, and the content types (template.HTML, ...)
	// are host scalars so a script value of them skips escaping.
	htmlContent := func(name string, zero any) *runtime.TypeDef {
		return &runtime.TypeDef{Name: "html/template." + name, Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("string"), HostScalar: zero}
	}

	bind("html/template", map[string]runtime.Value{
		"Template": stdlib.HostType("html/template.Template", func() any { return ht.New("") }),
		"New": h.Fn1("template.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: ht.New(stdlib.Str(a[0]))}, nil
		}, ht.New),
		"Must":     stdlib.TemplateMust("template.Must", ht.Must),
		"FuncMap":  funcMap, // html/template.FuncMap aliases text/template's
		"ParseFS":  &runtime.GoValue{V: ht.ParseFS},
		"HTML":     htmlContent("HTML", ht.HTML("")),
		"HTMLAttr": htmlContent("HTMLAttr", ht.HTMLAttr("")),
		"JS":       htmlContent("JS", ht.JS("")),
		"JSStr":    htmlContent("JSStr", ht.JSStr("")),
		"CSS":      htmlContent("CSS", ht.CSS("")),
		"URL":      htmlContent("URL", ht.URL("")),
		"Srcset":   htmlContent("Srcset", ht.Srcset("")),
		"HTMLEscapeString": h.Fn1("template.HTMLEscapeString", func(a []any) (any, error) {
			return ht.HTMLEscapeString(stdlib.Str(a[0])), nil
		}, ht.HTMLEscapeString),
		"JSEscapeString": h.Fn1("template.JSEscapeString", func(a []any) (any, error) {
			return ht.JSEscapeString(stdlib.Str(a[0])), nil
		}, ht.JSEscapeString),
		"URLQueryEscaper": h.Fn("template.URLQueryEscaper", func(a []any) (any, error) {
			ss := make([]any, len(a))
			for i, x := range a {
				ss[i] = stdlib.Str(x)
			}
			return ht.URLQueryEscaper(ss...), nil
		}, ht.URLQueryEscaper),
	})
}
