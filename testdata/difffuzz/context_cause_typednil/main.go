package main

import (
	"context"
	"errors"
	"fmt"
)

type E struct{ msg string }

func (e *E) Error() string { return e.msg }

func main() {
	ctx, cancel := context.WithCancelCause(context.Background())
	var e *E
	cancel(error(e))
	c := context.Cause(ctx)
	fmt.Println(c != nil)
	fmt.Println(errors.Is(c, context.Canceled))
}
