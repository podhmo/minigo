package main

import (
	"errors"
	"fmt"
	"os"
)

type B bool

// A method whose result is a named bool type does not satisfy the
// host's `Is(error) bool` / `As(any) bool` / `Timeout() bool`
// interfaces — gc ignores it, so scriptError must too.
type E struct{ ok B }
type T struct{}

func (e *E) Error() string { return "e" }
func (e *E) Is(t error) B  { return e.ok }
func (e *E) As(t any) B    { return e.ok }
func (e *E) Timeout() B    { return e.ok }
func (t *T) Error() string { return "t" }

func main() {
	err := error(&E{ok: true})
	fmt.Println(errors.Is(err, &T{}))
	var t *T
	fmt.Println(errors.As(err, &t))
	fmt.Println(os.IsTimeout(err))
}
