// Command extract-schema prints a JSON Schema for a Go struct type — or for
// every type in a package — by walking declarations through the minigo
// inspect API. The scan and generation are a minigo script (./script);
// this host only parses flags, runs plugin packages, and drives the
// interpreter.
//
//	extract-schema [flags] <target> [Type]
//
//	-pkg       target is an import path instead of a directory
//	-plugin    a script package dir providing Overrides() (repeatable)
//	-stdlib    leaf|walk — stop at standard-library types or walk them (default leaf)
//	-loose     emit additionalProperties:true on object schemas (default true)
//	-title     title attribute of the schema document
//	-desc      description attribute of the schema document
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

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:]))
}

func runMain(ctx context.Context, argv []string) int {
	fs := flag.NewFlagSet("extract-schema", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pkgMode := fs.Bool("pkg", false, "target is an import path instead of a directory")
	var plugins stringList
	fs.Var(&plugins, "plugin", "script package dir providing Overrides() (repeatable)")
	stdlib := fs.String("stdlib", "leaf", "leaf|walk: stop at standard-library types or walk them")
	loose := fs.Bool("loose", true, "emit additionalProperties:true on object schemas")
	title := fs.String("title", "", "title attribute of the schema document")
	desc := fs.String("desc", "", "description attribute of the schema document")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	switch *stdlib {
	case "leaf", "walk":
	default:
		fmt.Fprintf(os.Stderr, "extract-schema: -stdlib must be leaf or walk, got %q\n", *stdlib)
		return 2
	}
	if n := fs.NArg(); n < 1 || n > 2 {
		fmt.Fprintln(os.Stderr, "usage: extract-schema [flags] <target> [Type]")
		return 2
	}
	target := fs.Arg(0)
	typeName := ""
	if fs.NArg() == 2 {
		typeName = fs.Arg(1)
	}
	doc, err := run(ctx, ".", "./script", plugins, target, *pkgMode, typeName, *stdlib, *loose, *title, *desc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(doc)
	return 0
}

// run executes script.Main through a minigo engine rooted at engineDir.
// Plugin dirs are simply handed to the script — each is itself a
// script package exposing Config() (overrides + description filters
// under one entry point), which the walk loops over and merges inside
// the same VM. The script returns the schema document as a string.
func run(ctx context.Context, engineDir, scriptDir string, plugins []string, target string, pkgMode bool, typeName, stdlib string, loose bool, title, description string) (string, error) {
	e := minigo.NewEngine(engineDir, minigo.WithOutput(io.Discard))
	res, err := e.Run(ctx, scriptDir, "Main", target, pkgMode, typeName, stdlib, loose, plugins, title, description)
	if err != nil {
		return "", err
	}
	tup, ok := res.(*runtime.Tuple)
	if !ok || len(tup.Elems) != 2 {
		return "", fmt.Errorf("extract-schema: unexpected result %T (want (string, error))", res)
	}
	doc, ok := runtime.Unwrap(tup.Elems[0]).(string)
	if !ok {
		return "", fmt.Errorf("extract-schema: unexpected result type %T (want string)", tup.Elems[0])
	}
	return doc, errorResult(tup.Elems[1])
}

// errorResult unwraps the script's error slot: nil stays nil, a boxed
// Go error surfaces as the run's error.
func errorResult(v runtime.Value) error {
	e := runtime.Unwrap(v)
	if e == nil || e == runtime.NIL {
		return nil
	}
	if runtime.IfaceTaggedNil(e) != nil {
		return nil
	}
	if gv, ok := e.(*runtime.GoValue); ok {
		if err, ok := gv.V.(error); ok {
			return err
		}
	}
	return fmt.Errorf("extract-schema: unexpected error result %T", v)
}
