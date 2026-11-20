package main

import (
	"fmt"
	"os"
	"text/template"
)

type C map[string]string

func main() {
	data := map[string]any{"c": C{"x": "1", "y": "2"}}
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"get":   func(c C) string { return c["x"] },
		"write": func(m map[string]any) string { m["c"].(C)["x"] = "2"; return "w" },
	}).Parse(`{{get .c}}|{{write .}}|{{get .c}}`))
	if err := t.Execute(os.Stdout, data); err != nil {
		panic(err)
	}
	fmt.Println("|", data["c"].(C)["x"])
}
