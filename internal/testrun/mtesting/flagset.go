package mtesting

import (
	"fmt"
	"strings"
	"time"

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

type flagMember struct {
	name string
	fn   func(runtime.VMCaller, []runtime.Value) (runtime.Value, error)
}

// scalarFlagMembers is the registration surface shared by the
// package-level flag set and every NewFlagSet instance.
func scalarFlagMembers(fs *flagState) []flagMember {
	str := func(v runtime.Value) runtime.Value { return stdlib.Str(v) }
	boolArg := func(v runtime.Value) runtime.Value {
		b, _ := hostbridge.GoNative(v).(bool)
		return b
	}
	int64Arg := func(v runtime.Value) runtime.Value { return intArg(v) }
	float64Arg := func(v runtime.Value) runtime.Value { return floatArg(v) }
	dur := func(v runtime.Value) runtime.Value { return time.Duration(intArg(v)) }
	return []flagMember{
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
	}
}

// newFlagSetValue builds the script-visible FlagSet facade. It is a
// struct whose function-valued fields are the methods; using script
// builtins instead of reflect methods keeps the VMCaller available for
// script flag.Value implementations and makes the scalar constructors
// return script cells, so `*p` reads like a Go pointer result.
func newFlagSetValue(_ string, mode int64, suite *Suite) runtime.Value {
	fs := &flagState{defs: map[string]*flagDef{}, actual: map[string]bool{}, suite: suite}
	members := append(scalarFlagMembers(fs),
		flagMember{"Var", fs.varFlag},
		flagMember{"Parse", func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("flag.(*FlagSet).Parse: got %d argument(s), want 1", len(args))
			}
			err := fs.parseArgs(vc, stringsArgs(args[0]))
			if err == nil {
				return runtime.NIL, nil
			}
			if mode == 2 { // PanicOnError
				panic(&runtime.Panic{Value: stdlib.ErrVal(err)})
			}
			// ExitOnError cannot exit the host test process; report the
			// parse error like ContinueOnError instead.
			return stdlib.ErrVal(err), nil
		}},
		flagMember{"Parsed", fs.parsedFlag},
		flagMember{"Args", fs.args},
		flagMember{"Arg", fs.arg},
		flagMember{"NArg", fs.narg},
		flagMember{"NFlag", fs.nflag},
		flagMember{"Lookup", fs.lookup},
		flagMember{"Set", fs.set},
		flagMember{"PrintDefaults", fs.printDefaults},
	)
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
	fail := func(err error) error {
		fs.rest = append([]string(nil), rest...)
		return err
	}
	for len(rest) > 0 {
		s := rest[0]
		if s == "--" {
			rest = rest[1:]
			break
		}
		if len(s) < 2 || s[0] != '-' || s == "-" {
			break
		}
		rest = rest[1:]
		numMinuses := 1
		if s[1] == '-' {
			numMinuses++
		}
		nameval := s[numMinuses:]
		if nameval == "" || nameval[0] == '-' || nameval[0] == '=' {
			return fail(fmt.Errorf("bad flag syntax: %s", s))
		}
		name, value, hasValue := nameval, "", false
		if i := strings.IndexByte(nameval, '='); i >= 0 {
			name, value, hasValue = nameval[:i], nameval[i+1:], true
		}
		d, ok := fs.defs[name]
		if !ok {
			return fail(fmt.Errorf("flag provided but not defined: -%s", name))
		}
		if !hasValue {
			if d.boolFlag {
				value = "true"
			} else {
				if len(rest) < 1 {
					return fail(fmt.Errorf("flag needs an argument: -%s", name))
				}
				value = rest[0]
				rest = rest[1:]
			}
		}
		if err := fs.assign(vc, d, value); err != nil {
			return fail(fmt.Errorf("invalid value %q for flag -%s: %w", value, name, err))
		}
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
