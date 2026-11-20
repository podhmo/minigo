package main

import (
	"fmt"
	"go/ast"
)

// writing into a boxed host struct's fields: a script value into a host
// field, a grown host slice, and a host slice element.
func main() {
	ce := &ast.CallExpr{}
	ce.Fun = ast.NewIdent("x")
	ce.Args = append(ce.Args, ast.NewIdent("a"))
	ce.Args[0] = ast.NewIdent("b")
	fmt.Println(ce.Fun.(*ast.Ident).Name, len(ce.Args), ce.Args[0].(*ast.Ident).Name)

	// a write through a stored handle aliases the same box.
	f := ast.NewIdent("one")
	ce.Fun = f
	f.Name = "two"
	fmt.Println(ce.Fun.(*ast.Ident).Name)
}
