package runtime

import (
	"errors"
	"fmt"
	"testing"
)

func TestRuntimePanicPayload(t *testing.T) {
	p := NilDerefPanic()
	re, ok := p.Value.(*RuntimeError)
	if !ok {
		t.Fatalf("NilDerefPanic payload = %T, want *RuntimeError", p.Value)
	}
	// Go's errorString Error() carries the "runtime error:" prefix.
	if got := re.Error(); got != "runtime error: invalid memory address or nil pointer dereference" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestPlainPanicPayload(t *testing.T) {
	p := NilMapAssignPanic()
	pe, ok := p.Value.(PlainError)
	if !ok {
		t.Fatalf("NilMapAssignPanic payload = %T, want PlainError", p.Value)
	}
	// Go's plainError has no "runtime error:" prefix but IS an error —
	// recover().(error) must work on it.
	if !errors.Is(pe, pe) {
		t.Fatal("PlainError must be an error")
	}
	var _ error = pe
	if got := pe.Error(); got != "assignment to entry in nil map" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestPanicConstructors(t *testing.T) {
	for _, c := range []struct {
		p    *Panic
		want string // Error() text of the payload
	}{
		{BoundsPanic(5, 0), "runtime error: index out of range [5] with length 0"},
		{ComparingUncomparablePanic("[]int"), "runtime error: comparing uncomparable type []int"},
		{NegativeShiftPanic(), "runtime error: negative shift amount"},
		{SliceToArrayPanic(2, 3), "runtime error: cannot convert slice with length 2 to array or pointer to array with length 3"},
		{CloseNilChanPanic(), "close of nil channel"},
		{MakeslicePanic("len"), "runtime error: makeslice: len out of range"},
	} {
		e, ok := c.p.Value.(error)
		if !ok {
			t.Fatalf("payload %T is not an error", c.p.Value)
		}
		if got := fmt.Sprint(e); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

func TestTagTagOfUnwrap(t *testing.T) {
	td := &TypeDef{Name: "MyInt", Kind: KindNamedBasic}
	u := &TypeDef{Name: "OtherInt", Kind: KindNamedBasic}

	// Tag is the single construction point — it peels an existing
	// Named tag so values are never double-tagged.
	n := Tag(td, Tag(u, int64(3)))
	if n.Typ != td || n.V != int64(3) {
		t.Fatalf("nested Tag = %+v", n)
	}
	if _, nested := n.V.(*Named); nested {
		t.Fatal("Tag must not nest Named inside Named")
	}

	if TagOf(n) != td {
		t.Fatal("TagOf should return the outermost tag")
	}
	if TagOf(int64(3)) != nil {
		t.Fatal("TagOf of untagged value should be nil")
	}

	// Unwrap peels however many layers a stale value still carries.
	if got := Unwrap(&Named{Typ: td, V: &Named{Typ: u, V: int64(3)}}); got != int64(3) {
		t.Fatalf("Unwrap nested = %v", got)
	}
	if got := Unwrap(int64(3)); got != int64(3) {
		t.Fatalf("Unwrap passthrough = %v", got)
	}
}
