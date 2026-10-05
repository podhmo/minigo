// Command minigo-generate executes //minigo:generate directives without
// spawning processes: each directive names a package directory whose
// exported Main(args []string) int entry is invoked inside one shared
// minigo engine. Tools are interpreted, never built — and the engine's
// package caches are reused across directives.
//
//	minigo-generate [-n] [-x] [-v] [-run re] [dir]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:]))
}

func runMain(ctx context.Context, argv []string) int {
	fs := flag.NewFlagSet("minigo-generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dry := fs.Bool("n", false, "print directives without running them")
	echo := fs.Bool("x", false, "print each directive as it runs")
	verb := fs.Bool("v", false, "verbose file reporting")
	runRe := fs.String("run", "", "run only directives matching this regexp")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	var re *regexp.Regexp
	if *runRe != "" {
		var err error
		re, err = regexp.Compile(*runRe)
		if err != nil {
			fmt.Fprintln(os.Stderr, "minigo-generate:", err)
			return 2
		}
	}
	ds, err := scan(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "minigo-generate:", err)
		return 1
	}
	if *verb {
		fmt.Fprintf(os.Stderr, "minigo-generate: %d directive(s) in %s\n", len(ds), dir)
	}
	return run(ctx, dir, ds, re, *dry, *echo, os.Stdout)
}

// run executes directives sequentially on one engine. Each Engine.Call
// is an isolated process scope — a tool's panic or goroutines die with
// its call — while the shared package cache keeps the tool and scanned
// packages parsed and indexed exactly once.
func run(ctx context.Context, dir string, ds []directive, re *regexp.Regexp, dry, echo bool, out io.Writer) int {
	e := minigo.NewEngine(dir, minigo.WithWorkingDir(dir), minigo.WithOutput(out))
	failures := 0
	for _, d := range ds {
		if re != nil && !re.MatchString(d.text) {
			continue
		}
		if echo || dry {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", d.file, d.line, d.text)
		}
		if dry {
			continue
		}
		if err := execute(ctx, e, d); err != nil {
			fmt.Fprintf(os.Stderr, "%s:%d: %v\n", d.file, d.line, err)
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "minigo-generate: %d directive(s) failed\n", failures)
		return 1
	}
	return 0
}

// execute delivers the directive's file context through the environment
// — the go generate contract: a tool reads os.Getenv exactly as it
// would under go generate — then invokes the tool package's Main. The
// env is process-wide and rewritten per directive; sequential
// execution is what keeps that race-free.
func execute(ctx context.Context, e *minigo.Engine, d directive) error {
	refDir := d.ref
	if !filepath.IsAbs(refDir) {
		// refs anchor at the file holding the directive, like
		// go:embed paths anchor at the package dir.
		refDir = filepath.Join(filepath.Dir(d.file), refDir)
	}
	// the env is process-wide: swap the file context in for the call's
	// duration and put the caller's values back when it ends.
	restore := setEnv(map[string]string{
		"GOFILE":     filepath.Base(d.file),
		"GOFILEPATH": d.file,
		"GOLINE":     strconv.Itoa(d.line),
		"GOPACKAGE":  d.pkg,
	})
	defer restore()
	// the []string arg crosses the boundary as a script slice: raw host
	// slices are not auto-converted for Call (scalars are), so box it.
	elems := make([]runtime.Value, len(d.args))
	for i, a := range d.args {
		elems[i] = a
	}
	res, err := e.Run(ctx, refDir, "Main", &runtime.Slice{Elems: elems})
	if err != nil {
		return err
	}
	switch n := res.(type) {
	case nil:
		return nil // a void Main is success
	case int:
		if n != 0 {
			return fmt.Errorf("tool exited %d", n)
		}
	case int64:
		if n != 0 {
			return fmt.Errorf("tool exited %d", n)
		}
	default:
		return fmt.Errorf("unexpected Main result %T", res)
	}
	return nil
}

// setEnv installs env vars and returns a function restoring the prior
// values (set or unset). One Call's context must not leak into the next
// directive — or back out to the host process.
func setEnv(vars map[string]string) func() {
	type prior struct {
		val string
		ok  bool
	}
	saved := make(map[string]prior, len(vars))
	for k, v := range vars {
		old, ok := os.LookupEnv(k)
		saved[k] = prior{old, ok}
		os.Setenv(k, v)
	}
	return func() {
		for k, p := range saved {
			if p.ok {
				os.Setenv(k, p.val)
			} else {
				os.Unsetenv(k)
			}
		}
	}
}
