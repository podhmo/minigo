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

	var nilErr error
	w5 := fmt.Errorf("%w %w", nilErr, b)
	fmt.Println(len(w5.(interface{ Unwrap() []error }).Unwrap()), errors.Is(w5, b))
}
