// Command test-detect lists the packages affected by a set of changed
// .go files, so CI can run only the tests a change can break.
//
// It builds the repository's internal import graph by parsing every .go
// file with parser.ImportsOnly — no go list, no module resolution, no
// network — then walks reverse dependencies from the changed files.
//
//	test-detect [flags] [changed.go ...]
//	git diff --name-only | test-detect -stdin
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:]))
}

type regexList []*regexp.Regexp

func (l *regexList) String() string { return fmt.Sprint([]*regexp.Regexp(*l)) }
func (l *regexList) Set(s string) error {
	re, err := regexp.Compile(s)
	if err != nil {
		return err
	}
	*l = append(*l, re)
	return nil
}

func runMain(ctx context.Context, argv []string) int {
	fs := flag.NewFlagSet("test-detect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "repository root to scan")
	format := fs.String("format", "pkg", "output format: pkg|space|dir|json")
	stdin := fs.Bool("stdin", false, "read changed file paths from stdin (one per line)")
	includeUntested := fs.Bool("include-untested", false, "also list packages without _test.go files")
	verbose := fs.Bool("verbose", false, "report scan stats on stderr")
	var excludes regexList
	fs.Var(&excludes, "exclude", "regexp matching import paths to drop from output (repeatable)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	changed := fs.Args()
	if *stdin || len(changed) == 0 {
		lines, err := readLines(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "test-detect: reading stdin:", err)
			return 1
		}
		changed = append(changed, lines...)
	}

	absRoot, err := absPath(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-detect:", err)
		return 1
	}
	if info, err := os.Stat(absRoot); err != nil {
		fmt.Fprintf(os.Stderr, "test-detect: -root %s: %v\n", *root, err)
		return 1
	} else if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "test-detect: -root %s is not a directory\n", *root)
		return 1
	}
	// The tree walk uses Lstat and never descends through a symlink, so
	// a symlinked -root would scan nothing and report "no go.mod found".
	// Resolve it up front.
	if resolved, err := filepath.EvalSymlinks(absRoot); err != nil {
		fmt.Fprintf(os.Stderr, "test-detect: -root %s: %v\n", *root, err)
		return 1
	} else {
		absRoot = resolved
	}

	d, err := detectChanged(absRoot, changed, options{
		includeUntested: *includeUntested,
		exclude:         excludes,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-detect:", err)
		return 1
	}

	for _, w := range d.warnings {
		fmt.Fprintln(os.Stderr, "test-detect: warning:", w)
	}
	if *verbose {
		s := d.stats
		fmt.Fprintf(os.Stderr, "test-detect: %d module(s), %d file(s), %d edge(s), %d affected (%d dropped) in %s\n",
			s.modules, s.files, s.edges, len(d.packages), len(d.dropped), s.elapsed)
		for _, p := range d.dropped {
			fmt.Fprintf(os.Stderr, "test-detect: dropped %s\n", p.importPath)
		}
	}

	out, err := d.render(*format)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-detect:", err)
		return 2
	}
	_, _ = os.Stdout.Write(out)
	return 0
}

func readLines(f *os.File) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func absPath(p string) (string, error) {
	return filepath.Abs(p)
}
