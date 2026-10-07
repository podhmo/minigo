package main

import "fmt"

type (
	PackageName = string
	Symbol      = string

	References = map[PackageName]map[Symbol]bool
)

func main() {
	refs := References{}
	r := refs["fmt"]
	if r == nil {
		r = make(map[string]bool)
		refs["fmt"] = r
	}
	r["Println"] = true
	fmt.Println(refs)
}

type Names = []Symbol

func init() {
	var n Names = []string{"a"}
	var m map[Symbol]Names = map[string][]string{"k": n}
	fmt.Printf("%T %T %v\n", n, m, m)
	var f func(Symbol) PackageName = func(s string) string { return s + "!" }
	fmt.Println(f("x"))
}
