package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun"
)

func init() {
	helps["test"] = `usage: minigo test [dir] [flags]

Run the *_test.go files of the package directory dir (default: .),
the way 'go test -v' reports them: === RUN / --- PASS|FAIL|SKIP lines,
'ok  <pkg>' or 'FAIL <pkg>' trailers, exit 1 on any non-PASS test.

The directory builds as its test variant: the non-test files plus the
internal (same-package) *_test.go files form one package, and each
'func TestXxx(t *testing.T)' runs as its own call. A bound host
"testing" provides *testing.T, testing.Short and testing.Verbose; a
bound "flag" provides package-level flag constructors.

flags (accepted before or after dir):
  -v                stream === RUN / --- lines and test output
  -run pat          run only tests whose name matches the unanchored
                    regexp; "/" separates levels (like go test -run)
  -count n          run the suite n times (default 1)
  -timeout d        fail the suite after a duration like 30s or 10m
  -short            bound to testing.Short()
  -failfast         stop scheduling tests after the first failure
  --tagged tag      also select files needing this build tag, and run
                    only tests declared in those files
  --tags tag,...    build tags for file selection, like go build -tags
  --deny pkg,...    make importing these packages fail with a clear error
  --src pkg,...     interpret these packages from source even where a
                    host-bound version exists
  -h, --help        show this help

Not supported in this version (reported loudly): external
'package foo_test' files, BenchmarkXxx, FuzzXxx, examples with an
// Output: comment, TestMain, t.Parallel scheduling, t.Setenv, t.Chdir.
`
}

// runTest is `minigo test <dir>`: flag parsing around testrun.Run,
// mirroring `go test`'s spellings. Flags are accepted before or after
// the directory, like run's.
func runTest(ctx context.Context, args []string) error {
	o := testrun.Options{Out: os.Stdout, Count: 1}
	var denys, srcs, tags []string
	dir := ""
	need := func(name string, i int) error {
		if i >= len(args) {
			return usageErrorf("%s requires a value", name)
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		// "-x=v" spellings; each flag also answers its -test.x form.
		val := func(names ...string) (string, bool) {
			for _, n := range names {
				if strings.HasPrefix(a, n+"=") {
					return strings.SplitN(a, "=", 2)[1], true
				}
			}
			return "", false
		}
		is := func(names ...string) bool { // "-x" spellings
			for _, n := range names {
				if a == n {
					return true
				}
			}
			return false
		}
		switch {
		case is("-v", "--v", "-verbose", "--verbose", "-v=true", "-test.v", "-test.v=true"):
			o.Verbose = true
		case is("-short", "--short", "-test.short", "-test.short=true"):
			o.Short = true
		case is("-failfast", "--failfast", "-test.failfast", "-test.failfast=true"):
			o.FailFast = true
		case is("-run", "--run", "-test.run"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			o.Run = args[i]
		case is("-count", "--count", "-test.count"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return usageErrorf("invalid -count %q: %v", args[i], err)
			}
			o.Count = n
		case is("-timeout", "--timeout", "-test.timeout"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return usageErrorf("invalid -timeout %q: %v", args[i], err)
			}
			o.Timeout = d
		case is("--tagged", "-tagged"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			o.Tagged = args[i]
		case is("--deny", "-deny"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			denys = append(denys, strings.Split(args[i], ",")...)
		case is("--src", "-src"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			srcs = append(srcs, strings.Split(args[i], ",")...)
		case is("--tags", "-tags"):
			i++
			if err := need(a, i); err != nil {
				return err
			}
			tags = append(tags, splitTags(args[i])...)
		default:
			if s, ok := val("-run", "--run", "-test.run"); ok {
				o.Run = s
			} else if s, ok := val("-count", "--count", "-test.count"); ok {
				n, err := strconv.Atoi(s)
				if err != nil {
					return usageErrorf("invalid -count %q: %v", s, err)
				}
				o.Count = n
			} else if s, ok := val("-timeout", "--timeout", "-test.timeout"); ok {
				d, err := time.ParseDuration(s)
				if err != nil {
					return usageErrorf("invalid -timeout %q: %v", s, err)
				}
				o.Timeout = d
			} else if s, ok := val("--tagged", "-tagged"); ok {
				o.Tagged = s
			} else if s, ok := val("--deny", "-deny"); ok {
				denys = append(denys, strings.Split(s, ",")...)
			} else if s, ok := val("--src", "-src"); ok {
				srcs = append(srcs, strings.Split(s, ",")...)
			} else if s, ok := val("--tags", "-tags"); ok {
				tags = append(tags, splitTags(s)...)
			} else if isHelpFlag(a) {
				return errHelp
			} else if strings.HasPrefix(a, "-") {
				return usageErrorf("unknown flag %q", a)
			} else if dir != "" {
				return usageErrorf("test takes at most one package directory")
			} else {
				dir = a
			}
		}
	}
	if dir == "" {
		dir = "."
	}
	o.Dir = dir
	modes := map[string]minigo.PackageMode{}
	for _, p := range denys {
		modes[p] = minigo.ModeDeny
	}
	for _, p := range srcs {
		modes[p] = minigo.ModeSource
	}
	if len(modes) > 0 {
		o.Modes = modes
	}
	o.Tags = tags
	return testrun.Run(ctx, o)
}
