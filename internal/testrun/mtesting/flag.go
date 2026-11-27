package mtesting

import (
	"fmt"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// FlagSymbols returns the bind map the runner installs as "flag" so
// test files importing flag compile: the scalar constructors and *Var
// forms, Var/Parse/Args/Lookup/Set and friends.
//
// The package-level Parse remains a no-op: minigo test's own -run and
// friends never reach the script's flag set. NewFlagSet instances parse
// the argument slice handed to them, and PrintDefaults emits through the
// suite's report sink.
func FlagSymbols(s *Suite) map[string]runtime.Value {
	fs := &flagState{defs: map[string]*flagDef{}, actual: map[string]bool{}, suite: s}
	symbols := map[string]runtime.Value{
		"NewFlagSet": flagFn("flag.NewFlagSet", func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return newFlagSetValue(flagName(args), flagSetMode(args), s), nil
		}),
		"ContinueOnError": int64(0),
		"ExitOnError":     int64(1),
		"PanicOnError":    int64(2),
		"Var":             flagFn("flag.Var", fs.varFlag),
		"Parse":           flagFn("flag.Parse", fs.parse),
		"Parsed":          flagFn("flag.Parsed", fs.parsedFlag),
		"Args":            flagFn("flag.Args", fs.args),
		"Arg":             flagFn("flag.Arg", fs.arg),
		"NArg":            flagFn("flag.NArg", fs.narg),
		"NFlag":           flagFn("flag.NFlag", fs.nflag),
		"Lookup":          flagFn("flag.Lookup", fs.lookup),
		"Set":             flagFn("flag.Set", fs.set),
		"PrintDefaults":   flagFn("flag.PrintDefaults", fs.printDefaults),
	}
	for _, m := range scalarFlagMembers(fs) {
		symbols[m.name] = flagFn("flag."+m.name, m.fn)
	}
	return symbols
}

// flagFn adapts a raw-args flag builtin.
func flagFn(name string, f func(runtime.VMCaller, []runtime.Value) (runtime.Value, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: f}
}

// flagDef is one registered flag; value is the *runtime.Cell flag.X
// handed out, or the script's flag.Value for Var.
type flagDef struct {
	name     string
	usage    string
	value    runtime.Value // *runtime.Cell or the flag.Value impl
	vx       flagValue     // Var's script impl + caller
	parse    func(string) (runtime.Value, error)
	boolFlag bool
}

// flagValue is a script flag.Value captured with its VMCaller so Set
// can call back into the script's Set method.
type flagValue struct {
	vc runtime.VMCaller
	v  runtime.Value
}

type flagState struct {
	defs   map[string]*flagDef
	order  []*flagDef
	parsed bool
	rest   []string
	actual map[string]bool
	suite  *Suite
}

func (fs *flagState) register(name, usage string, v runtime.Value) {
	d := &flagDef{name: name, usage: usage, value: v}
	fs.defs[name] = d
	fs.order = append(fs.order, d)
}

func flagUsage(args []runtime.Value, i int) string {
	if i < len(args) {
		return stdlib.Str(args[i])
	}
	return ""
}

func flagName(args []runtime.Value) string {
	if len(args) == 0 {
		return ""
	}
	return stdlib.Str(args[0])
}

// scalarArg converts a flag default argument into its stored value.
type scalarArg func(runtime.Value) runtime.Value

// flagScalar builds a flag.* scalar constructor —
// flag.String("n", "d", "u") -> *Cell{Elem: d}.
func (fs *flagState) flagScalar(as scalarArg, zero runtime.Value, parse func(string) (runtime.Value, error)) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
	return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		def := zero
		if len(args) > 1 {
			def = as(args[1])
		}
		c := &runtime.Cell{Elem: def}
		name := flagName(args)
		fs.register(name, flagUsage(args, 2), c)
		fs.defs[name].parse = parse
		_, fs.defs[name].boolFlag = zero.(bool)
		return c, nil
	}
}

// scalarVar builds a flag.*Var constructor — args[0] is the &target
// (a *runtime.Cell); SetRef writes the default (args[2]) through it.
func (fs *flagState) scalarVar(as scalarArg, zero runtime.Value, parse func(string) (runtime.Value, error)) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
	return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		def := zero
		if len(args) > 2 {
			def = as(args[2])
		}
		v, err := fs.setDefault(args, def)
		if err == nil {
			if d := fs.defs[flagName(args[1:])]; d != nil {
				d.parse = parse
				_, d.boolFlag = zero.(bool)
			}
		}
		return v, err
	}
}

func (fs *flagState) setDefault(args []runtime.Value, def runtime.Value) (runtime.Value, error) {
	if len(args) < 1 || !runtime.SetRef(args[0], def) {
		return nil, fmt.Errorf("flag: first argument must be a pointer")
	}
	fs.register(flagName(args[1:]), flagUsage(args[1:], 2), args[0])
	return runtime.NIL, nil
}

// Var registers a script flag.Value: name -> the impl, kept with its
// VMCaller so flag.Set can call back into it.
func (fs *flagState) varFlag(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("flag.Var: got %d argument(s), want 3", len(args))
	}
	d := &flagDef{name: stdlib.Str(args[1]), usage: flagUsage(args, 2), value: args[0],
		vx: flagValue{vc: vc, v: args[0]}}
	fs.defs[d.name] = d
	fs.order = append(fs.order, d)
	return runtime.NIL, nil
}

// Parse is a no-op: minigo test's flags never enter the script's flag
// set and -- script args are not wired in v1.
func (fs *flagState) parse(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	fs.parsed = true
	return runtime.NIL, nil
}

// parsedFlag reports whether Parse was called.
func (fs *flagState) parsedFlag(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return fs.parsed, nil
}

func (fs *flagState) args(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return stdlib.StrsSlice(append([]string(nil), fs.rest...)), nil
}

func (fs *flagState) arg(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) == 0 {
		return "", nil
	}
	i := int(intArg(args[0]))
	if i < 0 || i >= len(fs.rest) {
		return "", nil
	}
	return fs.rest[i], nil
}

func (fs *flagState) narg(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return int64(len(fs.rest)), nil
}

func (fs *flagState) nflag(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return int64(len(fs.actual)), nil
}

// Set assigns a flag by name: Var entries call the script impl's Set;
// scalar cells store the value directly.
func (fs *flagState) set(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("flag.Set: got %d argument(s), want 2", len(args))
	}
	d, ok := fs.defs[stdlib.Str(args[0])]
	if !ok {
		return stdlib.ErrVal(fmt.Errorf("flag.Set: no such flag %q", stdlib.Str(args[0]))), nil
	}
	if err := fs.assign(vc, d, stdlib.Str(args[1])); err != nil {
		return stdlib.ErrVal(err), nil
	}
	return runtime.NIL, nil
}
