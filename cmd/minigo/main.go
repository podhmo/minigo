// Command minigo runs a function in a package directory, or an
// interactive session.
//
//	minigo run ./path/to/pkg [--entry FuncName]
//	minigo ./path/to/pkg [FuncName]   # shorthand for run
//	minigo repl                       # interactive session
//	minigo vet <ref> [--special import/path.Sym]...  # list unregistered stub calls
//	minigo gen-intrinsics -output <dir> <pkg>...     # emit a Bind table
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

func main() {
	ctx := context.Background()
	args := os.Args[1:]
	if len(args) < 1 {
		usage()
	}
	var err error
	switch args[0] {
	case "repl":
		err = runREPL(ctx, os.Stdin, os.Stdout)
	case "run":
		err = run(ctx, args[1:])
	case "vet":
		err = vet(ctx, args[1:])
	case "gen-intrinsics":
		err = genIntrinsics(ctx, args[1:])
	default:
		// shorthand: `minigo <ref> [func]`
		err = run(ctx, args)
	}
	if err != nil {
		slog.ErrorContext(ctx, "minigo", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  minigo run <dir-or-importpath> [--entry Func]
  minigo <dir-or-importpath> [Func]
  minigo repl
  minigo vet <dir-or-importpath> [--special import/path.Sym]...
  minigo gen-intrinsics -output <dir> <dir-or-importpath>...`)
	os.Exit(1)
}

func run(ctx context.Context, args []string) error {
	// extract -entry/--entry anywhere: Go's flag package stops at the
	// first positional, but `minigo run ./pkg --entry F` should work
	var entry string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--entry" || a == "-entry":
			i++
			if i >= len(args) {
				return fmt.Errorf("--entry requires a function name")
			}
			entry = args[i]
		case strings.HasPrefix(a, "--entry="), strings.HasPrefix(a, "-entry="):
			entry = strings.SplitN(a, "=", 2)[1]
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q (supported: --entry)", a)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 1 {
		usage()
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
	e := minigo.NewEngine(cwd, minigo.WithOutput(os.Stdout))
	r, err := e.Run(ctx, ref, fn)
	if err != nil {
		return err
	}
	if r != nil {
		fmt.Printf("%v\n", r)
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
				return fmt.Errorf("--special requires an import/path.Sym argument")
			}
			specials = append(specials, args[i])
		case strings.HasPrefix(a, "--special="), strings.HasPrefix(a, "-special="):
			specials = append(specials, strings.SplitN(a, "=", 2)[1])
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q (supported: --special)", a)
		default:
			if ref != "" {
				return fmt.Errorf("vet takes exactly one package reference")
			}
			ref = a
		}
	}
	if ref == "" {
		usage()
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
			return fmt.Errorf("--special wants import/path.Sym, got %q", s)
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
  :ls [ref]  list top-level decls of the current (or given) package
  :exit   quit (also :quit, :q, Ctrl-D)
input is a top-level declaration or statements; a trailing
expression is printed. new names introduced by := / var / const
persist as globals across lines. a line ending inside an open
() [] {} group (or after an operator) continues with a ".. "
prompt until it closes.`

func runREPL(ctx context.Context, in io.Reader, out io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := minigo.NewEngine(cwd, minigo.WithOutput(out))
	r := e.NewREPL()
	fmt.Fprintln(out, "minigo repl (:help for commands)")
	sc := bufio.NewScanner(in)
	var frag strings.Builder
	for {
		if frag.Len() == 0 {
			fmt.Fprint(out, ">> ")
		} else {
			fmt.Fprint(out, ".. ")
		}
		if !sc.Scan() {
			if frag.Len() > 0 {
				// EOF mid-fragment: surface the parse error rather
				// than dropping the input silently.
				if _, err := r.EvalLine(ctx, frag.String()); err != nil {
					fmt.Fprintf(out, "error: %s\n", err)
				}
			}
			fmt.Fprintln(out)
			return sc.Err()
		}
		text := sc.Text()
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
		if err != nil {
			fmt.Fprintf(out, "error: %s\n", err)
			continue
		}
		if d := r.Display(v); d != nil {
			fmt.Fprintf(out, "%v\n", d)
		}
	}
}
