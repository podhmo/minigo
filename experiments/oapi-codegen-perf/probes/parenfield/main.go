package main

import (
	"os"
	"text/template"
)

type It struct{ Name string }

func main() {
	t := template.Must(template.New("x").Parse(`{{(index .Items 1).Name}}` + "\n"))
	if err := t.Execute(os.Stdout, map[string]any{"Items": []It{{"a"}, {"b"}}}); err != nil {
		panic(err)
	}
}
