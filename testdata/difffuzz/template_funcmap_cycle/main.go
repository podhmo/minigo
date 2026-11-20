package main

import (
	"fmt"
	"net/url"
	"os"
	"text/template"
)

type M map[string]any

func main() {
	m := M{}
	m["self"] = m
	m["k"] = "v"
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"keys": func(x map[string]any) string {
			out := ""
			for k := range x {
				out += k + ";"
			}
			return out
		},
		"count": func(x url.Values) int { return len(x) },
	}).Parse(`{{keys .}}`))
	if err := t.Execute(os.Stdout, m); err != nil {
		panic(err)
	}
	fmt.Println()
}
