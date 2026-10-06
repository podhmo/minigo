// Command minigo runs a function in a package directory, or an
// interactive session.
//
//	minigo run ./path/to/pkg [--entry FuncName]
//	minigo ./path/to/pkg [FuncName]   # shorthand for run
//	minigo repl                       # interactive session
//	minigo vet <ref> [--special import/path.Sym]...  # list unregistered stub calls
//	minigo gen-intrinsics -output <dir> <pkg>...     # emit a Bind table
//	minigo help [command]                            # usage (also -h)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
	"golang.org/x/term"
)

func main() {
	ctx := context.Background()
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, helps[""])
		os.Exit(2)
	}
	cmd := args[0]
	var err error
	switch cmd {
	case "help", "-h", "-help", "--help":
		topic := ""
		if cmd == "help" && len(args) > 1 {
			topic = args[1]
		}
		h, ok := helps[topic]
		if !ok {
			fmt.Fprintf(os.Stderr, "minigo help: unknown command %q\n\n%s", topic, helps[""])
			os.Exit(2)
		}
		fmt.Print(h)
		return
	case "repl":
		err = replArgs(args[1:])
		if err == nil {
			err = runREPL(ctx, os.Stdin, os.Stdout)
		}
	case "run":
		err = run(ctx, args[1:])
	case "vet":
		err = vet(ctx, args[1:])
	case "gen-intrinsics":
		err = genIntrinsics(ctx, args[1:])
	default:
		// shorthand: `minigo <ref> [func]`
		cmd = "run"
		err = run(ctx, args)
	}
	switch {
	case err == nil:
	case errors.Is(err, errHelp):
		fmt.Print(helps[cmd])
	case errors.As(err, new(*usageError)):
		fmt.Fprintf(os.Stderr, "minigo %s: %v\n\n%s", cmd, err, helps[cmd])
		os.Exit(2)
	default:
		// the error carries a multi-line traceback: print it as is, like
		// go run prints a panic, rather than escaped inside a log record.
		fmt.Fprintf(os.Stderr, "minigo: %v\n", err)
		os.Exit(1)
	}
}

// errHelp is returned by a subcommand's argument parser on -h/--help:
// main prints that subcommand's help to stdout and exits 0. A
// *usageError is an argument error: main prints it with the
// subcommand's help to stderr and exits 2.
var errHelp = errors.New("help requested")

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func isHelpFlag(a string) bool {
	return a == "-h" || a == "-help" || a == "--help"
}

// helps maps a command name to its help text; "" is the overview.
var helps = map[string]string{
	"": `minigo runs Go source lazily, without a build step.

usage:
  minigo run <ref> [Func] [flags] [-- script args...]
  minigo <ref> [Func] [-- script args...]    shorthand for run
  minigo repl
  minigo vet <ref> [--special import/path.Sym]...
  minigo gen-intrinsics -output <dir> <ref>...
  minigo help [command]

<ref> is a package directory (., ./dir, /abs/dir) or an import path
resolved from the current directory's module.

Run 'minigo help <command>' or 'minigo <command> -h' for details.
`,
	"run": `usage: minigo run <ref> [Func] [flags] [-- script args...]
       minigo <ref> [Func] [-- script args...]

Run Func (default: main) in the package <ref> and print its result
unless it returns nothing. Arguments after -- become the script's
os.Args[1:]; os.Args[0] is <ref>.

flags (accepted before or after <ref>):
  --entry Func     function to run (same as the positional Func)
  --deny pkg,...   make importing these packages fail with a clear error
  --src pkg,...    interpret these packages from source even where a
                   host-bound version exists
  -h, --help       show this help
`,
	"repl": `usage: minigo repl

Start an interactive session over a scratch <repl> package. Type :help
inside the session for its commands.
`,
	"vet": `usage: minigo vet <ref> [--special import/path.Sym]...

List calls in <ref> to stub declarations (body: panic("minigo intrinsic"))
that are neither bound nor registered as special forms. Exits 1 if any
are found.

flags:
  --special import/path.Sym   treat Sym as registered, as an embedding
                              app would via RegisterSpecial (repeatable)
  -h, --help                  show this help
`,
	"gen-intrinsics": `usage: minigo gen-intrinsics -output <dir> <ref>...

Emit a Go file per package under <dir> with a Bind(*minigo.Engine)
function that registers the package's exported members as host-bound
symbols.

flags:
  -output <dir>   directory to write the generated files to (required)
  -h, --help      show this help
`,
}

func run(ctx context.Context, args []string) error {
	// `--` separates script args: `minigo run dir -- -x v` runs dir's
	// main with os.Args = [dir, -x, v].
	var scriptArgs []string
	for i, a := range args {
		if a == "--" {
			scriptArgs = args[i+1:]
			args = args[:i]
			break
		}
	}
	// extract -entry/--entry and package-mode flags anywhere: Go's flag
	// package stops at the first positional, but `minigo run ./pkg
	// --entry F` should work
	var entry string
	var rest []string
	var denys, srcs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--entry" || a == "-entry":
			i++
			if i >= len(args) {
				return usageErrorf("--entry requires a function name")
			}
			entry = args[i]
		case strings.HasPrefix(a, "--entry="), strings.HasPrefix(a, "-entry="):
			entry = strings.SplitN(a, "=", 2)[1]
		case a == "--deny" || a == "-deny":
			i++
			if i >= len(args) {
				return usageErrorf("--deny requires an import path")
			}
			denys = append(denys, strings.Split(args[i], ",")...)
		case strings.HasPrefix(a, "--deny="), strings.HasPrefix(a, "-deny="):
			denys = append(denys, strings.Split(strings.SplitN(a, "=", 2)[1], ",")...)
		case a == "--src" || a == "-src":
			i++
			if i >= len(args) {
				return usageErrorf("--src requires an import path")
			}
			srcs = append(srcs, strings.Split(args[i], ",")...)
		case strings.HasPrefix(a, "--src="), strings.HasPrefix(a, "-src="):
			srcs = append(srcs, strings.Split(strings.SplitN(a, "=", 2)[1], ",")...)
		case isHelpFlag(a):
			return errHelp
		case strings.HasPrefix(a, "-"):
			return usageErrorf("unknown flag %q", a)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 1 {
		return usageErrorf("missing package reference")
	}
	ref := rest[0]
	fn := entry
	if fn == "" && len(rest) > 1 {
		fn = rest[1]
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	modes := map[string]minigo.PackageMode{}
	for _, p := range denys {
		modes[p] = minigo.ModeDeny
	}
	for _, p := range srcs {
		modes[p] = minigo.ModeSource
	}
	opts := []minigo.Option{
		minigo.WithOutput(os.Stdout),
		minigo.WithArgs(append([]string{ref}, scriptArgs...)),
	}
	if len(modes) > 0 {
		opts = append(opts, minigo.WithPackageModes(modes))
	}
	e := minigo.NewEngine(cwd, opts...)
	r, err := e.Run(ctx, ref, fn)
	if err != nil {
		return err
	}
	// a void main returns the nil runtime value — print nothing.
	if r != nil && r != runtime.NIL {
		fmt.Printf("%v\n", minigo.Format(r))
	}
	return nil
}

func vet(ctx context.Context, args []string) error {
	var ref string
	var specials []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--special" || a == "-special":
			i++
			if i >= len(args) {
				return usageErrorf("--special requires an import/path.Sym argument")
			}
			specials = append(specials, args[i])
		case strings.HasPrefix(a, "--special="), strings.HasPrefix(a, "-special="):
			specials = append(specials, strings.SplitN(a, "=", 2)[1])
		case isHelpFlag(a):
			return errHelp
		case strings.HasPrefix(a, "-"):
			return usageErrorf("unknown flag %q", a)
		default:
			if ref != "" {
				return usageErrorf("vet takes exactly one package reference")
			}
			ref = a
		}
	}
	if ref == "" {
		return usageErrorf("missing package reference")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := minigo.NewEngine(cwd)
	// the CLI has no embedding app to register specials for it, so the
	// caller declares intercepted symbols explicitly: --special
	// import/path.Name marks that member as registered for this check.
	for _, s := range specials {
		dot := strings.LastIndex(s, ".")
		if dot <= 0 || dot == len(s)-1 {
			return usageErrorf("--special wants import/path.Sym, got %q", s)
		}
		e.RegisterSpecial(runtime.SymbolID{PackagePath: s[:dot], Name: s[dot+1:]},
			func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
				return nil, fmt.Errorf("unreachable: vet-only special")
			})
	}
	findings, err := e.Vet(ctx, ref)
	if err != nil {
		return err
	}
	for _, f := range findings {
		fmt.Fprintln(os.Stderr, f)
	}
	if len(findings) > 0 {
		return fmt.Errorf("%d unregistered stub call(s)", len(findings))
	}
	return nil
}

const replHelp = `commands:
  :help   show this help
  :reset  clear all definitions and values
  :cd <ref>  resolve names inside a package (path or ./dir); :cd alone
             shows the current package, :cd - leaves it
  :pin    write following declarations into the entered package's
          globals (monkey-patch: visible to every importer here)
  :unpin  stop writing into the package; decls land in <repl> again
  :ls [ref]  list top-level decls of the current (or given) package;
             ref is a path, ./dir, or a name bound by an import here
  :load <file|dir>  read a .go file (or a directory's files) into the
                    session: its decls become callable here, each file
                    keeps its own imports; again reloads, :load alone lists.
                    takes filesystem paths only — for a package by import
                    path, use import or :cd
  :doc <pkg>[.<sym>]  run go doc (pkg: imported name, "path", or path).
                      note: this is the Go toolchain's documentation, not
                      minigo's — a bound package may expose fewer symbols
                      (:ls <pkg> shows what minigo actually provides)
  :dump <expr>  evaluate expr and print it in Go syntax, like fmt's
                #v verb (type and field names, quoted strings, T(nil)) —
                for debugging nested values (also :p)
  :comp <text>  print completion candidates for a code fragment — the
                same candidates [TAB] offers while typing (on a terminal)
  :bindings [prefix]  list host-bound (native) import paths; :ls <path>
                      shows a bound package's symbols
  :exit   quit (also :quit, :q, Ctrl-D)
input is a top-level declaration or statements; a trailing
expression is printed, and the last three printed results are kept
as _1 (newest), _2, _3 (a multi-value result as a []any). new names introduced by := / var / const
persist as globals across lines. imports are ordinary Go syntax —
import "fmt" — plus directory forms import "./dir" or "/abs/dir"
(resolved eagerly, bound under the package's declared name). a
line ending inside an open () [] {} group (or after an operator)
continues with a ".. " prompt until it closes. on a real terminal
the input line is editable: arrows/Home/End move the cursor, Up/Down
recall history, and TAB completes code (and :command names).`

// replArgs rejects arguments to `minigo repl`, which takes none but -h.
func replArgs(args []string) error {
	for _, a := range args {
		if isHelpFlag(a) {
			return errHelp
		}
	}
	if len(args) > 0 {
		return usageErrorf("unexpected argument %q", args[0])
	}
	return nil
}

func runREPL(ctx context.Context, in io.Reader, out io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var src replSource = &scannerSource{sc: bufio.NewScanner(in), out: out}
	// On a real terminal, upgrade to a raw-mode line editor: cursor
	// keys and in-session history come from x/term, and TAB drives
	// (*REPL).Complete. Pipes keep the plain scanner path.
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if state, err := term.MakeRaw(int(f.Fd())); err == nil {
			defer func() { _ = term.Restore(int(f.Fd()), state) }()
			out = writeWithCRLF{out}
			t := term.NewTerminal(readWriter{f, out}, "")
			if path, err := defaultHistoryPath(); err == nil {
				t.History = newFileHistory(path, 1000)
			}
			src = &termSource{t: t}
		}
	}
	e := minigo.NewEngine(cwd, minigo.WithOutput(out))
	r := e.NewREPL()
	if ts, ok := src.(*termSource); ok {
		ts.t.AutoCompleteCallback = completerFor(r, ts.t)
	}
	fmt.Fprintln(out, "minigo repl (:help for commands)")
	var frag strings.Builder
	for {
		prompt := ">> "
		if frag.Len() > 0 {
			prompt = ".. "
		}
		text, err := src.next(prompt)
		if err != nil {
			if err == io.EOF {
				if frag.Len() > 0 {
					// EOF mid-fragment: surface the parse error rather
					// than dropping the input silently.
					if _, err := r.EvalLine(ctx, frag.String()); err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
					}
				}
				fmt.Fprintln(out)
				return nil
			}
			return err
		}
		if frag.Len() == 0 {
			line := strings.TrimSpace(text)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, ":") {
				cmd, arg, _ := strings.Cut(line, " ")
				switch cmd {
				case ":exit", ":quit", ":q":
					return nil
				case ":reset":
					r.Reset()
					fmt.Fprintln(out, "state cleared")
				case ":cd":
					arg = strings.TrimSpace(arg)
					switch {
					case arg == "":
						if p := r.Current(); p != nil {
							mode := ""
							if r.Pinned() {
								mode = " [pin]"
							}
							fmt.Fprintf(out, "%s (%s)%s\n", p.Name, p.Path, mode)
						} else {
							fmt.Fprintln(out, "<repl>")
						}
					case arg == "-":
						if err := r.Leave(); err != nil {
							fmt.Fprintf(out, "error: %s\n", err)
						} else {
							fmt.Fprintln(out, "<repl>")
						}
					default:
						p, err := r.Enter(ctx, arg)
						if err != nil {
							fmt.Fprintf(out, "error: %s\n", err)
						} else {
							fmt.Fprintf(out, "%s (%s)\n", p.Name, p.Path)
						}
					}
				case ":pin":
					if err := r.Pin(); err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
					} else {
						fmt.Fprintln(out, "write mode on: declarations land in the package")
					}
				case ":unpin":
					r.Unpin()
					fmt.Fprintln(out, "write mode off")
				case ":ls":
					lines, err := r.List(ctx, strings.TrimSpace(arg))
					if err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
					} else {
						for _, l := range lines {
							fmt.Fprintln(out, l)
						}
					}
				case ":bindings":
					prefix := strings.TrimSpace(arg)
					for _, path := range r.BoundPackages() {
						if strings.HasPrefix(path, prefix) {
							fmt.Fprintln(out, path)
						}
					}
				case ":load":
					arg = strings.TrimSpace(arg)
					if arg == "" {
						for _, origin := range r.Loaded() {
							fmt.Fprintln(out, origin)
						}
						break
					}
					paths, err := r.Load(ctx, arg)
					for _, w := range r.Warnings() {
						fmt.Fprintf(out, "warning: %s\n", w)
					}
					if err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
						break
					}
					for _, path := range paths {
						if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
							path = rel
						}
						fmt.Fprintf(out, "loaded %s\n", path)
					}
				case ":doc":
					runDoc(ctx, out, r, cwd, arg)
				case ":dump", ":p":
					arg = strings.TrimSpace(arg)
					if arg == "" {
						fmt.Fprintln(out, "usage: :dump <expr>")
						break
					}
					v, err := r.EvalLine(ctx, arg)
					for _, w := range r.Warnings() {
						fmt.Fprintf(out, "warning: %s\n", w)
					}
					if err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
						break
					}
					if d := r.Dump(v); d != nil {
						fmt.Fprintf(out, "%v\n", d)
					}
				case ":comp":
					for _, c := range r.Complete(strings.TrimSpace(arg)) {
						if c.Detail != "" {
							fmt.Fprintf(out, "%s\t%s\t%s\n", c.Kind, c.Name, c.Detail)
						} else {
							fmt.Fprintf(out, "%s\t%s\n", c.Kind, c.Name)
						}
					}
				case ":help":
					fmt.Fprintln(out, replHelp)
				default:
					fmt.Fprintf(out, "unknown command %q (see :help)\n", line)
				}
				continue
			}
		}
		if frag.Len() > 0 {
			frag.WriteByte('\n')
		}
		frag.WriteString(text)
		src := frag.String()
		if minigo.IncompleteInput(src) {
			continue
		}
		frag.Reset()
		v, err := r.EvalLine(ctx, src)
		for _, w := range r.Warnings() {
			fmt.Fprintf(out, "warning: %s\n", w)
		}
		if err != nil {
			fmt.Fprintf(out, "error: %s\n", err)
			continue
		}
		if d := r.Display(v); d != nil {
			fmt.Fprintf(out, "%v\n", d)
		}
	}
}
