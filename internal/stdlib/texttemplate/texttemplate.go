// Package texttemplate is the lazy installer for the bound
// "text/template" package (P2-12c).
package texttemplate

import (
	ht "html/template"
	"reflect"
	stdtemplate "text/template"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, funcMap *runtime.TypeDef) {
	// text/template: New/Parse/Must box *stdtemplate.Template — Execute's
	// io.Writer arg adapts `&b` cells via toReflectValue's auto-pointer
	// rule and the data map marshals through hostbridge.GoNative.
	bind("text/template", map[string]runtime.Value{
		"Template": stdlib.HostType("text/stdtemplate.Template", func() any { return stdtemplate.New("") }),
		"New": h.Fn1("template.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: stdtemplate.New(stdlib.Str(a[0]))}, nil
		}, stdtemplate.New),
		"Must":    stdlib.TemplateMust("template.Must", stdtemplate.Must),
		"FuncMap": funcMap,
		// a boxed host func: the fsys arg marshals through
		// callReflectFunc's fs.FS adaptation, so a script embed.FS (or
		// any script type declaring Open) works as stdtemplate.ParseFS's
		// filesystem.
		"ParseFS": &runtime.GoValue{V: stdtemplate.ParseFS},
		"HTMLEscapeString": h.Fn1("template.HTMLEscapeString", func(a []any) (any, error) {
			return stdtemplate.HTMLEscapeString(stdlib.Str(a[0])), nil
		}, stdtemplate.HTMLEscapeString),
		"JSEscapeString": h.Fn1("template.JSEscapeString", func(a []any) (any, error) {
			return stdtemplate.JSEscapeString(stdlib.Str(a[0])), nil
		}, stdtemplate.JSEscapeString),
		"URLQueryEscaper": h.Fn("template.URLQueryEscaper", func(a []any) (any, error) {
			ss := make([]any, len(a))
			for i, x := range a {
				ss[i] = stdlib.Str(x)
			}
			return stdtemplate.URLQueryEscaper(ss...), nil
		}, stdtemplate.URLQueryEscaper),
	})
}

func init() {
	vm.RegisterFuncMapType(reflect.TypeFor[stdtemplate.FuncMap]())
	vm.RegisterFuncMapType(reflect.TypeFor[ht.FuncMap]())
}

// templateFuncMap binds text/template's FuncMap — and html/template's,
// an alias of it in Go — as a script map[string]any; handing it to
// Template.Funcs adapts each func to a host func (see vm.funcMapTypes).
// Each session owns its own typedef (installbound.go: templateFuncMap).
