package main

// go/parser over string literals: on Go 1.26 the parser reads
// scannerhooks.StringEnd, which go/scanner's init() installs. The
// install is a write into a package nothing had initialized yet; it must
// survive that package's own initializer.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

const src = `package p

import "fmt"

var s = "a" + ` + "`raw`" + `

func F() { fmt.Println("x", s) }
`

func main() {
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, _ := strconv.Unquote(lit.Value)
			fmt.Println(lit.Value, v)
		}
		return true
	})
}
