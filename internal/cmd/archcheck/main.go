// Command archcheck checks the module's internal import graph, shared
// assets and file sizes against the allow lists in
// internal/archcheck/rules.go. It is a ratchet: today the lists pin the
// current state; later phases tighten it.
//
//	go run ./internal/cmd/archcheck                # check everything
//	go run ./internal/cmd/archcheck -print         # print the live import graph in rules.go form
//	go run ./internal/cmd/archcheck -print-sizes   # print live file sizes in rules.go form
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
	printSizes := flag.Bool("print-sizes", false, "print live file line counts in rules.go form and exit")
	flag.Parse()

	if *printSizes {
		sizes, err := archcheck.Sizes(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "archcheck:", err)
			os.Exit(2)
		}
		var paths []string
		for p := range sizes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Printf("\t%q: %d,\n", p, sizes[p])
		}
		return
	}

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

	// M1-4: files may only shrink.
	sizes, err := archcheck.Sizes(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	violations = append(violations, archcheck.CheckSizes(sizes, archcheck.FileBudgets)...)

	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "archcheck:", v.Message)
		}
		os.Exit(1)
	}
	fmt.Println("archcheck: import graph, shared assets and file sizes match the allow lists")
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
