package main

// A script error that crosses a bound host helper and comes back keeps
// its identity: errors.Unwrap, a host Unwrap method, context causes,
// json's MarshalJSON error chain, a script Read's error through io, and
// template.Must's panic value all hand back the same pointer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"text/template"
	"time"
)

type myErr struct{ n int }

func (m *myErr) Error() string { return fmt.Sprint("my ", m.n) }

var me = &myErr{7}

type M struct{}

func (M) MarshalJSON() ([]byte, error) { return nil, me }

type R struct{ calls int }

func (r *R) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == 1 {
		p[0] = 'x'
		return 1, nil
	}
	return 0, me
}

func main() {
	w := fmt.Errorf("w: %w", me)
	fmt.Println(errors.Unwrap(w) == me, errors.Unwrap(w))
	fmt.Println(w.(interface{ Unwrap() error }).Unwrap() == me)
	fmt.Println(errors.Unwrap(&fs.PathError{Op: "x", Path: "p", Err: me}) == me)
	j := errors.Join(me, fs.ErrNotExist)
	kids := j.(interface{ Unwrap() []error }).Unwrap()
	fmt.Println(kids[0] == me, kids[1] == fs.ErrNotExist, len(kids))

	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Millisecond, me)
	defer cancel()
	<-ctx.Done()
	c := context.Cause(ctx)
	fmt.Println(c, c == me, errors.Is(c, me), ctx.Err())
	ctx2, cancel2 := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), me)
	defer cancel2()
	c2 := context.Cause(ctx2)
	fmt.Println(c2, c2 == me)

	_, err := json.Marshal(M{})
	var t *myErr
	fmt.Println(err)
	fmt.Println(errors.Is(err, me), errors.As(err, &t), t == me)

	b, err := io.ReadAll(&R{})
	fmt.Println(string(b), err, err == me)

	func() {
		defer func() {
			r := recover()
			fmt.Println("must:", r == me, r)
		}()
		template.Must(template.New("x"), me)
	}()
}
