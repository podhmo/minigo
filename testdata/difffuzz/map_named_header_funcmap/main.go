package main

import (
	"fmt"
	"os"
	"strings"
	"text/template"
)

// template.FuncMap is `map[string]any` under a name: a plain map assigned
// to it, or handed to Funcs, is the same table under two views — a func
// registered through one view must be visible through the other, and a
// delete must leave no ghost entry (go prints `map[b:2]`; minigo used to
// print `map[a:<nil> b:2]` for the plain map).
func main() {
	plain := map[string]any{"a": 1}
	var fm template.FuncMap = plain
	fm["b"] = 2
	delete(fm, "a")
	fmt.Println(plain, len(plain), fm, len(fm))

	funcs := map[string]any{"up": strings.ToUpper}
	var named template.FuncMap = funcs
	named["lo"] = strings.ToLower
	t := template.Must(template.New("t").Funcs(funcs).Parse("{{up .}} {{lo .}}\n"))
	if err := t.Execute(os.Stdout, "Mixed"); err != nil {
		fmt.Println("err:", err)
	}
}
