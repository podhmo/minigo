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
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
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
  --cpuprofile f   write a CPU profile of the interpreter to f
                   (default: $MINIGO_CPUPROFILE)
  --memprofile f   write an allocation profile of the interpreter to f
                   at exit (default: $MINIGO_MEMPROFILE)
  -h, --help       show this help

The profiles are Go pprof profiles of minigo itself (read them with
go tool pprof); the env defaults let a wrapper script that runs minigo
be profiled without editing its command line.
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
	cpuProfile, memProfile := os.Getenv("MINIGO_CPUPROFILE"), os.Getenv("MINIGO_MEMPROFILE")
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
		case a == "--cpuprofile" || a == "-cpuprofile":
			i++
			if i >= len(args) {
				return usageErrorf("--cpuprofile requires a file name")
			}
			cpuProfile = args[i]
		case strings.HasPrefix(a, "--cpuprofile="), strings.HasPrefix(a, "-cpuprofile="):
			cpuProfile = strings.SplitN(a, "=", 2)[1]
		case a == "--memprofile" || a == "-memprofile":
			i++
			if i >= len(args) {
				return usageErrorf("--memprofile requires a file name")
			}
			memProfile = args[i]
		case strings.HasPrefix(a, "--memprofile="), strings.HasPrefix(a, "-memprofile="):
			memProfile = strings.SplitN(a, "=", 2)[1]
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
	stop, err := startProfiles(cpuProfile, memProfile)
	if err != nil {
		return err
	}
	e := minigo.NewEngine(cwd, opts...)
	r, err := e.Run(ctx, ref, fn)
	// profiles cover failed runs too (a trap, os.Exit(N)): the run is
	// over either way, and a slow failing script is worth profiling.
	if perr := stop(); err == nil {
		err = perr
	}
	if err != nil {
		return err
	}
	// a void main returns the nil runtime value — print nothing.
	if r != nil && r != runtime.NIL {
		fmt.Printf("%v\n", minigo.Format(r))
	}
	return nil
}

// startProfiles starts a CPU profile to cpuPath (if set) and returns a
// stop func that ends it and writes an allocation profile to memPath (if
// set).
func startProfiles(cpuPath, memPath string) (func() error, error) {
	var cpu *os.File
	if cpuPath != "" {
		f, err := os.Create(cpuPath)
		if err != nil {
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		cpu = f
	}
	return func() error {
		var errs []error
		if cpu != nil {
			pprof.StopCPUProfile()
			if err := cpu.Close(); err != nil {
				errs = append(errs, fmt.Errorf("cpuprofile: %w", err))
			}
		}
		if memPath != "" {
			if err := writeAllocProfile(memPath); err != nil {
				errs = append(errs, fmt.Errorf("memprofile: %w", err))
			}
		}
		return errors.Join(errs...)
	}, nil
}

func writeAllocProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	goruntime.GC() // settle the live-heap numbers, like go test -memprofile
	if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
  :unload <file|dir>  drop a :load: its decls and values go, and the
                      prompt definitions it replaced come back
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
				case ":unload":
					path, err := r.Unload(ctx, strings.TrimSpace(arg))
					if err != nil {
						fmt.Fprintf(out, "error: %s\n", err)
						break
					}
					if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
						path = rel
					}
					fmt.Fprintf(out, "unloaded %s\n", path)
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

// ---- REPL :doc command (go doc) ----

// docTarget maps a :doc argument to `go doc` arguments and the directory
// to run it in. Accepted spellings:
//
//	json / json.Unmarshal        — a name bound by a session import (alias too)
//	"encoding/json".Unmarshal    — a quoted import path, as in an import spec
//	encoding/json Unmarshal      — go doc's own two-argument form
//	encoding/json.Unmarshal      — anything else is handed to go doc as is
//
// Directory packages run go doc inside the directory (`go doc . Sym`) so
// the module that owns them supplies the context.
func docTarget(r *minigo.REPL, cwd, arg string) (dir string, args []string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil, fmt.Errorf("usage: :doc <pkg>[.<sym>[.<method>]]")
	}
	var pkg, sym string
	switch {
	case strings.HasPrefix(arg, `"`):
		quoted, err := strconv.QuotedPrefix(arg)
		if err != nil {
			return "", nil, fmt.Errorf("doc: bad quoted path: %w", err)
		}
		pkg, _ = strconv.Unquote(quoted)
		sym = strings.TrimLeft(arg[len(quoted):], ". ")
	case strings.Contains(arg, " "):
		pkg, sym, _ = strings.Cut(arg, " ")
		sym = strings.TrimSpace(sym)
	default:
		head, rest, _ := strings.Cut(arg, ".")
		path, ok := r.ImportPathOf(head)
		if !ok {
			return cwd, []string{arg}, nil
		}
		pkg, sym = path, rest
	}
	if path, ok := r.ImportPathOf(pkg); ok {
		pkg = path
	}
	dir = cwd
	if resolve.LooksLikeDir(pkg) {
		if !filepath.IsAbs(pkg) {
			pkg = filepath.Join(cwd, pkg)
		}
		dir, pkg = pkg, "."
	}
	args = []string{pkg}
	if sym != "" {
		args = append(args, sym)
	}
	return dir, args, nil
}

// runDoc prints `go doc` output for a :doc argument.
func runDoc(ctx context.Context, out io.Writer, r *minigo.REPL, cwd, arg string) {
	dir, args, err := docTarget(r, cwd, arg)
	if err != nil {
		fmt.Fprintf(out, "error: %s\n", err)
		return
	}
	cmd := exec.CommandContext(ctx, "go", append([]string{"doc"}, args...)...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	out.Write(b)
	if err != nil && len(b) == 0 {
		fmt.Fprintf(out, "error: %s\n", err)
	}
}

// ---- gen-intrinsics subcommand ----

// `minigo gen-intrinsics` emits a Bind table for a package's exported
// members: functions become WrapFunc reflect adapters, vars/consts become
// ValueOf snapshots, types become synthetic named-basic typedefs so
// `var x pkg.T` declares host-typed variables (their values stay boxed
// GoValues and pass through the host-boundary coerce). The generated
// file is ordinary Go — the embedding app calls install.Bind(engine).

var tmplGenIntrinsics = template.Must(template.New("install.go").Parse(`// Code generated by minigo gen-intrinsics. DO NOT EDIT.

package {{ .PackageName }}

import (
	"{{ .PackagePath }}"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// Bind registers the exported members of "{{ .PackagePath }}" on the engine.
func Bind(e *minigo.Engine) {
	e.Bind("{{ .PackagePath }}", map[string]runtime.Value{
{{- range .FuncSymbols }}
		"{{ . }}": minigo.WrapFunc("{{ $.PackagePath }}.{{ . }}", {{ $.PackageName }}.{{ . }}),
{{- end }}
{{- range .ValueSymbols }}
		"{{ . }}": minigo.ValueOf({{ $.PackageName }}.{{ . }}),
{{- end }}
{{- range .TypeSymbols }}
		"{{ . }}": &runtime.TypeDef{Name: "{{ . }}", Kind: runtime.KindNamedBasic, Pkg: &runtime.Package{Path: "{{ $.PackagePath }}", Name: "{{ $.PackageName }}"}},
{{- end }}
	})
}
`))

func genIntrinsics(ctx context.Context, args []string) error {
	var output string
	var refs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-output" || a == "--output":
			i++
			if i >= len(args) {
				return usageErrorf("-output requires a directory")
			}
			output = args[i]
		case isHelpFlag(a):
			return errHelp
		default:
			refs = append(refs, a)
		}
	}
	if output == "" {
		return usageErrorf("-output <dir> is required")
	}
	if len(refs) == 0 {
		return usageErrorf("at least one package is required")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := minigo.NewEngine(cwd)
	for _, ref := range refs {
		if err := genOne(ctx, e, output, ref); err != nil {
			return err
		}
	}
	return nil
}

func genOne(ctx context.Context, e *minigo.Engine, output, ref string) error {
	p, err := e.Package(ctx, ref)
	if err != nil {
		return fmt.Errorf("gen-intrinsics %s: %w", ref, err)
	}
	if p.Index == nil {
		return fmt.Errorf("gen-intrinsics %s: package has no source index (bound package?)", ref)
	}
	var funcs, values, types []string
	for _, d := range p.Index.Funcs {
		if d.Kind != index.FuncDecl || !ast.IsExported(d.Name) {
			continue
		}
		if tp := d.Func.Type.TypeParams; tp != nil && len(tp.List) > 0 {
			continue // generic functions have no single callable form
		}
		funcs = append(funcs, d.Name)
	}
	for _, d := range p.Index.Vars {
		if ast.IsExported(d.Name) {
			values = append(values, d.Name)
		}
	}
	for _, d := range p.Index.Consts {
		if ast.IsExported(d.Name) {
			values = append(values, d.Name)
		}
	}
	for name, ti := range p.Index.Types {
		if !ast.IsExported(name) {
			continue
		}
		if ts, ok := ti.Decl.Spec.(*ast.TypeSpec); ok && ts.TypeParams != nil && len(ts.TypeParams.List) > 0 {
			continue // generic types have no single reflect-free form
		}
		types = append(types, name)
	}
	sort.Strings(funcs)
	sort.Strings(values)
	sort.Strings(types)

	params := struct {
		PackageName  string
		PackagePath  string
		FuncSymbols  []string
		ValueSymbols []string
		TypeSymbols  []string
	}{p.Name, p.Path, funcs, values, types}

	dir := filepath.Join(output, filepath.FromSlash(p.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tmplGenIntrinsics.Execute(&buf, params); err != nil {
		return err
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("gen-intrinsics %s: formatting generated source: %w", ref, err)
	}
	name := filepath.Join(dir, "install.go")
	if err := os.WriteFile(name, src, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "generated", name)
	return nil
}

// ---- persistent REPL history ----

// Persistent REPL history: a term.History backed by a file
// (~/.minigo_history). Entries load at startup so Up/Down recall
// crosses sessions; each accepted line appends to the file.

// fileHistory implements term.History (index 0 = most recent) over a
// newline-separated file. File errors are ignored — history is
// best-effort, the in-memory list always works.
type fileHistory struct {
	path    string
	entries []string // chronological: oldest first
	max     int
}

// newFileHistory loads path (missing file is fine) and keeps the last
// max entries — rewriting the file down to the tail when it overflowed.
func newFileHistory(path string, max int) *fileHistory {
	h := &fileHistory{path: path, max: max}
	data, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			h.entries = append(h.entries, line)
		}
	}
	if max > 0 && len(h.entries) > max {
		h.entries = h.entries[len(h.entries)-max:]
		h.rewrite()
	}
	return h
}

// defaultHistoryPath is ~/.minigo_history — a plain dotfile like
// .python_history, deliberately not XDG state so it is easy to find.
func defaultHistoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".minigo_history"), nil
}

func (h *fileHistory) Len() int { return len(h.entries) }

// At indexes from most recent: At(0) is the last line entered.
func (h *fileHistory) At(idx int) string {
	return h.entries[len(h.entries)-1-idx]
}

func (h *fileHistory) Add(entry string) {
	if entry == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return // consecutive duplicates are noise, not history
	}
	h.entries = append(h.entries, entry)
	if h.max > 0 && len(h.entries) > h.max {
		h.entries = h.entries[1:]
		h.rewrite()
		return
	}
	if f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		fmt.Fprintln(f, entry)
	}
}

func (h *fileHistory) rewrite() {
	f, err := os.OpenFile(h.path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, e := range h.entries {
		fmt.Fprintln(f, e)
	}
}

// ---- line editing for minigo repl ----

// Line editing for `minigo repl`: on a real terminal the input loop
// upgrades to a raw-mode line editor (golang.org/x/term) so cursor keys,
// in-session history and tab completion work. Pipes and tests keep the
// plain bufio.Scanner path — no prompt escape sequences are emitted.

// replSource yields one input line per call; the prompt is decided by
// the caller (">> " vs the ".. " continuation).
type replSource interface {
	next(prompt string) (string, error)
}

// scannerSource is the plain path: print the prompt, scan a line.
type scannerSource struct {
	sc  *bufio.Scanner
	out io.Writer
}

func (s *scannerSource) next(prompt string) (string, error) {
	fmt.Fprint(s.out, prompt)
	if !s.sc.Scan() {
		if err := s.sc.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return s.sc.Text(), nil
}

// termSource hands the prompt to the line editor and returns edited
// lines. Ctrl-C and Ctrl-D both surface as io.EOF (x/term semantics).
type termSource struct {
	t *term.Terminal
}

func (s *termSource) next(prompt string) (string, error) {
	s.t.SetPrompt(prompt)
	return s.t.ReadLine()
}

// readWriter glues the input file and the output writer into the single
// ReadWriter a Terminal wants.
type readWriter struct {
	io.Reader
	io.Writer
}

// writeWithCRLF turns \n into \r\n — under raw mode a bare \n does not
// return the cursor to column 0, so every message the loop prints
// (and everything the interpreter echoes) needs the translation.
type writeWithCRLF struct {
	w io.Writer
}

func (w writeWithCRLF) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		todo := p
		if i >= 0 {
			todo = p[:i]
		}
		if len(todo) > 0 {
			if _, err := w.w.Write(todo); err != nil {
				return 0, err
			}
		}
		if i < 0 {
			break
		}
		if _, err := w.w.Write([]byte("\r\n")); err != nil {
			return 0, err
		}
		p = p[i+1:]
	}
	return n, nil
}

// metaCommands are the `:`-lines the front-end completes itself — the
// processor-side completer stays out of this namespace.
var metaCommands = []string{
	":bindings", ":cd", ":comp", ":doc", ":dump", ":exit", ":help", ":load", ":ls", ":p",
	":pin", ":q", ":quit", ":reset", ":unload", ":unpin",
}

// completerFor builds the AutoCompleteCallback: tab splices the common
// extension (or the whole match) into the line; an ambiguous press lists
// the candidates above the repainted prompt.
func completerFor(r *minigo.REPL, t *term.Terminal) func(line string, pos int, key rune) (string, int, bool) {
	return func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' {
			return "", 0, false
		}
		newLine, newPos, cands, ok := completeSplice(r, line, pos)
		if !ok {
			return "", 0, false
		}
		if len(cands) > 1 {
			t.Write([]byte("\n" + formatCandidates(cands) + "\n"))
		}
		return newLine, newPos, true
	}
}

// completeSplice computes the post-tab line: the shared extension of
// every candidate is inserted (a unique candidate completes fully), and
// the rest of the line past the cursor is preserved.
func completeSplice(r *minigo.REPL, line string, pos int) (newLine string, newPos int, cands []minigo.Candidate, ok bool) {
	head, tail := line[:pos], line[pos:]
	if c, isCmd := strings.CutPrefix(head, ":"); isCmd && !strings.ContainsAny(c, " \t") {
		var names []string
		for _, m := range metaCommands {
			if strings.HasPrefix(m, head) {
				names = append(names, m)
			}
		}
		if len(names) == 0 {
			return "", 0, nil, false
		}
		ext := commonPrefix(names)
		return ext + tail, len(ext), commandCandidates(names), true
	}
	complete := r.CompleteToken
	if strings.HasPrefix(head, ":") {
		complete = r.CompleteCommandArg // `:load ./fi` and the like
	}
	start, cc := complete(head)
	if len(cc) == 0 {
		return "", 0, nil, false
	}
	ext := commonPrefix(candidateNames(cc))
	return head[:start] + ext + tail, start + len(ext), cc, true
}

// commandCandidates renders the meta-command matches as Candidates so
// they list like any other completion.
func commandCandidates(names []string) []minigo.Candidate {
	out := make([]minigo.Candidate, len(names))
	for i, n := range names {
		out[i] = minigo.Candidate{Name: n, Kind: "command"}
	}
	return out
}

func candidateNames(cands []minigo.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Name
	}
	return out
}

// commonPrefix shrinks names[0] until every name shares it. The cut
// steps whole runes — a byte-wise walk can end mid-rune (`日`+`曜` vs
// `日`+`本` shares only 日, and the naive byte cut leaves a dangling
// first byte of 本).
func commonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	p := names[0]
	for _, n := range names[1:] {
		for len(p) > 0 && !strings.HasPrefix(n, p) {
			_, size := utf8.DecodeLastRuneInString(p)
			p = p[:len(p)-size]
		}
	}
	return p
}

// formatCandidates renders the ambiguous-press listing: names packed
// into a few columns, detail dropped (the table stays compact — `:comp`
// exists for the verbose view).
func formatCandidates(cands []minigo.Candidate) string {
	const maxRows = 20
	names := candidateNames(cands)
	shown := names
	truncated := 0
	if len(names) > maxRows {
		shown = names[:maxRows]
		truncated = len(names) - maxRows
	}
	width := 0
	for _, n := range shown {
		if len(n) > width {
			width = len(n)
		}
	}
	width += 2
	cols := 1
	if w := 80 / width; w > 1 {
		cols = w
	}
	var b strings.Builder
	for i, n := range shown {
		if i%cols == 0 && i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-*s", width, n)
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "\n… and %d more", truncated)
	}
	return b.String()
}
