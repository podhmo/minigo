// bounds.go carries the index/slice-bound operand model shared by host
// container indexing: BoundOperand records an index in its declared
// domain (signed int64 or boxed host unsigned) so bounds errors report
// Go's exact panic text, and SliceBoundsReason renders it.
package hostbridge

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

type BoundOperand struct {
	I        int64
	U        uint64
	Unsigned bool
}

// BoundOperandOf builds a BoundOperand — the fields stay private so the
// signedness invariants hold; vm constructs bounds through this.
func BoundOperandOf(i int64, u uint64, unsigned bool) BoundOperand {
	return BoundOperand{I: i, U: u, Unsigned: unsigned}
}

// Pos is the operand's position (wrapped bits for a wide uint64).
func (b BoundOperand) Pos() int64 { return b.I }
func (b BoundOperand) neg() bool  { return !b.Unsigned && b.I < 0 }

func (b BoundOperand) gt(x BoundOperand) bool {
	if b.Unsigned || x.Unsigned {
		return b.U > x.U
	}
	return b.I > x.I
}

func (b BoundOperand) gtc(cap int64) bool {
	if b.Unsigned {
		return b.U > uint64(cap)
	}
	return b.I > cap
}

func (b BoundOperand) disp() any {
	if b.Unsigned {
		return b.U
	}
	return b.I
}

// indexSmallInt reads an index operand as int64, looking inside a
// host GoValue's box — a host uint32(3) indexes like the constant 3,
// while unsigned widths that cannot fit int64 keep the boxed-wide
// out-of-bounds panic path.
func IndexSmallInt(v runtime.Value) (int64, bool) {
	if g, ok := v.(*runtime.GoValue); ok {
		return runtime.SmallIntOf(g.V)
	}
	return runtime.SmallIntOf(v)
}

// hostUint reads a host-boxed unsigned integer of any width as
// uint64. Host values arrive carrying their concrete type — uint,
// uint8..32, uintptr, uint64 — where script integers ride int64.
func HostUint(v any) (uint64, bool) {
	switch u := v.(type) {
	case uint64:
		return u, true
	case uint:
		return uint64(u), true
	case uint8, uint16, uint32, uintptr:
		return reflect.ValueOf(u).Uint(), true
	}
	return 0, false
}

// sliceBoundsReason renders Go's boundsError text for a failed slice
// operation — mirroring the check order and message shapes of the
// runtime's goPanicSlice* family. Every operand evaluates in its own
// domain: a negative signed index reports its bare form without the
// capacity/length suffix (Go's boundsNegErrorFmts), an unsigned one
// its uint64 magnitude. Returns "" when every index is in bounds.
func SliceBoundsReason(l, h, m BoundOperand, cap int64, three bool, word string) string {
	const p = "slice bounds out of range"
	if three {
		switch {
		case m.neg():
			return fmt.Sprintf("%s [::%v]", p, m.disp())
		case m.gtc(cap):
			return fmt.Sprintf("%s [::%v] with %s %d", p, m.disp(), word, cap)
		case h.neg():
			return fmt.Sprintf("%s [:%v:]", p, h.disp())
		case h.gt(m):
			return fmt.Sprintf("%s [:%v:%v]", p, h.disp(), m.disp())
		case l.neg():
			return fmt.Sprintf("%s [%v::]", p, l.disp())
		case l.gt(h):
			return fmt.Sprintf("%s [%v:%v:]", p, l.disp(), h.disp())
		}
		return ""
	}
	switch {
	case h.neg():
		return fmt.Sprintf("%s [:%v]", p, h.disp())
	case h.gtc(cap):
		return fmt.Sprintf("%s [:%v] with %s %d", p, h.disp(), word, cap)
	case l.neg():
		return fmt.Sprintf("%s [%v:]", p, l.disp())
	case l.gt(h):
		return fmt.Sprintf("%s [%v:%v]", p, l.disp(), h.disp())
	}
	return ""
}
