package mtesting

import (
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// FlagSymbols returns the bind map the runner installs as "flag" so
// test files importing flag compile: the scalar constructors and *Var
// forms, Var/Parse/Args/Lookup/Set and friends.
//
// v1 limits, by design: flag.Parse is a no-op (minigo test's own -run
// etc. never reach the script's flag set, and -- script args are not
// wired); Args/Arg/NArg/NFlag reflect the never-parsed state — empty;
// PrintDefaults emits through the suite's report sink.
func FlagSymbols(s *Suite) map[string]runtime.Value {
	fs := &flagState{defs: map[string]*flagDef{}, suite: s}
	return map[string]runtime.Value{
		"Bool":          flagFn("flag.Bool", fs.flagBool),
		"BoolVar":       flagFn("flag.BoolVar", fs.boolVar),
		"Duration":      flagFn("flag.Duration", fs.flagDuration),
		"DurationVar":   flagFn("flag.DurationVar", fs.durationVar),
		"Float64":       flagFn("flag.Float64", fs.flagFloat64),
		"Float64Var":    flagFn("flag.Float64Var", fs.float64Var),
		"Int":           flagFn("flag.Int", fs.flagInt),
		"IntVar":        flagFn("flag.IntVar", fs.intVar),
		"Int64":         flagFn("flag.Int64", fs.flagInt),
		"Int64Var":      flagFn("flag.Int64Var", fs.intVar),
		"String":        flagFn("flag.String", fs.flagString),
		"StringVar":     flagFn("flag.StringVar", fs.stringVar),
		"Uint":          flagFn("flag.Uint", fs.flagInt),
		"UintVar":       flagFn("flag.UintVar", fs.intVar),
		"Uint64":        flagFn("flag.Uint64", fs.flagInt),
		"Uint64Var":     flagFn("flag.Uint64Var", fs.intVar),
		"Var":           flagFn("flag.Var", fs.varFlag),
		"Parse":         flagFn("flag.Parse", fs.parse),
		"Parsed":        flagFn("flag.Parsed", fs.parsedFlag),
		"Args":          flagFn("flag.Args", fs.args),
		"Arg":           flagFn("flag.Arg", fs.arg),
		"NArg":          flagFn("flag.NArg", fs.narg),
		"NFlag":         flagFn("flag.NFlag", fs.nflag),
		"Lookup":        flagFn("flag.Lookup", fs.lookup),
		"Set":           flagFn("flag.Set", fs.set),
		"PrintDefaults": flagFn("flag.PrintDefaults", fs.printDefaults),
	}
}

// flagFn adapts a raw-args flag builtin.
func flagFn(name string, f func(runtime.VMCaller, []runtime.Value) (runtime.Value, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: f}
}

// flagDef is one registered flag; value is the *runtime.Cell flag.X
// handed out, or the script's flag.Value for Var.
type flagDef struct {
	name  string
	usage string
	value runtime.Value // *runtime.Cell or the flag.Value impl
	vx    flagValue     // Var's script impl + caller
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

// scalar constructors: flag.String("n", "d", "u") -> *Cell{Elem: d}

func (fs *flagState) flagString(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	def := ""
	if len(args) > 1 {
		def = stdlib.Str(args[1])
	}
	c := &runtime.Cell{Elem: def}
	fs.register(flagName(args), flagUsage(args, 2), c)
	return c, nil
}

func (fs *flagState) flagBool(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	def := false
	if len(args) > 1 {
		if b, ok := hostbridge.GoNative(args[1]).(bool); ok {
			def = b
		}
	}
	c := &runtime.Cell{Elem: def}
	fs.register(flagName(args), flagUsage(args, 2), c)
	return c, nil
}

func intArg(v runtime.Value) int64 {
	switch x := hostbridge.GoNative(v).(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func floatArg(v runtime.Value) float64 {
	switch x := hostbridge.GoNative(v).(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

func (fs *flagState) flagInt(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def int64
	if len(args) > 1 {
		def = intArg(args[1])
	}
	c := &runtime.Cell{Elem: def}
	fs.register(flagName(args), flagUsage(args, 2), c)
	return c, nil
}

func (fs *flagState) flagFloat64(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def float64
	if len(args) > 1 {
		def = floatArg(args[1])
	}
	c := &runtime.Cell{Elem: def}
	fs.register(flagName(args), flagUsage(args, 2), c)
	return c, nil
}

func (fs *flagState) flagDuration(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def runtime.Value = int64(0)
	if len(args) > 1 {
		def = hostbridge.GoNative(args[1])
	}
	c := &runtime.Cell{Elem: def}
	fs.register(flagName(args), flagUsage(args, 2), c)
	return c, nil
}

// *Var forms: args[0] is the &target — a *runtime.Cell; SetRef writes
// the default through it.

func (fs *flagState) setDefault(args []runtime.Value, def runtime.Value) (runtime.Value, error) {
	if len(args) < 1 || !runtime.SetRef(args[0], def) {
		return nil, fmt.Errorf("flag: first argument must be a pointer")
	}
	fs.register(flagName(args[1:]), flagUsage(args[1:], 2), args[0])
	return runtime.NIL, nil
}

func (fs *flagState) stringVar(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	def := ""
	if len(args) > 2 {
		def = stdlib.Str(args[2])
	}
	return fs.setDefault(args, def)
}

func (fs *flagState) boolVar(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	def := false
	if len(args) > 2 {
		if b, ok := hostbridge.GoNative(args[2]).(bool); ok {
			def = b
		}
	}
	return fs.setDefault(args, def)
}

func (fs *flagState) intVar(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def int64
	if len(args) > 2 {
		def = intArg(args[2])
	}
	return fs.setDefault(args, def)
}

func (fs *flagState) float64Var(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def float64
	if len(args) > 2 {
		def = floatArg(args[2])
	}
	return fs.setDefault(args, def)
}

func (fs *flagState) durationVar(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	var def runtime.Value = int64(0)
	if len(args) > 2 {
		def = hostbridge.GoNative(args[2])
	}
	return fs.setDefault(args, def)
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
	return stdlib.StrsSlice(nil), nil
}

func (fs *flagState) arg(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	// Parse never fills the arg list, so every index is out of range;
	// real flag.Arg panics — an empty string is friendlier here.
	return "", nil
}

func (fs *flagState) narg(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return int64(0), nil
}

func (fs *flagState) nflag(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	return int64(0), nil
}

// Lookup returns the flag's storage cell (an approximation — the real
// Lookup returns a *Flag descriptor).
func (fs *flagState) lookup(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) == 0 {
		return runtime.NIL, nil
	}
	d, ok := fs.defs[stdlib.Str(args[0])]
	if !ok {
		return runtime.NIL, nil
	}
	return d.value, nil
}

// Set assigns a flag by name: Var entries call the script impl's Set;
// scalar cells store the value directly.
func (fs *flagState) set(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("flag.Set: got %d argument(s), want 2", len(args))
	}
	d, ok := fs.defs[stdlib.Str(args[0])]
	if !ok {
		return nil, fmt.Errorf("flag.Set: no such flag %q", stdlib.Str(args[0]))
	}
	if d.vx.v != nil {
		m, ok := d.vx.vc.Member(d.vx.v, "Set")
		if !ok {
			return nil, fmt.Errorf("flag.Set: flag %q value has no Set method", d.name)
		}
		if _, err := d.vx.vc.Call(m, []runtime.Value{stdlib.Str(args[1])}); err != nil {
			return nil, err
		}
		return runtime.NIL, nil
	}
	if !runtime.SetRef(d.value, stdlib.Str(args[1])) {
		return nil, fmt.Errorf("flag.Set: cannot store into flag %q", d.name)
	}
	return runtime.NIL, nil
}

func (fs *flagState) printDefaults(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	if fs.suite == nil || fs.suite.Emit == nil {
		return runtime.NIL, nil
	}
	for _, d := range fs.order {
		fs.suite.Emit(fmt.Sprintf("  -%s\n    \t%s", d.name, d.usage))
	}
	return runtime.NIL, nil
}
