// Command archcheck checks the module's internal import graph against
// the allow list in internal/archcheck/rules.go. It is a ratchet: today
// the allow list pins the current graph; later phases tighten it.
//
//	go run ./internal/cmd/archcheck          # check
//	go run ./internal/cmd/archcheck -print   # print the live graph in rules.go form
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/podhmo/minigo/internal/archcheck"
)

func main() {
	root := flag.String("root", ".", "module root")
	printGraph := flag.Bool("print", false, "print the live graph in rules.go form and exit")
	flag.Parse()

	g, err := archcheck.Build(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	if *printGraph {
		var pkgs []string
		for pkg := range g {
			pkgs = append(pkgs, pkg)
		}
		sort.Strings(pkgs)
		for _, pkg := range pkgs {
			fmt.Printf("\t%q: {%s},\n", pkg, joinQuoted(g[pkg]))
		}
		return
	}
	violations := archcheck.Check(g, archcheck.Rules)

	// M1-3: execution state must not leak into shared engine assets.
	sv, err := archcheck.CheckSharedFields(*root, ".", "sharedAssets", archcheck.SharedStateTypes, archcheck.SharedFuncExempt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	violations = append(violations, sv...)

	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "archcheck:", v.Message)
		}
		os.Exit(1)
	}
	fmt.Println("archcheck: import graph and shared assets match the allow lists")
}

func joinQuoted(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%q", s)
	}
	return out
}
