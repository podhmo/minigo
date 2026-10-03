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
