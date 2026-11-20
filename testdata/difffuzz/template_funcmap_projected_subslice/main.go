package main

import (
	"os"
	"text/template"
)

type Nums []int

func main() {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"count": func(xs []int) int { return len(xs) },
	}).Parse(`{{count (slice . 0 1)}} {{count .}}`))
	if err := t.Execute(os.Stdout, Nums{1, 2, 3}); err != nil {
		panic(err)
	}
	println()
}
