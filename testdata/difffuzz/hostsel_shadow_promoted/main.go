package main

import (
	"fmt"
	"text/template"
)

// a declared method beats a promoted field of the same name:
// template.Template embeds *parse.Tree (which has a Name string
// field) and declares Name() — t.Name is the method, t.Tree.Name
// the field.
func main() {
	t := template.Must(template.New("x").Parse(`{{.}}`))
	fmt.Println(t.Name())
	fmt.Println(t.Tree.Name)
	fmt.Println(t.Templates()[0].Name())
}
