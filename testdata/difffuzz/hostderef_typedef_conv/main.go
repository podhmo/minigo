package main

import (
	"fmt"
	"text/template"
)

type Tpl template.Template

func main() {
	t := template.Must(template.New("x").Parse(`{{.}}`))
	t2 := Tpl(*t)
	fmt.Println(t2.Name)
	fmt.Println(t2.Tree.Name)
}
