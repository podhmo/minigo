package main

import (
	"bytes"
	"html/template"
)

type Item struct {
	Name  string
	Items []string
}

func F() string {
	var b bytes.Buffer
	template.Must(template.New("item").Parse("{{.Name}}{{range .Items}}{{.}}{{end}}")).Execute(&b, Item{Name: "ok", Items: []string{"!"}})
	return b.String()
}
