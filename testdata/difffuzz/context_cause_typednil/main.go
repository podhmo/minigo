package main

import (
	"context"
	"errors"
	"fmt"
)

type E struct{ msg string }

func (e *E) Error() string { return e.msg }

// E2's Error ignores its receiver — a typed nil still returns its
// message through the host error box.
type E2 struct{}

func (e *E2) Error() string { return "E2!" }

func main() {
	ctx, cancel := context.WithCancelCause(context.Background())
	var e *E
	cancel(error(e))
	c := context.Cause(ctx)
	fmt.Println(c != nil)
	fmt.Println(errors.Is(c, context.Canceled))
	var e2 *E2
	fmt.Println(errors.Join(error(e2)).Error())
}
