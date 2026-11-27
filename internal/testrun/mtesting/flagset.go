package mtesting

import (
	"fmt"
	"strings"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// flagSetMode reads NewFlagSet's ErrorHandling argument. The bound
// constants are plain ints; unknown values behave like ContinueOnError
// because exiting the host process is never acceptable in a test runner.
func flagSetMode(args []runtime.Value) int64 {
	if len(args) < 2 {
		return 0
	}
	if n, ok := runtime.Unwrap(args[1]).(int64); ok {
		return n
	}
	return 0
}

// newFlagSetValue builds the script-visible FlagSet facade. It is a
// struct whose function-valued fields are the methods; using script
// builtins instead of reflect methods keeps the VMCaller available for
// script flag.Value implementations and makes the scalar constructors
// return script cells, so `*p` reads like a Go pointer result.
func newFlagSetValue(_ string, mode int64, suite *Suite) runtime.Value {
	fs := &flagState{defs: map[string]*flagDef{}, actual: map[string]bool{}, suite: suite}
	str := func(v runtime.Value) runtime.Value { return stdlib.Str(v) }
	boolArg := func(v runtime.Value) runtime.Value {
		b, _ := hostbridge.GoNative(v).(bool)
		return b
	}
	int64Arg := func(v runtime.Value) runtime.Value { return intArg(v) }
	float64Arg := func(v runtime.Value) runtime.Value { return floatArg(v) }
	dur := func(v runtime.Value) runtime.Value { return hostbridge.GoNative(v) }

	members := []struct {
		name string
		fn   func(runtime.VMCaller, []runtime.Value) (runtime.Value, error)
	}{
		{"Bool", fs.flagScalar(boolArg, false, parseBoolFlag)},
		{"BoolVar", fs.scalarVar(boolArg, false, parseBoolFlag)},
		{"Duration", fs.flagScalar(dur, int64(0), parseDurationFlag)},
		{"DurationVar", fs.scalarVar(dur, int64(0), parseDurationFlag)},
		{"Float64", fs.flagScalar(float64Arg, float64(0), parseFloatFlag)},
		{"Float64Var", fs.scalarVar(float64Arg, float64(0), parseFloatFlag)},
		{"Int", fs.flagScalar(int64Arg, int64(0), parseIntFlag)},
		{"IntVar", fs.scalarVar(int64Arg, int64(0), parseIntFlag)},
		{"Int64", fs.flagScalar(int64Arg, int64(0), parseIntFlag)},
		{"Int64Var", fs.scalarVar(int64Arg, int64(0), parseIntFlag)},
		{"String", fs.flagScalar(str, "", parseStringFlag)},
		{"StringVar", fs.scalarVar(str, "", parseStringFlag)},
		{"Uint", fs.flagScalar(int64Arg, int64(0), parseUintFlag)},
		{"UintVar", fs.scalarVar(int64Arg, int64(0), parseUintFlag)},
		{"Uint64", fs.flagScalar(int64Arg, int64(0), parseUintFlag)},
		{"Uint64Var", fs.scalarVar(int64Arg, int64(0), parseUintFlag)},
		{"Var", fs.varFlag},
		{"Parse", func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("flag.(*FlagSet).Parse: got %d argument(s), want 1", len(args))
			}
			err := fs.parseArgs(vc, stringsArgs(args[0]))
			if err == nil {
				return runtime.NIL, nil
			}
			if mode == 2 { // PanicOnError
				panic(&runtime.Panic{Value: err.Error()})
			}
			// ExitOnError cannot exit the host test process; report the
			// parse error like ContinueOnError instead.
			return stdlib.ErrVal(err), nil
		}},
		{"Parsed", fs.parsedFlag},
		{"Args", fs.args},
		{"Arg", fs.arg},
		{"NArg", fs.narg},
		{"NFlag", fs.nflag},
		{"Lookup", fs.lookup},
		{"Set", fs.set},
		{"PrintDefaults", fs.printDefaults},
	}
	fields := make([]string, len(members))
	vals := make([]runtime.Value, len(members))
	for i, m := range members {
		fields[i] = m.name
		vals[i] = flagFn("flag.(*FlagSet)."+m.name, m.fn)
	}
	return &runtime.Struct{Def: &runtime.TypeDef{Name: "flag.FlagSet", Kind: runtime.KindStruct, Fields: fields}, Fields: vals}
}

func stringsArgs(v runtime.Value) []string {
	s, ok := stdlib.SliceOf(v)
	if !ok {
		return nil
	}
	out := make([]string, len(s.Elems))
	for i, e := range s.Elems {
		out[i] = stdlib.Str(e)
	}
	return out
}

// parseArgs consumes leading flags from args and leaves the positional
// remainder in fs.rest. It implements the standard flag package's flag
// forms: -x, --x, -x=v, --x=v, and -x v for non-bool flags.
func (fs *flagState) parseArgs(vc runtime.VMCaller, args []string) error {
	fs.parsed = true
	rest := append([]string(nil), args...)
	for len(rest) > 0 {
		s := rest[0]
		if s == "--" {
			rest = rest[1:]
			break
		}
		if len(s) < 2 || s[0] != '-' || s == "-" {
			break
		}
		numMinuses := 1
		if s[1] == '-' {
			numMinuses++
		}
		nameval := s[numMinuses:]
		if nameval == "" || nameval[0] == '-' || nameval[0] == '=' {
			return fmt.Errorf("bad flag syntax: %s", s)
		}
		name, value, hasValue := nameval, "", false
		if i := strings.IndexByte(nameval, '='); i >= 0 {
			name, value, hasValue = nameval[:i], nameval[i+1:], true
		}
		d, ok := fs.defs[name]
		if !ok {
			return fmt.Errorf("flag provided but not defined: -%s", name)
		}
		if !hasValue {
			if d.boolFlag {
				value = "true"
			} else {
				if len(rest) < 2 {
					return fmt.Errorf("flag needs an argument: -%s", name)
				}
				value = rest[1]
				rest = rest[1:]
			}
		}
		if err := fs.assign(vc, d, value); err != nil {
			return fmt.Errorf("invalid value %q for flag -%s: %w", value, name, err)
		}
		rest = rest[1:]
	}
	fs.rest = rest
	return nil
}

// assign stores one textual flag value. Var entries call the script
// implementation's Set method through the captured caller; scalar cells
// parse and store directly.
func (fs *flagState) assign(vc runtime.VMCaller, d *flagDef, raw string) error {
	if d.vx.v != nil {
		callvc := vc
		if callvc == nil {
			callvc = d.vx.vc
		}
		m, ok := callvc.Member(d.vx.v, "Set")
		if !ok {
			return fmt.Errorf("flag %q value has no Set method", d.name)
		}
		ret, err := callvc.Call(m, []runtime.Value{raw})
		if err != nil {
			return err
		}
		if ret != nil && !runtime.IsNilIface(runtime.Unwrap(ret)) {
			if err := hostbridge.HostErrOf(callvc, ret); err != nil {
				return err
			}
			return fmt.Errorf("%v", runtime.Unwrap(ret))
		}
		fs.actual[d.name] = true
		return nil
	}
	v := runtime.Value(raw)
	if d.parse != nil {
		parsed, err := d.parse(raw)
		if err != nil {
			return err
		}
		v = parsed
	}
	if !runtime.SetRef(d.value, v) {
		return fmt.Errorf("cannot store into flag %q", d.name)
	}
	fs.actual[d.name] = true
	return nil
}
