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
	"io"
	"os"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
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
	if fs.NArg() > 1 {
		// `./app -check` puts -check in the positional args too —
		// silently dropping it would run a *write* where a check was
		// meant. Refuse rather than guess.
		fmt.Fprintf(os.Stderr, "gen-sync: unexpected extra arguments: %s\n", strings.Join(fs.Args()[1:], " "))
		return 2
	}
	dir := "./app"
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	n, err := run(ctx, ".", "./script", dir, *check, *deps, os.Stdout)
	if err != nil {
		// errors from the script already carry their own "gen-sync:"
		// prefix — adding another would print "gen-sync: gen-sync: ..."
		fmt.Fprintln(os.Stderr, err)
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
// rooted at engineDir. The script returns (changed, error): files it
// could not sync ride the error so a failed file never reads as
// "nothing to do".
func run(ctx context.Context, engineDir, scriptDir, dir string, check, deps bool, out io.Writer) (int, error) {
	e := minigo.NewEngine(engineDir, minigo.WithOutput(out))
	res, err := e.Run(ctx, scriptDir, "Main", dir, check, deps)
	if err != nil {
		return 0, err
	}
	tup, ok := res.(*runtime.Tuple)
	if !ok {
		return 0, fmt.Errorf("gen-sync: unexpected result type %T (want (int, error))", res)
	}
	if len(tup.Elems) != 2 {
		return 0, fmt.Errorf("gen-sync: unexpected result arity %d (want (int, error))", len(tup.Elems))
	}
	n, err := intResult(tup.Elems[0])
	if err != nil {
		return 0, err
	}
	return n, errorResult(tup.Elems[1])
}

func intResult(v runtime.Value) (int, error) {
	switch n := runtime.Unwrap(v).(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	}
	return 0, fmt.Errorf("gen-sync: unexpected result type %T (want int)", v)
}

func errorResult(v runtime.Value) error {
	e := runtime.Unwrap(v)
	if e == nil || e == runtime.NIL {
		return nil
	}
	if gv, ok := e.(*runtime.GoValue); ok {
		if err, ok := gv.V.(error); ok {
			return err
		}
	}
	return fmt.Errorf("gen-sync: unexpected error result %T", v)
}
