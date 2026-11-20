package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
)

func main() {
	fmt.Printf("%T\n", os.Stdout)
	fmt.Printf("%T\n", os.Stderr)
	fmt.Printf("%T\n", os.Stdin)

	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "x.go", "package x; func f(a, b int) {}", 0)
	fmt.Printf("%T\n", f.Decls)
	fn, _ := f.Decls[0].(*ast.FuncDecl)
	fmt.Printf("%T\n", fn.Type.Params.List)
	for _, field := range fn.Type.Params.List {
		fmt.Printf("%T\n", field.Names)
	}
	fmt.Printf("%T\n", f.Imports)
}
