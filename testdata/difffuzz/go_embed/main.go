package main

// //go:embed on string, []byte and embed.FS vars: directory patterns
// skip . and _ names unless all:, and embed.FS walks/reads/lists like
// the compiler-built value (oapi-codegen embeds its templates dir).

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed hello.txt
var s string

//go:embed hello.txt
var b []byte

//go:embed tpl
var tpl embed.FS

var (
	//go:embed all:tpl hello.txt
	all embed.FS
)

func main() {
	fmt.Printf("%q %q\n", s, b)
	for _, f := range []embed.FS{tpl, all} {
		fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
			fmt.Println(p, d.IsDir(), err)
			return nil
		})
	}
	data, err := tpl.ReadFile("tpl/sub/b.tmpl")
	fmt.Printf("%q %v\n", data, err)
	ents, _ := tpl.ReadDir("tpl")
	for _, e := range ents {
		fmt.Println(e.Name(), e.IsDir())
	}
}
