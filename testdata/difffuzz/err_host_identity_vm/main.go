package main

// A script error returned from a script method the host calls through
// an interface proxy (Write, Read, a split func) or passed to a host
// `error` parameter (a CancelCauseFunc) keeps its identity when the host
// hands it back — and a boxed typed-nil error stays a non-nil error.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type myErr struct{ n int }

func (m *myErr) Error() string { return fmt.Sprint("my ", m.n) }

var me = &myErr{7}

type W struct{}

func (W) Write(p []byte) (int, error) { return 0, me }

type NW struct{}

func (NW) Write(p []byte) (int, error) {
	var e *myErr
	return 0, e
}

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
	_, err := fmt.Fprintf(W{}, "x")
	fmt.Println(err, err == me)
	_, err = io.Copy(W{}, strings.NewReader("abc"))
	fmt.Println(err, err == me)
	_, err = io.WriteString(W{}, "abc")
	fmt.Println(err, err == me)
	bw := bufio.NewWriter(W{})
	bw.WriteString("x")
	err = bw.Flush()
	fmt.Println(err, err == me)

	sc := bufio.NewScanner(strings.NewReader("a b"))
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) { return 0, nil, me })
	for sc.Scan() {
	}
	var t *myErr
	fmt.Println(sc.Err(), sc.Err() == me, errors.As(sc.Err(), &t), t == me)

	var b bytes.Buffer
	_, err = b.ReadFrom(&R{})
	fmt.Println(err, err == me, b.String())

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(me)
	c := context.Cause(ctx)
	fmt.Println(c, c == me, errors.Is(c, me), ctx.Err())

	_, err = fmt.Fprintf(NW{}, "x")
	_, isMine := err.(*myErr)
	fmt.Println(err != nil, isMine)
}
