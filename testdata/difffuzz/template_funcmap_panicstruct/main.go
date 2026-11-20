package main

import (
	"bytes"
	"fmt"
	"text/template"
)

type P struct{ Name string }

func main() {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"boom":    func() string { panic(P{Name: "x"}) },
		"boomPtr": func() string { panic(&P{Name: "x"}) },
		"boomVar": func() string { var p P = struct{ Name string }{"x"}; panic(p) },
	}).Parse(`{{boom}}`))
	var b bytes.Buffer
	err := t.Execute(&b, nil)
	fmt.Println("ERR:", err)
	t2 := template.Must(template.New("t2").Funcs(template.FuncMap{
		"boomPtr": func() string { panic(&P{Name: "x"}) },
	}).Parse(`{{boomPtr}}`))
	b.Reset()
	err = t2.Execute(&b, nil)
	fmt.Println("ERR:", err)
	t3 := template.Must(template.New("t3").Funcs(template.FuncMap{
		"boomVar": func() string { var p P = struct{ Name string }{"x"}; panic(p) },
	}).Parse(`{{boomVar}}`))
	b.Reset()
	err = t3.Execute(&b, nil)
	fmt.Println("ERR:", err)
}
