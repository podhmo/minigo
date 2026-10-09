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
	// crypto/md5's compression (md5block_decl.go, //go:noescape): the
	// interpreted Write path feeds blocks through it — an unimplemented
	// stub would leave dig.s at the IV, so Sum prints the IV bytes.
	"crypto/md5.block": md5Block,
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

// md5Block is crypto/md5's block(dig, p): MD5 compression of each
// 64-byte chunk of p into dig.s, matching md5block_generic.go.
func md5Block(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	st := runtime.StructOf(args[0])
	if st == nil || len(st.Fields) == 0 {
		return runtime.NIL, nil
	}
	s := wordElems(st.Fields[0])
	p := wordElems(args[1])
	if len(s) < 4 {
		return runtime.NIL, nil
	}
	a, b, c, d := uint32(wordOf(s[0])), uint32(wordOf(s[1])), uint32(wordOf(s[2])), uint32(wordOf(s[3]))
	var x [16]uint32
	for i := 0; i+64 <= len(p); i += 64 {
		for j := 0; j < 16; j++ {
			x[j] = uint32(wordOf(p[i+4*j])) |
				uint32(wordOf(p[i+4*j+1]))<<8 |
				uint32(wordOf(p[i+4*j+2]))<<16 |
				uint32(wordOf(p[i+4*j+3]))<<24
		}
		aa, bb, cc, dd := a, b, c, d
		for j := 0; j < 64; j++ {
			var f, g uint32
			switch j / 16 {
			case 0:
				f = (b & c) | (^b & d)
				g = uint32(j)
			case 1:
				f = (d & b) | (^d & c)
				g = uint32(5*j+1) % 16
			case 2:
				f = b ^ c ^ d
				g = uint32(3*j+5) % 16
			default:
				f = c ^ (b | ^d)
				g = uint32(7*j) % 16
			}
			f += a + md5K[j] + x[g]
			a, d, c, b = d, c, b, b+bits.RotateLeft32(f, md5S[j])
		}
		a, b, c, d = a+aa, b+bb, c+cc, d+dd
	}
	s[0], s[1], s[2], s[3] = int64(a), int64(b), int64(c), int64(d)
	return runtime.NIL, nil
}

var md5S = [64]int{
	7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
	5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
	4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
	6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
}

var md5K = [64]uint32{
	0xd76aa478, 0xe8c7b756, 0x242070db, 0xc1bdceee,
	0xf57c0faf, 0x4787c62a, 0xa8304613, 0xfd469501,
	0x698098d8, 0x8b44f7af, 0xffff5bb1, 0x895cd7be,
	0x6b901122, 0xfd987193, 0xa679438e, 0x49b40821,
	0xf61e2562, 0xc040b340, 0x265e5a51, 0xe9b6c7aa,
	0xd62f105d, 0x02441453, 0xd8a1e681, 0xe7d3fbc8,
	0x21e1cde6, 0xc33707d6, 0xf4d50d87, 0x455a14ed,
	0xa9e3e905, 0xfcefa3f8, 0x676f02d9, 0x8d2a4c8a,
	0xfffa3942, 0x8771f681, 0x6d9d6122, 0xfde5380c,
	0xa4beea44, 0x4bdecfa9, 0xf6bb4b60, 0xbebfbc70,
	0x289b7ec6, 0xeaa127fa, 0xd4ef3085, 0x04881d05,
	0xd9d4d039, 0xe6db99e5, 0x1fa27cf8, 0xc4ac5665,
	0xf4292244, 0x432aff97, 0xab9423a7, 0xfc93a039,
	0x655b59c3, 0x8f0ccc92, 0xffeff47d, 0x85845dd1,
	0x6fa87e4f, 0xfe2ce6e0, 0xa3014314, 0x4e0811a1,
	0xf7537e82, 0xbd3af235, 0x2ad7d2bb, 0xeb86d391,
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
