package main

// fmt.Errorf with several %w verbs wraps every operand (Unwrap() []error,
// no single Unwrap() error), not just the last one.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type myErr struct{ n int }

func (m *myErr) Error() string { return fmt.Sprint("my ", m.n) }

var me = &myErr{7}

func main() {
	a, b := errors.New("a"), errors.New("b")
	w := fmt.Errorf("%w %w", a, b)
	fmt.Println(w, errors.Is(w, a), errors.Is(w, b), errors.Unwrap(w) == nil)
	kids := w.(interface{ Unwrap() []error }).Unwrap()
	fmt.Println(len(kids), kids[0] == a, kids[1] == b)

	w2 := fmt.Errorf("a %w b %w", me, os.ErrExist)
	ue := w2.(interface{ Unwrap() []error }).Unwrap()
	var t *myErr
	fmt.Println(ue[0] == me, len(ue), errors.Is(w2, me), errors.Is(w2, fs.ErrExist), os.IsExist(w2), errors.As(w2, &t), t == me)

	w3 := fmt.Errorf("%w|%w|%w", a, a, b)
	fmt.Println(len(w3.(interface{ Unwrap() []error }).Unwrap()))

	w4 := fmt.Errorf("one %w", a)
	fmt.Println(errors.Unwrap(w4) == a)

	// shape follows the raw %w count; explicit indexes move the cursor
	w6 := fmt.Errorf("%[1]w %[1]w", a)
	fmt.Println(errors.Unwrap(w6) == nil, len(w6.(interface{ Unwrap() []error }).Unwrap()))
	w7 := fmt.Errorf("%[2]w %[1]v %w", a, b)
	fmt.Println(errors.Unwrap(w7) == nil, errors.Is(w7, a), errors.Is(w7, b), w7)
	w8 := fmt.Errorf("%*d %w", 3, 4, a)
	fmt.Println(errors.Unwrap(w8) == a, w8)
	// non-error operands wrap nothing
	w9 := fmt.Errorf("%w + %w", 3, b)
	fmt.Println(len(w9.(interface{ Unwrap() []error }).Unwrap()))
	fmt.Println(errors.Unwrap(fmt.Errorf("x %w", "s")) == nil)
	var tn *myErr
	w10 := fmt.Errorf("x %w %w", error(tn), &myErr{1})
	fmt.Println(len(w10.(interface{ Unwrap() []error }).Unwrap()))
	fmt.Println(errors.Unwrap(fmt.Errorf("x %w", error(tn))) == nil)
	// flags and width on %w
	fmt.Printf("[%v] [%v]\n", fmt.Errorf("%6w", a), fmt.Errorf("%-6w|", a))
	fmt.Println(errors.Unwrap(fmt.Errorf("%+w", a)) == a)

	var nilErr error
	w5 := fmt.Errorf("%w %w", nilErr, b)
	fmt.Println(len(w5.(interface{ Unwrap() []error }).Unwrap()), errors.Is(w5, b))
}
