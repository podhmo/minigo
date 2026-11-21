package main

import (
	"os"
	"text/template"
)

type P struct{ N int }

func main() {
	p := &P{N: 1}
	m := map[string]*P{"p": p}
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"bump": func(x *P) string { x.N = 9; return "b" },
	}).Parse(`{{.p.N}}{{bump .p}}{{.p.N}}|`))
	if err := t.Execute(os.Stdout, m); err != nil {
		panic(err)
	}
}
