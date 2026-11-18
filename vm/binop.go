// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"go/constant"
	"go/token"
	"math"
	"reflect"
	"time"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// rawHostScalarOperands is twinOperands for a raw host scalar — a bound
// named basic's payload that crossed an interface untagged, as
// any(2*time.Second) does: against a Named of its bound typedef it
// reads as that Named, so the interface comparison sees one type.
func rawHostScalarOperands(a, b runtime.Value) (runtime.Value, runtime.Value) {
	if n, ok := b.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostScalar != nil {
		if _, isNamed := a.(*runtime.Named); !isNamed && reflect.TypeOf(a) == reflect.TypeOf(n.Typ.HostScalar) {
			return runtime.Tag(n.Typ, a), b
		}
	}
	if n, ok := a.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostScalar != nil {
		if _, isNamed := b.(*runtime.Named); !isNamed && reflect.TypeOf(b) == reflect.TypeOf(n.Typ.HostScalar) {
			return a, runtime.Tag(n.Typ, b)
		}
	}
	return a, b
}

// Equal implements [runtime.VMCaller].
func (v *VM) Equal(td *runtime.TypeDef, a, b runtime.Value) bool {
	if td == nil {
		return v.ifaceEql(v.topFrame(), a, b)
	}
	return v.slotEql(td, a, b)
}

// slotEql compares two values held in a slot declared td — a struct
// field, an array element, a slices/maps helper's element: an
// interface-typed slot compares (dynamic type, value) pairs, so
// S{M(1)} != S{1} for `type S struct{ A any }`; a slot of a host named
// basic's twin type folds the host box (twinSlot). A nil td compares
// like eqlValue.
func (v *VM) slotEql(td *runtime.TypeDef, a, b runtime.Value) bool {
	if td != nil && td.Kind == runtime.KindAlias {
		td = v.peelAlias(td)
	}
	if td != nil && td.Kind == runtime.KindInterface && !nilSlotValue(a) && !nilSlotValue(b) {
		return v.ifaceEql(v.topFrame(), a, b)
	}
	if isGoValue(a) || isGoValue(b) {
		a, b = twinSlot(td, a), twinSlot(td, b)
	}
	return v.eqlValue(a, b)
}

// nilSlotValue reports the nil shapes eqlValue already compares by
// Go's interface rules (a nil interface of any declared interface
// type, a boxed typed nil).
func nilSlotValue(x runtime.Value) bool {
	switch x.(type) {
	case runtime.Nil, *runtime.IfaceNil, *runtime.TypedNil:
		return true
	}
	return false
}

// binOpToken maps a bytecode binary op to its go/token spelling —
// inverse of compile's binOpOf, for constant-domain evaluation.
func binOpToken(op bytecode.BinOp) (token.Token, bool) {
	switch op {
	case bytecode.BinAdd:
		return token.ADD, true
	case bytecode.BinSub:
		return token.SUB, true
	case bytecode.BinMul:
		return token.MUL, true
	case bytecode.BinQuo:
		return token.QUO, true
	case bytecode.BinRem:
		return token.REM, true
	case bytecode.BinAnd:
		return token.AND, true
	case bytecode.BinOr:
		return token.OR, true
	case bytecode.BinXor:
		return token.XOR, true
	case bytecode.BinAndNot:
		return token.AND_NOT, true
	case bytecode.BinShl:
		return token.SHL, true
	case bytecode.BinShr:
		return token.SHR, true
	case bytecode.BinEql:
		return token.EQL, true
	case bytecode.BinNeq:
		return token.NEQ, true
	case bytecode.BinLss:
		return token.LSS, true
	case bytecode.BinLeq:
		return token.LEQ, true
	case bytecode.BinGtr:
		return token.GTR, true
	case bytecode.BinGeq:
		return token.GEQ, true
	}
	return 0, false
}

// asComplex reads a value in the complex domain: a boxed complex of
// either width, or a bare numeric operand that promotes to complex128.
// width is 64 for a complex64 value, else 128.
func asComplex(x runtime.Value) (cv complex128, width int, ok bool) {
	if n, isN := x.(*runtime.Named); isN {
		x = n.V
	}
	if v, isG := x.(*runtime.GoValue); isG {
		switch c := v.V.(type) {
		case complex64:
			return complex128(c), 64, true
		case complex128:
			return c, 128, true
		}
	}
	return 0, 0, false
}

// complexOperand reads a value into the complex domain for arithmetic:
// a real complex value keeps its width; a bare int64/float64 promotes
// with width 0 — untyped, so it joins either side (`c64 + 1` works).
func complexOperand(x runtime.Value) (complex128, int, bool) {
	if cv, w, ok := asComplex(x); ok {
		return cv, w, true
	}
	switch v := x.(type) {
	case int64:
		return complex(float64(v), 0), 0, true
	case float64:
		return complex(v, 0), 0, true
	}
	return 0, 0, false
}

// complexResult boxes a complex result at the operand's width — the
// narrowest side wins like Go's complex64/complex128 promotion rules,
// and a promoted constant (width 0) takes the other side's width.
func complexResult(cv complex128, wa, wb int) runtime.Value {
	if wa == 64 || wb == 64 {
		return &runtime.GoValue{V: complex64(cv)}
	}
	return &runtime.GoValue{V: cv}
}

// isCompareOp reports whether the op is a value comparison —
// constants in those adopt the operand's type instead of folding.
func isCompareOp(op bytecode.BinOp) bool {
	switch op {
	case bytecode.BinEql, bytecode.BinNeq, bytecode.BinLss,
		bytecode.BinLeq, bytecode.BinGtr, bytecode.BinGeq:
		return true
	}
	return false
}

// ifaceEql is the strict (type, value) pair equality an interface-typed
// switch tag uses: `case 1:` materializes to int and never matches an
// any(float64(1.0)) tag — plain BinEql would coerce it equal. A named
// tag only pairs with the same named type; the nil-ish operands keep
// the interface nil rules from eqlValue.
func (v *VM) ifaceEql(f *frame, a, b runtime.Value) bool {
	a, b = v.materialize(f, a), v.materialize(f, b)
	// a nil interface equals nil: `var a any; a == nil` is true — the
	// pair rule below only applies when a dynamic type exists. A typed
	// nil still carries its type, so `any((*int)(nil)) == nil` is false.
	if isIfaceNilValue(a) || isIfaceNilValue(b) {
		return isIfaceNilValue(a) && isIfaceNilValue(b)
	}
	// an interface operand is a (dynamic type, value) pair: the dynamic
	// typedefs must be identical before values compare. Values carry
	// their type on Typ/Def rather than only on a Named wrapper —
	// any(chan<-T) vs any(chan T) share a channel but not a type, and
	// any(A) vs any(B) differ for look-alike declared arrays (the lax
	// eqlValue paths relax across typedefs for static =='s
	// assignability rules).
	if isGoValue(a) || isGoValue(b) {
		a, b = runtime.TwinOperands(a, b)
	} else {
		a, b = rawHostScalarOperands(a, b)
	}
	atd, btd := v.typeOfValue(a), v.typeOfValue(b)
	if !sameTypeDef(atd, btd) && !v.sameTypedef(atd, btd) {
		// a nil host *T is the same dynamic type as its twin's *T
		return false
	}
	// The dynamic types are identical — an uncomparable one panics on
	// the TYPE alone: `any(f) == any(nilFunc)` and `any(mapLit) ==
	// any(nilMap)` trap regardless of the values' wrapper shapes.
	// Arrays stay comparable-by-element here: [N]T keeps the slice Kind
	// on its typedef, so only a non-array shape panics this early.
	if atd != nil && uncomparableTyp(atd) && !isArrayTyp(atd) {
		panic(runtime.ComparingUncomparablePanic(spelledTyp(atd)))
	}
	an, aNamed := a.(*runtime.Named)
	bn, bNamed := b.(*runtime.Named)
	if aNamed != bNamed {
		return false
	}
	if aNamed {
		if !sameTypeDef(an.Typ, bn.Typ) {
			return false
		}
		a, b = an.V, bn.V
	}
	switch a.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return v.eqlValue(a, b)
	}
	switch b.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return v.eqlValue(a, b)
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	return v.eqlValue(a, b)
}

// isIfaceNilValue reports whether x carries no dynamic type — an
// untyped nil or a nil interface value. A typed nil keeps its type
// inside IfaceNil.Typ (coerce boxes TypedNil{*T} as IfaceNil{*T}), so
// only an interface-typed or untagged IfaceNil counts as plain nil.
func isIfaceNilValue(x runtime.Value) bool {
	switch t := x.(type) {
	case runtime.Nil:
		return true
	case *runtime.IfaceNil:
		return t.Typ == nil || t.Typ.Kind == runtime.KindInterface
	}
	return false
}

func (v *VM) binaryOp(f *frame, op bytecode.BinOp, a, b runtime.Value) runtime.Value {
	return v.binaryOpFlags(f, op, a, b, 0)
}

// binaryOpFlags is binaryOp with the instruction's B flags
// (bytecode.ShiftConstCount).
// typedFoldKey memoizes a typed constant expression's fold on its left
// payload (see binaryOpFlags): the operator, the right payload and the
// shared tag determine the result.
type typedFoldKey struct {
	op    bytecode.BinOp
	other *runtime.UConst
	tag   *runtime.TypeDef
}

func (v *VM) binaryOpFlags(f *frame, op bytecode.BinOp, a, b runtime.Value, flags int) runtime.Value {
	// an interface-typed switch tag compares pairs, not coerced values.
	if op == bytecode.BinEqlIface {
		return v.ifaceEql(f, a, b)
	}
	// constant payloads the operands arrived with: an op where every
	// operand is still a constant is gc's constant EXPRESSION — it
	// evaluates in the declared type's exact domain and an
	// unrepresentable result is a compile error, where the same op on
	// a var wraps (`uint(4) - 8` errors, `v - 8` wraps).
	ca, aIsConst := constPayload(a)
	cb, bIsConst := constPayload(b)
	// a non-constant shift's result (Wrap) is a value, not a constant.
	aIsConst = aIsConst && !ca.Wrap
	bIsConst = bIsConst && !cb.Wrap
	if op != bytecode.BinShl && op != bytecode.BinShr {
		if r, ok := v.wrapConstBinary(f, op, &a, &b); ok {
			return r
		}
	}
	// untyped constants fold in the arbitrary-precision constant domain
	// while both sides read as constants — a bare int64/float64 operand
	// came from a folded literal and can lift back (`const C = B - 1<<99`
	// computes exactly). With a real value a constant materializes to
	// its default type instead (and can fail to, like Go's compile-time
	// "constant overflows int"). The exception is comparisons: Go
	// converts the constant to the operand's type and compares the
	// resulting values, so `f == 1e100-1` rounds the constant into
	// float64 where an exact-domain compare would report false.
	if ua, isA := a.(*runtime.UConst); isA {
		resolved := false
		if _, isB := b.(*runtime.UConst); isB || isPlainConst(b) {
			switch {
			case !isB && isCompareOp(op):
				if s, ok := scalarConst(ua, b); ok {
					a, resolved = s, true
				}
			case op == bytecode.BinShl || op == bytecode.BinShr:
				// a shift is never constant-folded when one side is a
				// runtime value — shiftOp reads a constant count as the
				// unsigned integer Go requires (`x << (math.MaxUint+0)`),
				// so the operand keeps its UConst form.
				resolved = true
			default:
				// folding needs a constant on BOTH sides — a bare
				// scalar is a runtime value now that every literal
				// emits as a UConst, so `x + 1` on a variable stays
				// concrete instead of folding into a constant.
				if isB {
					ca, _ := constOf(a)
					cb, _ := constOf(b)
					if r, ok := constBinary(op, ca, cb); ok {
						return r
					}
				}
			}
		}
		if !resolved && op != bytecode.BinShl && op != bytecode.BinShr {
			// a shift types its result by the left operand alone: an
			// untyped-constant left side keeps its constness whatever
			// the count's type is — `32 << tag{uint,1}` folds the
			// untyped constant 64, not a tagged uint.
			a = v.adaptConst(f, ua, b)
		}
	}
	if ub, ok := b.(*runtime.UConst); ok {
		resolved := false
		switch {
		case op == bytecode.BinShl || op == bytecode.BinShr:
			// a shift is never constant-folded when one side is a
			// runtime value — shiftOp reads a constant count as the
			// unsigned integer Go requires (`x << (math.MaxUint+0)`),
			// whatever the left operand's shape.
			resolved = true
		case isPlainConst(a):
			if isCompareOp(op) {
				if s, ok := scalarConst(ub, a); ok {
					b, resolved = s, true
				}
			}
			// a bare scalar operand is a runtime value, not a
			// constant — the constant adapts to it below rather
			// than folding `x + 1` into a new constant.
		}
		if !resolved {
			b = v.adaptConst(f, ub, a)
		}
	}
	// shifts evaluate in the left operand's signedness — Go types the
	// result by the left side alone, so `^uintptr(0) >> 63` must shift
	// logically, not as int64. They get their own operator.
	if op == bytecode.BinShl || op == bytecode.BinShr {
		// `uint8(1) << 8` is a constant expression too: fold in the
		// exact domain and range-check against the LEFT operand's tag.
		if aIsConst && bIsConst {
			if n, ok := a.(*runtime.Named); ok && numericBasicName(basicNameOf(v.peelNamed(n.Typ))) {
				if r, ok := constBinary(op, ca, cb); ok {
					if ru, isConst := r.(*runtime.UConst); isConst {
						if _, err := v.materializeConstErr(ru, n.Typ); err != nil {
							f.trap("%s", err)
						}
						return runtime.Tag(n.Typ, ru)
					}
					return r
				}
			}
		}
		return v.shiftOp(f, op, a, b, flags)
	}
	// named basic values operate on their underlying value; two different
	// declared types in one operation is a type error (Go: `x + y` on
	// MyInt and Other traps), and an arithmetic result keeps the
	// operand's declared tag — comparisons produce an untyped bool, which
	// stays bare. The unwrap happens before the complex branch so a
	// declared complex type is checked too.
	var tag *runtime.TypeDef
	var aTd, bTd *runtime.TypeDef
	if op >= bytecode.BinEql && op <= bytecode.BinEqlIface && (isGoValue(a) || isGoValue(b)) {
		// comparisons only: an arithmetic result keeps the host value's
		// own type, whose host String prints it (Go sees one type)
		a, b = runtime.TwinOperands(a, b)
	}
	if n, ok := a.(*runtime.Named); ok {
		tag = n.Typ
		aTd = n.Typ
		a = n.V
	}
	if n, ok := b.(*runtime.Named); ok {
		if aTd != nil && !sameTypeDef(aTd, n.Typ) {
			// equality is lawful between differently-typed dynamic
			// values — `any(u64) != any(u32)` is true in Go; the
			// ordered ops and arithmetic stay a type error.
			if isCompareOp(op) && (op == bytecode.BinEql || op == bytecode.BinNeq) {
				return op == bytecode.BinNeq
			}
			f.trap("invalid operation: mismatched types %s and %s", tdName(aTd), tdName(n.Typ))
		}
		tag = n.Typ
		bTd = n.Typ
		b = n.V
	}
	// a named operand against a bare scalar — a bound call's return or a
	// package constant that arrived materialized — mismatches the same
	// way two differently-typed operands do (`Duration < len(x)` fails
	// like `Duration < int`); the escape is the same as in Go — convert.
	// `==`/`!=` stay dynamic here too: an `any`-carried scalar reaches
	// this path and interface equality is lawful.
	if (aTd != nil) != (bTd != nil) && op != bytecode.BinEql && op != bytecode.BinNeq {
		var nt *runtime.TypeDef
		var bare runtime.Value
		bareFirst := false
		if aTd != nil {
			nt, bare = aTd, b
		} else {
			nt, bare, bareFirst = bTd, a, true
		}
		if utd := v.peelNamed(nt); utd == nil || utd.Kind != runtime.KindInterface {
			if name, ok := bareScalarName(bare); ok && !v.bareScalarShared(bare, nt) {
				if bareFirst {
					f.trap("invalid operation: mismatched types %s and %s", name, tdName(nt))
				}
				f.trap("invalid operation: mismatched types %s and %s", tdName(nt), name)
			}
		}
	}
	// two bare scalars — both sides host-carried or materialized — check
	// like two named operands, on domains (`len(x) + "s"` fails like
	// `int + string`). Equality stays dynamic for the same any-carried
	// reason as above.
	if aTd == nil && bTd == nil && op != bytecode.BinEql && op != bytecode.BinNeq {
		if an, aok := bareScalarDomain(a); aok {
			if bn, bok := bareScalarDomain(b); bok && bn != an {
				an2, _ := bareScalarName(a)
				bn2, _ := bareScalarName(b)
				f.trap("invalid operation: mismatched types %s and %s", an2, bn2)
			}
		}
	}
	if tag != nil && aIsConst && bIsConst && numericBasicName(basicNameOf(v.peelNamed(tag))) {
		// a constant expression evaluates in the tag's exact domain and
		// an unrepresentable result is gc's `constant ... overflows`
		// reject (`uint8(250) + 10`, `int8(120) + 10`, `uint8(1) << 8`,
		// `complex64(...) + complex64(...)` past float32 range). The
		// folded result keeps the tag so chained ops keep checking.
		// Both payloads read in the tag's kind first: `float64(7)`
		// carries the Int payload 7, but `float64(7) / 2` divides as
		// float64 — 3.5, not the integer quotient.
		// both payloads are immutable typed constants, so the folded
		// result is memoized on the left one: `s += lk * k` re-folds
		// the same pair on every iteration.
		key := typedFoldKey{op: op, other: cb, tag: tag}
		if r, hit := ca.Memo(key); hit {
			return r
		}
		tn := basicNameOf(v.peelNamed(tag))
		if r, ok := constBinary(op, constInKind(ca, tn), constInKind(cb, tn)); ok {
			if ru, isConst := r.(*runtime.UConst); isConst {
				if _, err := v.materializeConstErr(ru, tag); err != nil {
					f.trap("%s", err)
				}
				r = runtime.Tag(tag, ru)
			}
			ca.SetMemo(key, r) // comparisons fold to an untyped bool
			return r
		}
		// unfoldable — division by zero, mismatched kinds — falls
		// through to the runtime path for its own trap.
	}
	// complex values operate in the complex domain: complex64 wins over
	// complex128, ordered comparisons are a compile reject in Go.
	_, _, aIs := asComplex(a)
	_, _, bIs := asComplex(b)
	if aIs || bIs {
		ca, wa, aok := complexOperand(a)
		cb, wb, bok := complexOperand(b)
		if aok && bok {
			if wa != 0 && wb != 0 && wa != wb {
				f.trap("invalid operation: %s (mismatched types complex%d and complex%d)", op, wa, wb)
			}
			var r runtime.Value
			switch op {
			case bytecode.BinAdd:
				r = complexResult(ca+cb, wa, wb)
			case bytecode.BinSub:
				r = complexResult(ca-cb, wa, wb)
			case bytecode.BinMul:
				r = complexResult(ca*cb, wa, wb)
			case bytecode.BinQuo:
				r = complexResult(ca/cb, wa, wb)
			case bytecode.BinEql:
				r = ca == cb
			case bytecode.BinNeq:
				r = ca != cb
			default:
				f.trap("invalid operation: %s (complex numbers are not ordered)", op)
			}
			if tag != nil {
				if _, isBool := r.(bool); !isBool {
					return runtime.Tag(tag, r)
				}
			}
			return r
		}
	}
	if tag != nil {
		// an unsigned-width declared int evaluates `/`, `%` and ordered
		// comparisons in the uint64 domain — same bits as int64 for the
		// rest, so only those ops differ.
		if uname := sizedNameOf(tag); unsignedName(uname) {
			// a still-constant operand materializes at the declared
			// width first — the both-const fold above already ran, so
			// this is a const riding next to a var (`v - 8`): the
			// concrete uint op wraps, never the exact-domain reject.
			a = v.materializeOperandConst(f, a, tag)
			b = v.materializeOperandConst(f, b, tag)
			if ua, aok := uintOperand(a); aok {
				if ub, bok := uintOperand(b); bok {
					res, isInt := uintBinOp(f, op, ua, ub)
					if !isInt {
						return res
					}
					return runtime.Tag(tag, maskInt(int64(res.(uint64)), uname))
				}
			}
		}
		res := v.binaryOp(f, op, a, b)
		if iv, ok := res.(int64); ok {
			return runtime.Tag(tag, maskInt(iv, sizedNameOf(tag)))
		}
		if fv, ok := res.(float64); ok {
			// a float32-flavored tag narrows the result the way an
			// assignment into a float32 slot does.
			if basicNameOf(tag) == "float32" {
				fv = float64(float32(fv))
			}
			return runtime.Tag(tag, fv)
		}
		if _, ok := res.(string); ok {
			return runtime.Tag(tag, res)
		}
		if uc, ok := res.(*runtime.UConst); ok && uc.V.Kind() != constant.Bool {
			// a folded result of a typed constant stays a typed
			// constant — `Duration(5) + 1` is still a Duration
			// constant; comparisons produce untyped bools.
			return runtime.Tag(tag, uc)
		}
		return res
	}
	// equality works on any comparable pair and must see the boxed
	// dynamic types — unwrapping a host numeric here would turn
	// `any(time.Weekday(4)) == any(int(4))` into a true int64 compare
	// where Go reports false.
	switch op {
	case bytecode.BinEql:
		return v.eqlValue(a, b)
	case bytecode.BinNeq:
		return !v.eqlValue(a, b)
	}
	// bound time.* constants and reflect-produced durations arrive as
	// raw time.Duration values — they behave as their int64 underlying
	// in arithmetic and comparisons (2*time.Second, d < timeout), and
	// a result on a duration operand stays a duration (except d/d,
	// which is unitless like Go).
	var dmark, dboth bool
	if d, ok := a.(time.Duration); ok {
		a = int64(d)
		dmark = true
	}
	if d, ok := b.(time.Duration); ok {
		b = int64(d)
		if dmark {
			dboth = true
		}
		dmark = true
	}
	// uint64 literals too wide for int64 stay boxed as GoValue;
	// arithmetic on them computes mod 2^64 in int64 (same bits) and
	// re-boxes so formatting keeps the unsigned domain.
	var ubox bool
	// numTag remembers the named host int an operand was unwrapped from
	// (reflect.Kind and friends) so an arithmetic result can be re-boxed
	// into the same type — `reflect.Int + 1` stays a reflect.Kind.
	var numTag reflect.Type
	var strTag reflect.Type
	var strBoxed bool
	if g, ok := a.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			a = int64(u)
			ubox = true
		} else if iv, ok := runtime.SmallIntOf(g.V); ok {
			// a GoValue carrying an ordered numeric (reflect.Kind and
			// other named host ints) unwraps for arithmetic and
			// comparisons like an ordinary named int.
			a = iv
			numTag = reflect.TypeOf(g.V)
		}
	}
	if g, ok := b.(*runtime.GoValue); ok {
		if u, isU := g.V.(uint64); isU {
			b = int64(u)
			ubox = true
		} else if iv, ok := runtime.SmallIntOf(g.V); ok {
			b = iv
			if bt := reflect.TypeOf(g.V); numTag != nil && bt != numTag {
				// Go rejects arithmetic on differently-named ints
				// (reflect.Kind + time.Weekday) at compile time.
				f.trap("invalid operation: mismatched types %s and %s", numTag, bt)
			} else {
				numTag = bt
			}
		}
	}
	// a host named string (template.HTML) operates as its underlying
	// string — boxed, or raw under a Named tag. A boxed operand's
	// string result is boxed back into the host type, as in Go.
	// (the int64 check keeps the common arithmetic path call-free)
	if _, isInt := a.(int64); !isInt {
		if hs, ok := hostStrOperand(a); ok {
			a = hs.s
			strTag, strBoxed = hs.rt, hs.boxed
		}
	}
	if _, isInt := b.(int64); !isInt {
		if hs, ok := hostStrOperand(b); ok {
			if strTag != nil && hs.rt != strTag {
				f.trap("invalid operation: mismatched types %s and %s", strTag, hs.rt)
			}
			b = hs.s
			strTag, strBoxed = hs.rt, strBoxed || hs.boxed
		}
	}
	if s, ok := a.(string); ok {
		res := stringBinOp(f, op, s, b)
		if rs, isStr := res.(string); isStr && strBoxed {
			return &runtime.GoValue{V: reflect.ValueOf(rs).Convert(strTag).Interface()}
		}
		return res
	}
	if isFloat(a) || isFloat(b) {
		return floatBinOp(f, op, toFloat(a), toFloat(b))
	}
	if ai, ok := a.(int64); ok {
		bi, ok := b.(int64)
		if !ok {
			f.trap("unsupported types: %T %s %T", a, op, b)
		}
		if ubox {
			res, isInt := uintBinOp(f, op, uint64(ai), uint64(bi))
			if isInt {
				return &runtime.GoValue{V: res}
			}
			return res
		}
		res := intBinOp(f, op, ai, bi)
		if iv, isI := res.(int64); isI {
			switch {
			case dmark && op == bytecode.BinQuo && dboth:
				return iv // d/d is unitless in Go
			case dmark:
				return time.Duration(iv)
			case numTag != nil:
				// re-box into the unwrapped operand's named host type —
				// `reflect.Int + 1` is a reflect.Kind, not an int.
				rv := reflect.New(numTag).Elem()
				switch numTag.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					rv.SetInt(iv)
				default:
					rv.SetUint(uint64(iv))
				}
				return &runtime.GoValue{V: rv.Interface()}
			}
		}
		return res
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		if !ok {
			f.trap("unsupported types: %T %s %T", a, op, b)
		}
		switch op {
		case bytecode.BinLAnd:
			return ab && bb
		case bytecode.BinLOr:
			return ab || bb
		}
	}
	f.trap("unsupported types: %T %s %T", a, op, b)
	return nil
}

// hostString is a host named string operand read as a plain string.
type hostString struct {
	s     string
	rt    reflect.Type
	boxed bool // carried in a GoValue (else a raw payload under a Named tag)
}

// hostStrOperand reads a host named string (template.HTML) — boxed in
// a GoValue, or raw — as its string. Plain strings and the script's own
// scalars are not host strings.
func hostStrOperand(x runtime.Value) (hs hostString, ok bool) {
	switch g := x.(type) {
	case string, int64, float64, bool, nil:
		return hs, false
	case *runtime.GoValue:
		x, hs.boxed = g.V, true
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() != reflect.String || rv.Type() == reflect.TypeFor[string]() {
		return hs, false
	}
	hs.s, hs.rt = rv.String(), rv.Type()
	return hs, true
}

// uintOperand reads an int-domain runtime value as uint64: int64s carry
// two's-complement bits already and a GoValue holds its boxed unsigned
// width verbatim.
func uintOperand(v runtime.Value) (uint64, bool) {
	switch x := v.(type) {
	case int64:
		return uint64(x), true
	case *runtime.GoValue:
		return hostUint(x.V)
	}
	return 0, false
}

// uintBinOp is intBinOp evaluated in the uint64 domain: only `/`, `%` and
// the ordered comparisons differ from the signed reading — bit ops and
// add/sub/mul produce identical bits. Integer results arrive as uint64.
func uintBinOp(f *frame, op bytecode.BinOp, a, b uint64) (res runtime.Value, isInt bool) {
	switch op {
	case bytecode.BinAdd:
		return a + b, true
	case bytecode.BinSub:
		return a - b, true
	case bytecode.BinMul:
		return a * b, true
	case bytecode.BinQuo:
		return a / b, true
	case bytecode.BinRem:
		return a % b, true
	case bytecode.BinAnd:
		return a & b, true
	case bytecode.BinOr:
		return a | b, true
	case bytecode.BinXor:
		return a ^ b, true
	case bytecode.BinAndNot:
		return a &^ b, true
	case bytecode.BinLss:
		return a < b, false
	case bytecode.BinLeq:
		return a <= b, false
	case bytecode.BinGtr:
		return a > b, false
	case bytecode.BinGeq:
		return a >= b, false
	case bytecode.BinEql:
		return a == b, false
	case bytecode.BinNeq:
		return a != b, false
	}
	f.trap("uint binary %s", op)
	return nil, false
}

// constShift folds a constant shift; go/constant panics past its
// representable range, which the caller treats as "didn't fold".
func constShift(lv constant.Value, op bytecode.BinOp, count uint64) (res constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := binOpToken(op)
	if !ok {
		return nil, false
	}
	return constant.Shift(lv, tok, uint(count)), true
}

// shiftOp evaluates << and >> in the left operand's signedness: Go types
// the result by the left side alone (the count is always an unsigned
// count), so a uintptr/uint64 value shifts logically while int64 shifts
// arithmetically. A declared-width operand re-tags and re-masks.
func (v *VM) shiftOp(f *frame, op bytecode.BinOp, a, b runtime.Value, flags int) runtime.Value {
	staticConst := flags&bytecode.ShiftConstCount != 0
	sure := flags&bytecode.ShiftNonConstCount != 0
	var count uint64
	var countOK bool
	// a constant count — untyped or typed — keeps a constant left
	// operand's shift a constant expression; any other count makes it
	// a non-constant shift (`1 << n` for a variable n). The compiler
	// knows statically (staticConst): a typed constant count arrives
	// as a plain typed value (`const T uint = 3`, `len("abc")`), which
	// the value alone cannot tell from a variable.
	cu, countConst := constPayload(b)
	countConst = staticConst || (countConst && !cu.Wrap)
	if uc, isU := b.(*runtime.UConst); isU {
		// an untyped constant count is legal whenever it is
		// representable as an unsigned integer (`x << (M+0)` with M =
		// math.MaxUint reads as uint64), even past int64 —
		// materializeDefault would overflow-report where Go does not.
		iv := uc.V
		if iv.Kind() != constant.Int {
			// integer-valued Float/Complex constants (`1.`, `1+0i`)
			// count too; non-integral ones fall through to the
			// materialized-count path, which rejects them like Go.
			if ti, ok := toIntConst(iv); ok {
				iv = ti
			}
		}
		if iv.Kind() == constant.Int {
			if s, ok := constant.Uint64Val(iv); ok {
				count, countOK = s, true
			}
		}
	}
	if !countOK {
		b = v.materialize(f, b)
		count, countOK = shiftCount(b)
	}
	if !countOK {
		f.trap("unsupported shift count %T", b)
	}
	if uc, isU := a.(*runtime.UConst); isU && !countConst && flags&bytecode.ShiftTypedLeft != 0 && !uc.Wrap {
		// a typed left operand (`int(1) << n`) can arrive as a bare
		// constant payload; it is no untyped constant to take the
		// context's type — it shifts as the int value it is.
		a = v.materialize(f, uc)
	}
	if uc, isU := a.(*runtime.UConst); isU && !(uc.Wrap && op == bytecode.BinShr) {
		// both sides in the constant domain: Go folds the shift —
		// `const c1 = chuge >> 100` (chuge = 1<<100) is the constant
		// 1. A float/complex LHS reads as its integer value; one that
		// is not integral falls through and materializes.
		//
		// With a non-constant count the untyped left operand takes
		// its type from the context instead (`m & (1 << n)` shifts a
		// FileMode, `fmt.Println(1 << n)` an int), which is not known
		// here: the exact result rides as a Wrap value that truncates
		// to whatever width it materializes at — `1 << 70` as an int
		// is 0, not gc's "constant overflows" reject.
		// (A Wrap left operand under >> settles at int first: a right
		// shift does not commute with truncation.)
		lv := uc.V
		if lv.Kind() != constant.Int {
			lv, _ = toIntConst(lv)
		}
		if lv != nil {
			if wrap := uc.Wrap || !countConst; wrap {
				// a Wrap value only matters mod 2^64, so it never
				// computes the exact big result: `1 << (1<<40)`
				// would allocate a terabit integer.
				if op == bytecode.BinShl && count >= 64 {
					return &runtime.UConst{V: constant.MakeInt64(0), Rune: uc.Rune, Wrap: true, WrapSure: sure || uc.WrapSure}
				}
				if res, ok := constShift(lv, op, count); ok {
					return &runtime.UConst{V: wrapReduce(res), Rune: uc.Rune, Wrap: true, WrapSure: sure || uc.WrapSure}
				}
			} else if res, ok := constShift(lv, op, count); ok {
				return &runtime.UConst{V: res, Rune: uc.Rune}
			}
		}
	}
	a = v.materialize(f, a)
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag, a = n.Typ, n.V
	}
	unsigned := unsignedName(sizedNameOf(tag))
	switch x := a.(type) {
	case int64:
		var r int64
		if unsigned {
			r = int64(shiftUint(op, uint64(x), count))
		} else {
			r = shiftInt(op, x, count)
		}
		if tag != nil {
			return runtime.Tag(tag, maskInt(r, sizedNameOf(tag)))
		}
		return r
	case time.Duration:
		return time.Duration(shiftInt(op, int64(x), count))
	case float64:
		// an untyped constant left operand of a non-constant shift
		// takes the integer interpretation (`x<<(1.<<x)` is int
		// arithmetic in Go); a genuine float64 operand reaching here
		// is a program Go rejects anyway.
		if x == math.Trunc(x) {
			if x >= math.MaxInt64 {
				x = math.MaxInt64
			} else if x <= math.MinInt64 {
				x = math.MinInt64
			}
			return shiftInt(op, int64(x), count)
		}
	case *runtime.GoValue:
		if c, isC := x.V.(complex128); isC {
			re, im := real(c), imag(c)
			if im == 0 && re == math.Trunc(re) {
				if re >= math.MaxInt64 {
					re = math.MaxInt64
				} else if re <= math.MinInt64 {
					re = math.MinInt64
				}
				return shiftInt(op, int64(re), count)
			}
		}
		if u, ok := x.V.(uint64); ok {
			r := shiftUint(op, u, count)
			if tag != nil {
				return runtime.Tag(tag, maskInt(int64(r), sizedNameOf(tag)))
			}
			return &runtime.GoValue{V: r}
		}
	}
	f.trap("unsupported types: %T %s %T", a, op, b)
	return nil
}

// shiftCount reads the right operand of a shift as an unsigned count —
// a negative count panics like Go's runtime error. A Named unsigned tag
// reinterprets the int64 bits (`x << uint(-4)` shifts by 2^64-4, not -4).
func shiftCount(b runtime.Value) (uint64, bool) {
	if n, ok := b.(*runtime.Named); ok {
		if unsignedName(sizedNameOf(n.Typ)) {
			if iv, ok := n.V.(int64); ok {
				return uint64(iv), true
			}
		}
	}
	ux := runtime.Unwrap(b)
	switch x := ux.(type) {
	case int64:
		if x < 0 {
			panic(runtime.NegativeShiftPanic())
		}
		return uint64(x), true
	case float64:
		// an untyped float constant is a legal count when it is
		// integral (`x << 1.`); a non-integral or negative one is a
		// compile-time rejection in Go, so it keeps trapping here.
		if x != math.Trunc(x) {
			return 0, false
		}
		if x < 0 {
			panic(runtime.NegativeShiftPanic())
		}
		if x >= math.MaxUint64 {
			return math.MaxUint64, true
		}
		return uint64(x), true
	case *runtime.GoValue:
		if u, ok := hostUint(x.V); ok {
			return u, true
		}
		switch u := x.V.(type) {
		case complex64:
			return complexShiftCount(complex128(u))
		case complex128:
			return complexShiftCount(u)
		}
	}
	// a raw host integer scalar (time.Duration) counts by its int64
	// value — `1 << d` on a Duration is legal like any named int.
	if iv, ok := runtime.SmallIntOf(ux); ok {
		if iv < 0 {
			panic(runtime.NegativeShiftPanic())
		}
		return uint64(iv), true
	}
	return 0, false
}

// complexShiftCount reads a complex constant as a shift count: a zero
// imaginary part and integral real part make `x << (1+0i)` legal, like
// an integral float constant.
func complexShiftCount(c complex128) (uint64, bool) {
	re, im := real(c), imag(c)
	if im != 0 || re != math.Trunc(re) {
		return 0, false
	}
	if re < 0 {
		panic(runtime.NegativeShiftPanic())
	}
	if re >= math.MaxUint64 {
		return math.MaxUint64, true
	}
	return uint64(re), true
}

// shiftInt shifts int64 with Go's saturation: `x << c` loses bits past
// 64 (zero fill), `x >> c` sign-fills for a negative left operand.
func shiftInt(op bytecode.BinOp, a int64, c uint64) int64 {
	if c >= 64 {
		if op == bytecode.BinShl || a >= 0 {
			return 0
		}
		return -1
	}
	if op == bytecode.BinShl {
		return a << c
	}
	return a >> c
}

func shiftUint(op bytecode.BinOp, a uint64, c uint64) uint64 {
	if c >= 64 {
		return 0
	}
	if op == bytecode.BinShl {
		return a << c
	}
	return a >> c
}

func intBinOp(f *frame, op bytecode.BinOp, a, b int64) runtime.Value {
	switch op {
	case bytecode.BinAdd:
		return a + b
	case bytecode.BinSub:
		return a - b
	case bytecode.BinMul:
		return a * b
	case bytecode.BinQuo:
		return a / b
	case bytecode.BinRem:
		return a % b
	case bytecode.BinAnd:
		return a & b
	case bytecode.BinOr:
		return a | b
	case bytecode.BinXor:
		return a ^ b
	case bytecode.BinAndNot:
		return a &^ b
	case bytecode.BinLss:
		return a < b
	case bytecode.BinLeq:
		return a <= b
	case bytecode.BinGtr:
		return a > b
	case bytecode.BinGeq:
		return a >= b
	}
	f.trap("int binary %s", op)
	return nil
}

func floatBinOp(f *frame, op bytecode.BinOp, a, b float64) runtime.Value {
	switch op {
	case bytecode.BinAdd:
		return a + b
	case bytecode.BinSub:
		return a - b
	case bytecode.BinMul:
		return a * b
	case bytecode.BinQuo:
		return a / b
	case bytecode.BinLss:
		return a < b
	case bytecode.BinLeq:
		return a <= b
	case bytecode.BinGtr:
		return a > b
	case bytecode.BinGeq:
		return a >= b
	}
	f.trap("float binary %s", op)
	return nil
}

func stringBinOp(f *frame, op bytecode.BinOp, a string, b runtime.Value) runtime.Value {
	s, ok := b.(string)
	if !ok {
		f.trap("string binary on %T", b)
	}
	switch op {
	case bytecode.BinAdd:
		return a + s
	case bytecode.BinLss:
		return a < s
	case bytecode.BinLeq:
		return a <= s
	case bytecode.BinGtr:
		return a > s
	case bytecode.BinGeq:
		return a >= s
	}
	f.trap("string binary %s", op)
	return nil
}

// unOpToken maps a bytecode unary op to its go/token spelling.
func unOpToken(op bytecode.UnOp) (token.Token, bool) {
	switch op {
	case bytecode.UnPos:
		return token.ADD, true
	case bytecode.UnNeg:
		return token.SUB, true
	case bytecode.UnNot:
		return token.NOT, true
	case bytecode.UnXor:
		return token.XOR, true
	}
	return 0, false
}

// constUnary applies a unary op in the constant domain; ok is false
// when the op doesn't apply to the constant's kind (go/constant
// panics on those — the runtime path below reports them).
func constUnary(op bytecode.UnOp, u *runtime.UConst) (res *runtime.UConst, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := unOpToken(op)
	if !ok {
		return nil, false
	}
	// -x and ^x agree with every width's truncation, so a non-constant
	// shift result (Wrap) stays one through them.
	cv := constant.UnaryOp(tok, u.V, 0)
	if u.Wrap {
		cv = wrapReduce(cv)
	}
	return &runtime.UConst{V: cv, Rune: u.Rune, Wrap: u.Wrap, WrapSure: u.WrapSure}, true
}

func (v *VM) unaryOp(f *frame, op bytecode.UnOp, a runtime.Value) runtime.Value {
	var tag *runtime.TypeDef
	if n, ok := a.(*runtime.Named); ok {
		tag, a = n.Typ, n.V
	}
	if u, ok := a.(*runtime.UConst); ok {
		// unary ops on constants stay in the constant domain — a typed
		// constant keeps its tag so -float64(0) is +0, not -0.0.
		if cv, ok2 := constUnary(op, u); ok2 {
			if tag == nil {
				return cv
			}
			if _, err := v.materializeConstErr(cv, tag); err == nil {
				return runtime.Tag(tag, cv)
			}
			if !unsignedName(sizedNameOf(tag)) || op != bytecode.UnXor {
				// the constant-domain result is not representable in
				// the declared type — `-uint(5)`/`-int8(-128)` is Go's
				// "constant overflows" compile error.
				f.trap("constant %s overflows %s", cv.V, runtime.DisplayName(tag))
			}
			// only ^x on an unsigned tag applies the op at the
			// declared width: the constant domain's ^uint(0) is -1,
			// but the uint constant is maxuint — materialize, run the
			// concrete op, and retag masks it back into the width.
			a = runtime.Unwrap(v.materializeConst(f, u, tag))
		} else {
			a = v.materialize(f, a)
		}
	}
	// unary results keep the operand's declared type (-x, +x, ^x, !x are
	// all typed T when x is T); a bare result stays bare. A sized-int
	// operand wraps the result to its width — -uint8(5) is 251, not -5.
	retag := func(r runtime.Value) runtime.Value {
		if tag != nil {
			if iv, ok := r.(int64); ok {
				return runtime.Tag(tag, maskInt(iv, sizedNameOf(tag)))
			}
			switch r.(type) {
			case float64, string, bool, *runtime.GoValue:
				return runtime.Tag(tag, r)
			}
		}
		return r
	}
	switch op {
	case bytecode.UnNot:
		return retag(!truthy(a))
	case bytecode.UnPos:
		return retag(a)
	case bytecode.UnNeg:
		switch x := a.(type) {
		case int64:
			return retag(-x)
		case float64:
			return retag(-x)
		case time.Duration:
			return retag(-x)
		case *runtime.GoValue:
			// -u on a wide uint64 wraps mod 2^64, re-boxed so the unsigned
			// domain survives.
			if u, ok := x.V.(uint64); ok {
				return retag(&runtime.GoValue{V: -u})
			}
			switch c := x.V.(type) {
			case complex64:
				return &runtime.GoValue{V: -c}
			case complex128:
				return &runtime.GoValue{V: -c}
			}
		}
		f.trap("unary - on %T", a)
	case bytecode.UnXor:
		if x, ok := a.(int64); ok {
			return retag(^x)
		}
		if g, ok := a.(*runtime.GoValue); ok {
			if u, ok := g.V.(uint64); ok {
				return retag(&runtime.GoValue{V: ^u})
			}
		}
		// raw host integer scalars (^time.Duration) complement in the
		// int64 domain and retag to the operand's declared type.
		if iv, ok := runtime.SmallIntOf(a); ok {
			return retag(^iv)
		}
		f.trap("unary ^ on %T", a)
	}
	f.trap("unary %s on %T", op, a)
	return nil
}

// hostNamedNonString is [runtime.HostNamedScalar] minus the string kind:
// host-boxed strings keep comparing by contents (hostStringOperand).
func hostNamedNonString(x runtime.Value) (runtime.Value, reflect.Type, bool) {
	p, rt, ok := runtime.HostNamedScalar(x)
	if !ok || rt.Kind() == reflect.String {
		return nil, nil, false
	}
	return p, rt, true
}

// hostNamedEql decides a == b when either side is a boxed host named
// basic (io/fs's FileMode from the host os, a go/token Token off
// inspect.AST, reflect.Kind): Go compares (type, value), so it equals a
// box of the same host type holding the same value, or a value tagged
// with its source twin or bound typedef ([runtime.HostTwinOf]) holding
// the same payload — never a look-alike of another type. ok is false
// when neither side is such a box.
func hostNamedEql(a, b runtime.Value) (eq, ok bool) {
	pa, rta, okA := hostNamedNonString(a)
	pb, rtb, okB := hostNamedNonString(b)
	switch {
	case okA && okB:
		return rta == rtb && pa == pb, true
	case okA:
		return twinPayloadEql(pa, rta, b), true
	case okB:
		return twinPayloadEql(pb, rtb, a), true
	}
	return false, false
}

// twinPayloadEql reports whether x is a value of the host type rt whose
// payload is p: a Named of rt's twin typedef, or a raw host scalar of rt.
func twinPayloadEql(p runtime.Value, rt reflect.Type, x runtime.Value) bool {
	n, ok := x.(*runtime.Named)
	if !ok {
		// an untagged script scalar is another type; a raw host
		// scalar may still be one of rt.
		q, qt, ok := runtime.HostNamedPayload(x)
		return ok && qt == rt && p == q
	}
	if !runtime.HostTwinOf(rt, n.Typ) {
		return false
	}
	switch q := n.V.(type) {
	case int64, string, bool:
		return p == q
	case *runtime.UConst:
		mv, err := runtime.UConstNative(q)
		return err == nil && p == runtime.Unwrap(mv)
	}
	q, _, ok := runtime.HostNamedPayload(n.V)
	return ok && p == q
}

// twinSlot folds a boxed host named basic stored in a slot declared as
// its twin typedef td (a struct field, an array element) to the Named
// that slot's source values hold, so eqlValue compares it with a value
// the slot stored untagged. Any other value, or an interface-typed slot,
// passes through: there the dynamic type decides.
func twinSlot(td *runtime.TypeDef, x runtime.Value) runtime.Value {
	if td == nil || td.Kind == runtime.KindInterface {
		return x
	}
	if p, rt, ok := hostNamedNonString(x); ok && runtime.HostTwinOf(rt, td) {
		return runtime.Tag(td, p)
	}
	return x
}

func (v *VM) eqlValue(a, b runtime.Value) bool {
	// a boxed host named basic compares as a (type, value) pair before
	// the tags peel: rec{fi.Mode().Type()} == rec{fs.ModeDir} meets the
	// source twin field by field, while S{m} == S{uint32(fs.ModeDir)}
	// with an `any` field stays false like Go.
	if isGoValue(a) || isGoValue(b) {
		if eq, ok := hostNamedEql(a, b); ok {
			return eq
		}
	}
	if n, ok := a.(*runtime.Named); ok {
		a = n.V
	}
	if n, ok := b.(*runtime.Named); ok {
		b = n.V
	}
	// raw time.Duration values compare by their int64 nanoseconds.
	if d, ok := a.(time.Duration); ok {
		a = int64(d)
	}
	if d, ok := b.(time.Duration); ok {
		b = int64(d)
	}
	// a host-boxed string (the facade's StructField.Tag) compares by
	// its string contents — a converted StructTag and a field's Tag
	// compare equal, and field.Tag == "json:..." holds like Go's.
	if gv, ok := a.(*runtime.GoValue); ok {
		a = hostStringOperand(gv)
	}
	if gv, ok := b.(*runtime.GoValue); ok {
		b = hostStringOperand(gv)
	}
	// boxed complex values compare by value across widths — Go rejects
	// complex64 == complex128 (mismatched types), but here both operands
	// already passed the tag check so compare numerically like int/float.
	if ca, _, ok := asComplex(a); ok {
		cb, _, ok2 := asComplex(b)
		return ok2 && ca == cb
	}
	if ai, ok := a.(int64); ok {
		switch bv := b.(type) {
		case int64:
			return ai == bv
		case float64:
			return float64(ai) == bv
		case float32:
			return float64(ai) == float64(bv)
		}
		return false
	}
	if af, ok := a.(float64); ok {
		switch bv := b.(type) {
		case int64:
			return af == float64(bv)
		case float64:
			return af == bv
		case float32:
			return af == float64(bv)
		}
		return false
	}
	// host-returned float32 values (math.Float32frombits, ...) stay raw;
	// compare them numerically so -0 == +0 holds like Go.
	if af, ok := a.(float32); ok {
		switch bv := b.(type) {
		case int64:
			return float64(af) == float64(bv)
		case float64:
			return float64(af) == bv
		case float32:
			return af == bv
		}
		return false
	}
	// two interface values with the same uncomparable dynamic type panic
	// on == — `any([]int(nil)) == any([]int{1})` traps on the *type*,
	// not the values — whatever nil-ness the pair holds.
	if ua := uncomparableDynamicTyp(a); ua != nil {
		if ub := uncomparableDynamicTyp(b); ub != nil && sameTypeDef(ua, ub) {
			panic(runtime.ComparingUncomparablePanic(spelledTyp(ua)))
		}
	}
	if in, ok := a.(*runtime.IfaceNil); ok {
		if runtime.IfaceTaggedNil(in) != nil {
			// a nil interface value: equal to nil and to other nil
			// interfaces (the declared interface type is static-only),
			// but never to a typed nil boxed in an interface.
			switch bi := b.(type) {
			case runtime.Nil:
				return true
			case *runtime.IfaceNil:
				return runtime.IfaceTaggedNil(bi) != nil
			}
			return false
		}
		// interface value holding a typed nil: nil only to a same-typed nil
		switch bi := b.(type) {
		case *runtime.IfaceNil:
			if bt := runtime.BoxedNilTyp(bi); bt != nil {
				return sameTypeDef(in.Typ, bt) || v.sameTypedef(in.Typ, bt)
			}
			return false
		case *runtime.TypedNil:
			return sameTypeDef(in.Typ, bi.Typ) || v.sameTypedef(in.Typ, bi.Typ)
		}
		return false
	}
	if tn, ok := a.(*runtime.TypedNil); ok {
		switch bi := b.(type) {
		case runtime.Nil:
			return true // a nil pointer/slice/map/chan/func == nil
		case *runtime.TypedNil:
			// two typed nils of different dynamic types are just unequal
			// (Go's interface == compares the (type, value) pair);
			// only the SAME type can panic for being uncomparable.
			if !runtime.TypIdentical(tn.Typ, bi.Typ) {
				// a nil host *T equals a nil of its source twin's *T
				return v.sameTypedef(tn.Typ, bi.Typ)
			}
			// a typed nil of a slice/map/func type is still uncomparable —
			// Go panics on the TYPE even when the value is nil.
			if uncomparableTyp(tn.Typ) {
				panic(runtime.ComparingUncomparablePanic(spelledTyp(tn.Typ)))
			}
			return true
		case *runtime.IfaceNil:
			if bt := runtime.BoxedNilTyp(bi); bt != nil {
				return sameTypeDef(tn.Typ, bt) || v.sameTypedef(tn.Typ, bt)
			}
			return false
		}
		return false
	}
	if _, ok := a.(runtime.Nil); ok {
		switch bi := b.(type) {
		case runtime.Nil, *runtime.TypedNil:
			return true
		case *runtime.IfaceNil:
			// a nil interface value equals nil; a typed nil boxed in
			// an interface does not.
			return runtime.IfaceTaggedNil(bi) != nil
		case *runtime.GoValue:
			return hostBoxIsNil(bi)
		}
		return false
	}
	switch av := a.(type) {
	case *runtime.Struct:
		// structs compare field-wise in Go; the defs must name the same
		// type — anonymous struct typedefs with the same field list are
		// the same type (Go's identical-underlying rule). A named type
		// also compares against its anonymous spelling — `s ==
		// struct{a int}{...}` — the same tdShapeEq relax arrays get
		// (field types must spell identically, not just names).
		bs, ok := b.(*runtime.Struct)
		if !ok || len(av.Fields) != len(bs.Fields) {
			return false
		}
		if !runtime.TypIdentical(av.Def, bs.Def) &&
			!(av.Def != nil && bs.Def != nil && v.tdShapeEq(av.Def, bs.Def)) {
			return false
		}
		fts := v.fieldTypedefs(av.Def)
		for i := range av.Fields {
			// an uncomparable field type panics on the struct itself —
			// Go's message names the enclosing type, not the field's.
			if uncomparableValue(av.Fields[i]) || (i < len(fts) && v.uncomparableTypDeep(fts[i])) {
				panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Def)))
			}
			var ft *runtime.TypeDef
			if i < len(fts) {
				ft = fts[i]
			}
			if !v.slotEql(ft, av.Fields[i], bs.Fields[i]) {
				return false
			}
		}
		return true
	case *runtime.Slice:
		if bs, ok := b.(*runtime.Slice); ok {
			// different element types make different dynamic types —
			// any([]int{...}) == any([]string{...}) is false, not a panic.
			// Arrays relax to the underlying shape: `OutputID == [32]any`
			// compares element-wise (issue23545) — the named tag already
			// peels above for scalars, so the same lax rule applies here.
			if !runtime.TypIdentical(av.Typ, bs.Typ) &&
				!(av.Typ != nil && bs.Typ != nil && isArrayTyp(av.Typ) && isArrayTyp(bs.Typ) && v.tdShapeEq(av.Typ, bs.Typ)) {
				return false
			}
			// array-typed values compare element-wise; plain slices are
			// uncomparable and the comparison panics, like Go.
			if isArrayTyp(av.Typ) && isArrayTyp(bs.Typ) {
				if len(av.Elems) != len(bs.Elems) {
					return false
				}
				// an uncomparable element type panics on the array
				// itself — Go names the enclosing type, not the
				// element's. Interface-typed elements stay comparable:
				// the dynamic type decides inside (its own panic).
				et := v.elemTypedef(nil, av.Typ)
				if v.uncomparableTypDeep(et) {
					panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Typ)))
				}
				for i := range av.Elems {
					if !v.slotEql(et, av.Elems[i], bs.Elems[i]) {
						return false
					}
				}
				return true
			}
			panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Typ)))
		}
		return false
	case *runtime.Map:
		if bm, ok := b.(*runtime.Map); ok {
			if !runtime.TypIdentical(av.Typ, bm.Typ) {
				return false
			}
			panic(runtime.ComparingUncomparablePanic(spelledTyp(av.Typ)))
		}
		return false
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch b.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			// Go names the signature: "comparing uncomparable type func()".
			panic(runtime.ComparingUncomparablePanic(spelledTyp(v.typeOfValue(a))))
		}
		return false
	case *runtime.Chan:
		if bc, ok := b.(*runtime.Chan); ok {
			// channel identity is the underlying channel, not the
			// wrapper — assigning a chan to a directional-chan slot
			// re-stamps the container with a fresh *Chan header, so
			// `var r <-chan T = c; c == r` must still be true.
			return av.C == bc.C
		}
		return false
	case *runtime.GoValue:
		// a nil map or slice the embedder handed in as a box (a nil
		// http.Header, net.IP) equals nil, like the typed nil a host
		// call result reads as.
		if _, isNil := b.(runtime.Nil); isNil {
			return hostBoxIsNil(av)
		}
		// host values compare by underlying identity: two wrappers
		// around the same object (e.g. the io.EOF sentinel) are equal.
		if bg, ok := b.(*runtime.GoValue); ok {
			if av.V == nil || bg.V == nil {
				return av.V == bg.V
			}
			t := reflect.TypeOf(av.V)
			if !t.Comparable() {
				panic(runtime.ComparingUncomparablePanic(t.String()))
			}
			return av.V == bg.V
		}
		return false
	case *runtime.Cell:
		if cb, ok := b.(*runtime.Cell); ok {
			// Go allocates zero-size pointees at runtime.zerobase — two
			// distinct cells still compare equal, like `new([0]int)`.
			if zeroSizeValue(av.Elem) && zeroSizeValue(cb.Elem) {
				return true
			}
			return av == cb
		}
		return false
	case *runtime.IndexRef:
		if br, ok := b.(*runtime.IndexRef); ok {
			// &s[i] compares by the backing element slot, not the ref
			// node — (*[N]T)(s) re-views s's backing array, so
			// &s5[0] == &ss[0] even through distinct slice headers.
			as, bs := av.Slice(), br.Slice()
			ai, aok := runtime.Unwrap(av.Key).(int64)
			bi, bok := runtime.Unwrap(br.Key).(int64)
			if as != nil && bs != nil && aok && bok &&
				ai >= 0 && ai < int64(len(as.Elems)) &&
				bi >= 0 && bi < int64(len(bs.Elems)) {
				// zero-size elements share runtime.zerobase:
				// &x[1] == &x[2] for [N][0]byte or []struct{},
				// and slice elements share it even across
				// containers. Distinct ARRAY objects still
				// fold to different objects (&x1[0] != &x2[0]).
				if zeroSizeValue(as.Elems[ai]) && zeroSizeValue(bs.Elems[bi]) {
					if as != bs && isArrayTyp(as.Typ) && isArrayTyp(bs.Typ) {
						return false
					}
					return true
				}
				return &as.Elems[ai] == &bs.Elems[bi]
			}
			return refBase(av.Base) == refBase(br.Base) && v.eqlValue(av.Key, br.Key)
		}
		return false
	case *runtime.FieldRef:
		if br, ok := b.(*runtime.FieldRef); ok {
			// &s.f compares by the resolved field slot — two refs formed
			// by different selector paths to the same promoted field are
			// the same pointer (container/list's l.root.next != &l.root).
			// Fields inside distinct struct objects never alias, even
			// zero-size ones (Go: &x.z != &y.z for distinct x, y).
			ast, ai, aok := av.Find()
			bst, bi, bok := br.Find()
			if !aok || !bok {
				return false
			}
			return ast == bst && ai == bi
		}
		return false
	}
	return a == b // pointers, strings, bools
}

// hostBoxIsNil reports whether a boxed host value holds a nil map,
// slice, pointer, chan or func.
func hostBoxIsNil(gv *runtime.GoValue) bool {
	rv := reflect.ValueOf(gv.V)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}
