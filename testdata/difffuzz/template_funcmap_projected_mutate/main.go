package main

import (
	"net/url"
	"os"
	"text/template"
)

func main() {
	v := url.Values{"a": {"1"}, "b": {"2"}}
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"del": func(m url.Values) string { delete(m, "a"); m["c"] = []string{"3"}; return "d" },
	}).Parse(`{{del .}}|{{range $k,$_ := .}}{{$k}};{{end}}`))
	if err := t.Execute(os.Stdout, v); err != nil {
		panic(err)
	}
	println()
}
