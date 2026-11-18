package minigo

import (
	"fmt"
	htmltemplate "html/template"
	"reflect"
	"text/template"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installTextTemplate(h *hostHelpers) {
	// text/template: New/Parse/Must box *template.Template — Execute's
	// io.Writer arg adapts `&b` cells via toReflectValue's auto-pointer
	// rule and the data map marshals through goNative.
	e.Bind("text/template", map[string]runtime.Value{
		"Template": hostType("text/template.Template", func() any { return template.New("") }),
		"New": h.fn1("template.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: template.New(str(a[0]))}, nil
		}, template.New),
		"Must":    templateMust(template.Must),
		"FuncMap": e.templateFuncMap(),
		// a boxed host func: the fsys arg marshals through
		// callReflectFunc's fs.FS adaptation, so a script embed.FS (or
		// any script type declaring Open) works as template.ParseFS's
		// filesystem.
		"ParseFS": &runtime.GoValue{V: template.ParseFS},
		"HTMLEscapeString": h.fn1("template.HTMLEscapeString", func(a []any) (any, error) {
			return template.HTMLEscapeString(str(a[0])), nil
		}, template.HTMLEscapeString),
		"JSEscapeString": h.fn1("template.JSEscapeString", func(a []any) (any, error) {
			return template.JSEscapeString(str(a[0])), nil
		}, template.JSEscapeString),
		"URLQueryEscaper": h.fn("template.URLQueryEscaper", func(a []any) (any, error) {
			ss := make([]any, len(a))
			for i, x := range a {
				ss[i] = str(x)
			}
			return template.URLQueryEscaper(ss...), nil
		}, template.URLQueryEscaper),
	})
}

func init() {
	vm.RegisterFuncMapType(reflect.TypeFor[template.FuncMap]())
	vm.RegisterFuncMapType(reflect.TypeFor[htmltemplate.FuncMap]())
}

// templateFuncMap binds text/template's FuncMap — and html/template's,
// an alias of it in Go — as a script map[string]any; handing it to
// Template.Funcs adapts each func to a host func (see vm.funcMapTypes).
// Each session owns its own typedef (installbound.go: templateFuncMap).

// templateMust binds text/template's or html/template's Must. It takes
// the (t, err) pair of New/Parse — a Tuple arg unpacks — or the two
// values spelled out. The panic on a non-nil err mirrors template.Must
// and carries the error value itself, so recover() hands back the
// script's error.
func templateMust(target any) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: "template.Must", Target: target, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		var t, ev runtime.Value
		switch {
		case len(args) == 1:
			t, ev = args[0], runtime.NIL
			if tup, ok := args[0].(*runtime.Tuple); ok && len(tup.Elems) == 2 {
				t, ev = tup.Elems[0], tup.Elems[1]
			}
		case len(args) == 2:
			t, ev = args[0], args[1]
		default:
			return nil, fmt.Errorf("template.Must needs 2 args, got %d", len(args))
		}
		if err := hostErrOf(v, ev); err != nil {
			panic(&runtime.Panic{Value: errVal(err), VC: v})
		}
		return scriptVal(goNative(t)), nil
	}}
}
