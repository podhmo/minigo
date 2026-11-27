package main

// helps maps a help topic or command name to its text; "" is the overview.
var helps = map[string]string{
	"": `minigo runs Go source lazily, without a build step.

usage:
  minigo run <ref> [Func] [flags] [-- script args...]
  minigo run file.go... [Func] [flags] [-- script args...]
  minigo <ref> [Func] [-- script args...]    shorthand for run
  minigo repl
  minigo test [dir] [flags]
  minigo vet <ref> [--special import/path.Sym]...
  minigo gen-intrinsics -output <dir> <ref>...
  minigo help [topic]

<ref> is a package directory (., ./dir, /abs/dir) or an import path
resolved from the current directory's module.

Run 'minigo help <command>' or 'minigo <command> -h' for details.
Use 'minigo help examples' for small end-to-end examples and
'minigo help env' for environment variables.
`,
	"run": `usage: minigo run <ref> [Func] [flags] [-- script args...]
       minigo run file.go... [Func] [flags] [-- script args...]
       minigo <ref> [Func] [-- script args...]

Run Func (default: main) in the package <ref> and print its result
unless it returns nothing. A list of .go files runs like
'go run a.go b.go': the named files form one package. Arguments after
-- become the script's os.Args[1:]; os.Args[0] is <ref>.

flags (accepted before or after <ref>):
  --entry Func     function to run (same as the positional Func)
  --deny pkg,...   make importing these packages fail with a clear error
  --src pkg,...    interpret these packages from source even where a
                   host-bound version exists
  --tags tag,...   build tags for file selection, like go build -tags
  --cpuprofile f   write a CPU profile of the interpreter to f
                   (default: $MINIGO_CPUPROFILE)
  --memprofile f   write an allocation profile of the interpreter to f
                   at exit (default: $MINIGO_MEMPROFILE)
  --scriptprofile f
                   write a profile of the script's own functions and
                   lines to f (default: $MINIGO_SCRIPTPROFILE)
  -h, --help       show this help

--cpuprofile and --memprofile profile minigo itself; --scriptprofile
samples the interpreted program's call stacks (wall time, excluding
time parked on channels and select). All three are pprof profiles: read
them with go tool pprof. The env defaults let a wrapper script that runs
minigo be profiled without editing its command line.
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
	"examples": `examples:

Run one file as a script:
  minigo run script.go

Run a package entry function with script arguments:
  minigo run ./cmd/tool --entry Main -- -verbose input.txt

Force a package to interpret from source when its host binding is
incomplete or when editing a copy of that package:
  minigo run ./app --src encoding/json,text/template

Profile a run (see minigo help env for the environment-variable forms):
  minigo run ./app --cpuprofile cpu.pprof --scriptprofile script.pprof
  go tool pprof cpu.pprof
`,
	"env": `environment variables:

  MINIGO_CPUPROFILE     default for --cpuprofile: write a pprof CPU
                        profile of the minigo interpreter itself
  MINIGO_MEMPROFILE     default for --memprofile: write a pprof
                        allocation profile of the interpreter at exit
  MINIGO_SCRIPTPROFILE  default for --scriptprofile: write a pprof
                        profile of the script's own functions and lines

Command-line flags override these defaults. The environment variables
are useful when minigo is launched by a wrapper or executable script and
editing its command line is inconvenient.
`,
}
