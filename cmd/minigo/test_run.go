package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/podhmo/minigo/internal/testrun"
)

// runTest is `minigo test <dir>`: flag parsing around testrun.Run,
// mirroring `go test`'s spellings. Flags are accepted before or after
// the directory, like run's.
func runTest(ctx context.Context, args []string) error {
	o := testrun.Options{Out: os.Stdout, Count: 1}
	var denys, srcs, tags []string
	dir := ""
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
		// bools answer "-x", "-x=true" and "-x=false".
		isBool := func(dst *bool, names ...string) bool {
			for _, n := range names {
				switch a {
				case n, n + "=true":
					*dst = true
					return true
				case n + "=false":
					*dst = false
					return true
				}
			}
			return false
		}
		switch {
		case isBool(&o.Verbose, "-v", "--v", "-verbose", "--verbose", "-test.v", "-test.verbose"):
		case isBool(&o.Short, "-short", "--short", "-test.short"):
		case isBool(&o.FailFast, "-failfast", "--failfast", "-test.failfast"):
		case isBool(&o.Isolate, "-isolate", "--isolate", "-test.isolate"):
		case is("-run", "--run", "-test.run"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			o.Run = args[i]
		case is("-count", "--count", "-test.count"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return usageErrorf("invalid -count %q: %v", args[i], err)
			}
			o.Count = n
		case is("-timeout", "--timeout", "-test.timeout"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return usageErrorf("invalid -timeout %q: %v", args[i], err)
			}
			o.Timeout = d
		case is("--tagged", "-tagged"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			o.Tagged = args[i]
		case is("--deny", "-deny"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			denys = append(denys, strings.Split(args[i], ",")...)
		case is("--src", "-src"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			srcs = append(srcs, strings.Split(args[i], ",")...)
		case is("--tags", "-tags"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			tags = append(tags, splitTags(args[i])...)
		case is("--goroot", "-goroot"):
			i++
			if err := needValue(a, i, len(args)); err != nil {
				return err
			}
			o.GOROOT = args[i]
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
			} else if s, ok := val("--goroot", "-goroot"); ok {
				o.GOROOT = s
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
	if modes := testPackageModes(denys, srcs); len(modes) > 0 {
		o.Modes = modes
	}
	o.Tags = tags
	return testrun.Run(ctx, o)
}
