// Command gen-sync keeps //go:generate directives in sync with the
// declarations that want generation tooling. The scanning and rewriting
// logic is a minigo script (./script); this host only parses flags and
// drives the interpreter.
//
//	gen-sync [-check] [-deps] [dir]
//
//	-check  report drift instead of writing (for CI)
//	-deps   also follow same-module imports transitively
//	dir     package directory to scan (default ./app)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/podhmo/minigo"
)

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:]))
}

func runMain(ctx context.Context, argv []string) int {
	fs := flag.NewFlagSet("gen-sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "report drift without writing (for CI)")
	deps := fs.Bool("deps", false, "follow same-module imports transitively")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	dir := "./app"
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	n, err := run(ctx, ".", "./script", dir, *check, *deps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-sync:", err)
		return 1
	}
	if *check {
		if n > 0 {
			fmt.Fprintf(os.Stderr, "%d file(s) out of sync\n", n)
			return 1
		}
		fmt.Println("gen-sync: up to date")
		return 0
	}
	fmt.Printf("%d file(s) updated\n", n)
	return 0
}

// run executes script.Main(dir, check, deps) through a minigo engine
// rooted at engineDir.
func run(ctx context.Context, engineDir, scriptDir, dir string, check, deps bool) (int, error) {
	e := minigo.NewEngine(engineDir, minigo.WithOutput(os.Stdout))
	res, err := e.Run(ctx, scriptDir, "Main", dir, check, deps)
	if err != nil {
		return 0, err
	}
	switch n := res.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	}
	return 0, fmt.Errorf("unexpected result type %T", res)
}
