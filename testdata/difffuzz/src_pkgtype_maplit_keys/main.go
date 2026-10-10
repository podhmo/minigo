package main

// --src text/template: a composite literal on a named map type reached
// through another package's spec — or a local type decl aliasing one —
// evaluated an identifier key as a field name instead of an expression:
// FuncMap{name: f} produced {"name": f}, so an empty or invalid key
// never panicked inside template.Funcs (upstream TestBadFuncNames).

import (
	"fmt"
	"text/template"
)

func f() int { return 0 }

func main() {
	name := "bad-name"
	for _, m := range []template.FuncMap{
		template.FuncMap{name: f}, // qualified named map type
	} {
		for k := range m {
			fmt.Println(k)
		}
	}
	// a local alias to a package-qualified named map type
	type L = template.FuncMap
	m := L{name: f}
	for k := range m {
		fmt.Println(k)
	}
	// the bad name now panics inside Funcs like gc
	defer func() {
		fmt.Printf("recover: %v\n", recover())
	}()
	template.New("x").Funcs(template.FuncMap{"2": f})
}
