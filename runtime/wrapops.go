package runtime

import (
	"go/constant"
	"reflect"

	"github.com/podhmo/minigo/bytecode"
)

// WrapOp is one integer operation deferred on a Wrap constant (see
// UConst.WrapOps): applied to the reduced payload when the value takes
// a concrete width. Arg is the other operand's constant payload;
// ArgFirst marks a non-commutative op where that operand stood left
// of the Wrap (`100 / (1<<n)`).
type WrapOp struct {
	Op       bytecode.BinOp
	Arg      constant.Value
	ArgFirst bool
}

// WrapReplay reduces a Wrap value to a concrete integer: the payload
// and each deferred op apply in the domain mask selects — the
// identity for int64 (the default int), or the width mask a named
// integer type narrows to. A divide or remainder by zero reports
// false so the caller can report it; any other op reports false too
// (WrapOps is a closed set).
func WrapReplay(u *UConst, mask func(int64) int64) (int64, bool) {
	if mask == nil {
		mask = func(x int64) int64 { return x }
	}
	acc := mask(WrapInt64(u.V))
	for _, s := range u.WrapOps {
		l, r := acc, mask(WrapInt64(s.Arg))
		if s.ArgFirst {
			l, r = r, l
		}
		switch s.Op {
		case bytecode.BinAdd:
			acc = mask(l + r)
		case bytecode.BinSub:
			acc = mask(l - r)
		case bytecode.BinMul:
			acc = mask(l * r)
		case bytecode.BinAnd:
			acc = mask(l & r)
		case bytecode.BinOr:
			acc = mask(l | r)
		case bytecode.BinXor:
			acc = mask(l ^ r)
		case bytecode.BinAndNot:
			acc = mask(l &^ r)
		case bytecode.BinQuo:
			if r == 0 {
				return 0, false
			}
			acc = mask(l / r)
		case bytecode.BinRem:
			if r == 0 {
				return 0, false
			}
			acc = mask(l % r)
		case bytecode.BinShl:
			if sh := uint64(WrapInt64(s.Arg)); sh >= 64 {
				acc = 0
			} else {
				acc = mask(acc << sh)
			}
		case bytecode.BinShr:
			// the count is a count, not a masked operand: a count past
			// the width keeps the sign like a Go runtime shift.
			if sh := uint64(WrapInt64(s.Arg)); sh >= 64 {
				if acc < 0 {
					acc = mask(-1)
				} else {
					acc = 0
				}
			} else {
				acc = mask(acc >> sh)
			}
		default:
			return 0, false
		}
	}
	return acc, true
}

// WrapReplayAtKind replays a Wrap value's deferred ops in the domain
// of the reflect integer kind k — the width the constant takes when
// it converts to a host integer type like reflect.Kind.
func WrapReplayAtKind(u *UConst, k reflect.Kind) (int64, bool) {
	switch k {
	case reflect.Int, reflect.Int64:
		return WrapReplay(u, nil)
	case reflect.Int8:
		return WrapReplay(u, func(x int64) int64 { return int64(int8(x)) })
	case reflect.Int16:
		return WrapReplay(u, func(x int64) int64 { return int64(int16(x)) })
	case reflect.Int32:
		return WrapReplay(u, func(x int64) int64 { return int64(int32(x)) })
	case reflect.Uint8:
		return WrapReplay(u, func(x int64) int64 { return int64(uint8(x)) })
	case reflect.Uint16:
		return WrapReplay(u, func(x int64) int64 { return int64(uint16(x)) })
	case reflect.Uint32:
		return WrapReplay(u, func(x int64) int64 { return int64(uint32(x)) })
	default:
		return WrapReplay(u, func(x int64) int64 { return int64(uint64(x)) })
	}
}
