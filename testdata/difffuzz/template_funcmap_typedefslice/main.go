package main

import (
	"fmt"
	"os"
	"text/template"
)

type Level int

func main() {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"f": func(ls []Level) string { return fmt.Sprintf("%T %v", ls[0], ls) },
		"g": func(m map[Level]string) string { return fmt.Sprintf("%T %v", m, m[1]) },
		"h": func(l Level) string { return fmt.Sprintf("%T", l) },
	}).Parse(`{{f .S}} {{g .M}} {{h .L}}`))
	err := t.Execute(os.Stdout, map[string]any{"S": []Level{1, 2}, "M": map[Level]string{1: "x"}, "L": Level(7)})
	fmt.Printf("\nERR: %v\n", err)
}
