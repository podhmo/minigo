package main

import (
	"os"
	"text/template"
)

type arg struct{ def bool }

func (a *arg) Maybe() bool { return true }

func main() {
	t := template.Must(template.New("t").Parse(`{{if .Maybe}}yes{{else}}no{{end}}`))
	if err := t.Execute(os.Stdout, &arg{}); err != nil {
		panic(err)
	}
}
