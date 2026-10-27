package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
)

// min and max type their result like an operator chain: untyped
// constant operands take the typed operand's type (`max(200, u8)` is
// the uint8 200), a non-constant shift truncates at that type, an
// all-constant call is a constant of the widest kind, and host named
// basics (fs.FileMode from os.Stat, a parsed token.Token) compare by
// value against their source twins and constants.

type M uint8

func main() {
	var u8 uint8 = 3
	a := max(200, u8)
	fmt.Printf("%T %v\n", a, a) // uint8 200
	var m M = 5
	b := min(m, 1)
	fmt.Printf("%T %v\n", b, b) // main.M 1
	n := 8
	c := max(1<<n, u8)
	fmt.Printf("%T %v\n", c, c) // uint8 3
	d := min(2.5, 1)
	fmt.Printf("%T %v\n", d, d) // float64 1
	var f float64 = 2
	e := max(f, 3)
	fmt.Printf("%T %v\n", e, e)     // float64 3
	fmt.Printf("%T\n", max(1, 'a')) // int32
	const k = min(1, 2.5)
	fmt.Println(k / 2) // 0.5

	dir, _ := os.MkdirTemp("", "minmax")
	defer os.RemoveAll(dir)
	st, _ := os.Stat(dir)
	mt := st.Mode().Type()
	fmt.Println(min(mt, fs.ModeSymlink), max(mt, fs.ModeSymlink)) // L--------- d---------
	fmt.Println(min(fs.ModeSymlink, mt), max(fs.ModeSymlink, mt)) // L--------- d---------
	fmt.Println(min(mt, 0), max(0, mt) == fs.ModeDir)             // ---------- true

	x, _ := parser.ParseExpr("a * b")
	tok := x.(*ast.BinaryExpr).Op
	fmt.Println(min(tok, token.ADD), max(tok, token.ADD), min(tok, 1)) // + * EOF
	fmt.Printf("%T\n", min(tok, 1))                                    // token.Token
}
