package main

import (
	"os"
	"text/template"
)

// text/template invokes a niladic method on field access — `{{if
// .Maybe}}` evaluates Maybe(), it does not read the method value
// itself — and the (value, error) shape stores the first result.
type T struct{}

func (*T) Maybe() bool           { return false }
func (*T) Name() string          { return "minigo" }
func (*T) Pair() (string, error) { return "paired", nil }

func main() {
	t := template.Must(template.New("t").Parse(`{{if .Maybe}}yes{{else}}no{{end}} {{.Name}} {{.Pair}}`))
	if err := t.Execute(os.Stdout, &T{}); err != nil {
		panic(err)
	}
}
