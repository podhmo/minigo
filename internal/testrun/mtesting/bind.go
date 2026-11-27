package mtesting

import (
	"fmt"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// B, F, M are placeholder host types: `*testing.B` / `*testing.F` /
// `*testing.M` parameters resolve so the signature scan can report
// benchmarks, fuzz targets and TestMain as "not supported" instead of
// failing the build. Nothing ever constructs them.
type B struct{}

// F is the fuzz placeholder (see B).
type F struct{}

// M is the TestMain placeholder (see B).
type M struct{}

// testingTB is the bound `testing.TB`: an interface typedef listing
// the methods *T exposes. Go's TB also requires an unexported method
// so callers cannot implement it; the v1 facade has no unexported
// requirement — *T is the only implementation that exists anyway.
var testingTB = &runtime.TypeDef{
	Name: "testing.TB",
	Kind: runtime.KindInterface,
	MReqs: []string{
		"ArtifactDir", "Attr", "Chdir", "Cleanup", "Context", "Deadline",
		"Error", "Errorf", "Fail", "FailNow", "Failed", "Helper",
		"Log", "Logf", "Name", "Output", "Parallel", "Run",
		"Setenv", "Skip", "Skipf", "SkipNow", "Skipped", "TempDir",
	},
}

// Symbols returns the bind map the runner installs as "testing" on its
// dedicated engine: the *testing.T typedef for test signatures,
// placeholder B/F/M/TB typedefs for detection, testing.Short/Verbose,
// and the __cleanups entry the runner's second Engine.Call uses to run
// the top-level test's cleanups.
func Symbols(s *Suite) map[string]runtime.Value {
	return map[string]runtime.Value{
		"T":  stdlib.HostType("testing.T", func() any { return &T{} }),
		"B":  stdlib.HostType("testing.B", func() any { return &B{} }),
		"F":  stdlib.HostType("testing.F", func() any { return &F{} }),
		"M":  stdlib.HostType("testing.M", func() any { return &M{} }),
		"TB": testingTB,
		"Short": &runtime.BuiltinFunc{Name: "testing.Short", Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return s.Short, nil
		}},
		"Verbose": &runtime.BuiltinFunc{Name: "testing.Verbose", Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return s.Verbose, nil
		}},
		// __cleanup is the second-process executor for one top-level
		// t.Cleanup func: the test function's Engine.Call is over and
		// its VM is gone. The runner calls it once per cleanup so a
		// panicking cleanup cannot kill the process later cleanups need.
		"__cleanup": &runtime.BuiltinFunc{Name: "testing.__cleanup", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, errArgs("__cleanup", len(args), 1)
			}
			t, err := tOf(args[0])
			if err != nil {
				return nil, err
			}
			return nil, RunOneCleanup(vc, t)
		}},
	}
}

// tOf unwraps a *T argument (a GoValue box, or a Named/Cell around it).
func tOf(v runtime.Value) (*T, error) {
	if gv, ok := runtime.Unwrap(v).(*runtime.GoValue); ok {
		if t, ok := gv.V.(*T); ok {
			return t, nil
		}
	}
	return nil, errArgs("__cleanup", 1, 1)
}

func errArgs(name string, got, want int) error {
	return fmt.Errorf("%s: got %d argument(s), want %d", name, got, want)
}
