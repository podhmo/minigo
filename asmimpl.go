package minigo

import (
	"math/bits"

	"github.com/podhmo/minigo/runtime"
)

// asmImpls wires host implementations to bodiless stdlib declarations —
// the //go:noescape assembly stubs whose "body" is a .s file the
// interpreter cannot run. Without them the decl compiles to the
// zero-return shim and, e.g., math/big's nat.add silently yields an
// empty nat (new(big.Int).Add printed 0). The substitution happens at
// materialize time (materializeOne) so every call form — direct,
// deferred, func value — sees the host impl like a normal builtin.
//
// Each entry implements the documented Go contract of the assembly
// primitive: slices arrive as script *runtime.Slice (elements int64,
// Word-sized), writes go through to the shared backing array exactly
// like the asm does to the caller's array.
// The bodiless decls are math/big's vector primitives: addVV, subVV,
// lshVU, rshVU, mulAddVWW, addMulVVWW (arith_decl.go, //go:noescape).
// addVW/subVW/shlVU/addMulVVW/divWVW keep Go bodies — the substitute
// only fires when the decl has no body, so listing extra names is
// harmless but the table mirrors the real decl set.
var asmImpls = map[string]func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error){
	"math/big.addVV":      bigAddVV,
	"math/big.subVV":      bigSubVV,
	"math/big.lshVU":      bigLshVU,
	"math/big.rshVU":      bigRshVU,
	"math/big.mulAddVWW":  bigMulAddVWW,
	"math/big.addMulVVWW": bigAddMulVVWW,
	// maps.Clone's body asserts the linknamed runtime clone's result.
	"maps.clone": mapsClone,
}

// mapsClone is the runtime map clone behind maps.Clone: a shallow copy
// keeping the declared map type (and a named map type's tag).
func mapsClone(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return runtime.NIL, nil
	}
	var clone func(v runtime.Value) runtime.Value
	clone = func(v runtime.Value) runtime.Value {
		switch m := v.(type) {
		case *runtime.Named:
			return &runtime.Named{V: clone(m.V), Typ: m.Typ}
		case *runtime.Map:
			pairs := make(map[runtime.Value]runtime.Value, len(m.Pairs))
			for k, e := range m.Pairs {
				pairs[k] = e
			}
			return &runtime.Map{Pairs: pairs, Order: append([]runtime.Value(nil), m.Order...),
				Keys: append([]runtime.Value(nil), m.Keys...), Typ: m.Typ}
		}
		if d, ok := runtime.Deref(v); ok {
			return clone(d)
		}
		return v
	}
	return clone(args[0]), nil
}

// asmImpl resolves a bodiless func declaration to a registered host
// implementation, keyed by package path + name. Called at materialize
// time so the name never reaches a caller as a zero-return Function.
func asmImpl(pkg *runtime.Package, name string) (runtime.Value, bool) {
	impl, ok := asmImpls[pkg.Path+"."+name]
	if !ok {
		return nil, false
	}
	return &runtime.BuiltinFunc{Name: pkg.Path + "." + name, Fn: impl}, true
}

// wordElems unwraps a script nat ([]Word) argument to its backing
// elements — Named peels, nil keeps a nil slice.
func wordElems(v runtime.Value) []runtime.Value {
	v = runtime.Unwrap(v)
	switch s := v.(type) {
	case *runtime.Slice:
		return s.Elems
	case *runtime.TypedNil:
		return nil
	}
	return nil
}

func wordOf(v runtime.Value) uint64 {
	switch n := runtime.Unwrap(v).(type) {
	case int64:
		return uint64(n)
	case float64:
		return uint64(n)
	}
	return 0
}

func bigAddVV(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x, y := wordElems(args[0]), wordElems(args[1]), wordElems(args[2])
	var c uint64
	for i := range x {
		var zi uint64
		zi, c = bits.Add64(wordOf(x[i]), wordOf(y[i]), c)
		z[i] = int64(zi)
	}
	return int64(c), nil
}

func bigSubVV(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x, y := wordElems(args[0]), wordElems(args[1]), wordElems(args[2])
	var c uint64
	for i := range x {
		var zi uint64
		zi, c = bits.Sub64(wordOf(x[i]), wordOf(y[i]), c)
		z[i] = int64(zi)
	}
	return int64(c), nil
}

func bigLshVU(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x := wordElems(args[0]), wordElems(args[1])
	s := uint(wordOf(args[2]))
	var c uint64
	for i := range x {
		xi := wordOf(x[i])
		z[i] = int64(xi<<s | c)
		c = xi >> (64 - s)
	}
	return int64(c), nil
}

func bigRshVU(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x := wordElems(args[0]), wordElems(args[1])
	s := uint(wordOf(args[2]))
	var c uint64
	for i := len(x) - 1; i >= 0; i-- {
		xi := wordOf(x[i])
		z[i] = int64(xi>>s | c)
		c = xi << (64 - s)
	}
	return int64(c), nil
}

func bigMulAddVWW(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x := wordElems(args[0]), wordElems(args[1])
	y, c := wordOf(args[2]), wordOf(args[3])
	for i := range x {
		hi, lo := bits.Mul64(wordOf(x[i]), y)
		var zi, cc uint64
		zi, cc = bits.Add64(lo, c, 0)
		z[i] = int64(zi)
		c = hi + cc
	}
	return int64(c), nil
}

// addMulVVWW sets z = x + y*m + a (z may alias x or y), per arith_decl.go.
func bigAddMulVVWW(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	z, x, y := wordElems(args[0]), wordElems(args[1]), wordElems(args[2])
	m, c := wordOf(args[3]), wordOf(args[4])
	for i := range x {
		hi, lo := bits.Mul64(wordOf(y[i]), m)
		lo, cc := bits.Add64(lo, c, 0)
		var zi, c2 uint64
		zi, c2 = bits.Add64(wordOf(x[i]), lo, 0)
		z[i] = int64(zi)
		c = hi + cc + c2
	}
	return int64(c), nil
}
