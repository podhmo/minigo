package main

import (
	"os"
	"text/template"
)

func main() {
	template.Must(template.New("t").Parse(`{{range $v := .}}{{printf "%T%d" $v $v}}{{end}}`)).Execute(os.Stdout, int8(3))
}
