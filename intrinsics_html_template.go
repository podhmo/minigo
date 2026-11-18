package minigo

import (
	"go/ast"
	htmltemplate "html/template"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installHtmlTemplate(h *hostHelpers) {
	// html/template is bound like text/template rather than interpreted:
	// its source walks text/template's parse trees and FuncMap, which the
	// bound text/template does not expose. Script data marshals through
	// goNative the same way, and the content types (template.HTML, ...)
	// are host scalars so a script value of them skips escaping.
	htmlContent := func(name string, zero any) *runtime.TypeDef {
		return &runtime.TypeDef{Name: "html/template." + name, Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("string"), HostScalar: zero}
	}

	e.Bind("html/template", map[string]runtime.Value{
		"Template": hostType("html/template.Template", func() any { return htmltemplate.New("") }),
		"New": h.fn1("template.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: htmltemplate.New(str(a[0]))}, nil
		}, htmltemplate.New),
		"Must":     templateMust(htmltemplate.Must),
		"FuncMap":  e.templateFuncMap(), // html/template.FuncMap aliases text/template's
		"ParseFS":  &runtime.GoValue{V: htmltemplate.ParseFS},
		"HTML":     htmlContent("HTML", htmltemplate.HTML("")),
		"HTMLAttr": htmlContent("HTMLAttr", htmltemplate.HTMLAttr("")),
		"JS":       htmlContent("JS", htmltemplate.JS("")),
		"JSStr":    htmlContent("JSStr", htmltemplate.JSStr("")),
		"CSS":      htmlContent("CSS", htmltemplate.CSS("")),
		"URL":      htmlContent("URL", htmltemplate.URL("")),
		"Srcset":   htmlContent("Srcset", htmltemplate.Srcset("")),
		"HTMLEscapeString": h.fn1("template.HTMLEscapeString", func(a []any) (any, error) {
			return htmltemplate.HTMLEscapeString(str(a[0])), nil
		}, htmltemplate.HTMLEscapeString),
		"JSEscapeString": h.fn1("template.JSEscapeString", func(a []any) (any, error) {
			return htmltemplate.JSEscapeString(str(a[0])), nil
		}, htmltemplate.JSEscapeString),
		"URLQueryEscaper": h.fn("template.URLQueryEscaper", func(a []any) (any, error) {
			ss := make([]any, len(a))
			for i, x := range a {
				ss[i] = str(x)
			}
			return htmltemplate.URLQueryEscaper(ss...), nil
		}, htmltemplate.URLQueryEscaper),
	})
}
