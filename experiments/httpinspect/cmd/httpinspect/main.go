package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/experiments/httpinspect"
	"github.com/podhmo/minigo/inspect"
)

func main() {
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: httpinspect package-path function")
		os.Exit(2)
	}
	e := minigo.NewEngine(".")
	p, err := e.Package(context.Background(), flag.Arg(0))
	if err != nil {
		fail(err)
	}
	if p.Index == nil {
		fail(fmt.Errorf("package has no source index"))
	}
	d := p.Index.Funcs[flag.Arg(1)]
	if d == nil {
		fail(fmt.Errorf("source function not found: %s", flag.Arg(1)))
	}
	report, err := httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, d), httpinspect.Limits{})
	if err != nil {
		fail(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
