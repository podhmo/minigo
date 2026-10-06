// Command gen-sync keeps //go:generate directives in sync with the
// declarations that want generation tooling. The scanning and rewriting
// logic is a minigo script (./script); this host only parses flags and
// drives the interpreter.
//
//	gen-sync [-check] [-deps] [-explain] [dir]
//
//	-check    report drift instead of writing (for CI)
//	-deps     also follow same-module imports transitively
//	-explain  print why each directive was inferred
//	dir       package directory to scan (default ./app)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	explain := fs.Bool("explain", false, "print why each directive was inferred")
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
	n, err := run(ctx, ".", "./script", dir, *check, *deps, *explain, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, describeFailure(err, dir, "./script"))
		if *check {
			// a real failure is not drift — keep exit 1 for "found
			// files out of sync" alone so CI can tell the reports apart.
			return 2
		}
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

// run executes script.Main(dir, check, deps, explain) through a minigo
// engine rooted at engineDir. The script returns (changed, error): files
// it could not sync ride the error so a failed file never reads as
// "nothing to do".
func run(ctx context.Context, engineDir, scriptDir, dir string, check, deps, explain bool, out io.Writer) (int, error) {
	e := minigo.NewEngine(engineDir, minigo.WithOutput(out))
	res, err := e.Run(ctx, scriptDir, "Main", dir, check, deps, explain)
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
	if runtime.IfaceTaggedNil(e) != nil {
		// a nil interface value returned in the error slot is a nil
		// error (IfaceNil{error} is what `return nil` now binds); a
		// boxed typed nil stays a non-nil error.
		return nil
	}
	if gv, ok := e.(*runtime.GoValue); ok {
		if err, ok := gv.V.(error); ok {
			return err
		}
	}
	return fmt.Errorf("gen-sync: unexpected error result %T", v)
}

// describeFailure renders a run failure with a first line naming who
// must act, in the vocabulary of the invocation and the scanned input:
// the dir argument, an input file at a position, a dependency, or
// gen-sync itself. A *runtime.Trap is unwrapped to the engine error it
// carries so the classification sees the real cause (a resolve/parse
// failure), not the "runtime trap" wrapper. Errors returned by the
// script already carry their own "gen-sync:" vocabulary and pass
// through untouched.
func describeFailure(err error, dir, scriptDir string) string {
	u := err
	var trap *runtime.Trap
	if errors.As(err, &trap) && trap.Err != nil {
		u = trap.Err
	}
	msg := u.Error()
	blame := "this looks like a gen-sync bug"
	switch {
	case strings.HasPrefix(msg, "gen-sync:"):
		return msg
	case errors.Is(u, os.ErrPermission) || strings.Contains(msg, "permission denied"):
		// a filesystem refusal stays the root cause however many
		// wrappers sit on top — resolve dir %q / resolve %q / import %s
		// wrap the package-dir read failure, so classifying the outer
		// prefix alone mislabels a permissions problem as a bad dir
		// argument.
		blame = "check the named file or directory's permissions"
	case strings.HasPrefix(msg, "resolve dir "):
		// loadDir on the dir argument (inspect.DirOf in the script) —
		// anything else is the tool's own script dir.
		if quotedArg(msg, "resolve dir ") == dir {
			blame = "fix the dir argument"
		}
	case strings.HasPrefix(msg, "resolve "), strings.Contains(msg, "could not be resolved"):
		blame = "fix the package's imports or the module setup"
	case strings.HasPrefix(msg, "parse "):
		target := parseTarget(msg)
		switch {
		case pathInside(dir, target):
			blame = "fix the input file at the reported position"
		case pathInside(scriptDir, target):
			// the tool's own script is broken — not the user's input
		default:
			blame = "fix a dependency file at the reported position"
		}
	}
	return "gen-sync: " + blame + "\n" + err.Error()
}

// quotedArg extracts the %q-quoted operand following prefix in an
// engine error message (e.g. `resolve dir "./app": ...` → "./app").
// The operand is unquoted — a dir argument containing a quote or an
// escape sequence still compares equal to itself raw.
func quotedArg(msg, prefix string) string {
	rest := strings.TrimPrefix(msg, prefix)
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	for i := 1; i < len(rest); i++ {
		switch rest[i] {
		case '\\':
			i++ // escaped byte: skip the pair
		case '"':
			if s, err := strconv.Unquote(rest[:i+1]); err == nil {
				return s
			}
			return ""
		}
	}
	return ""
}

// parseTarget extracts the file a `parse <file>: <error>` message
// failed on — the text up to the first ": " boundary, which is where
// the parser's own "<file>:<line>:<col>" report begins.
func parseTarget(msg string) string {
	rest := strings.TrimPrefix(msg, "parse ")
	if i := strings.Index(rest, ": "); i >= 0 {
		return rest[:i]
	}
	return ""
}

// pathInside reports whether path resolves inside dir.
func pathInside(dir, path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
