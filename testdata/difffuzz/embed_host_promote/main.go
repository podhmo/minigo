package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// A script struct embedding a host value (bytes.Buffer) promotes its
// pointer-receiver methods through the addressable parent, so &t is an
// io.Writer for the host io helpers.

type T struct {
	bytes.Buffer
}

// A script struct embedding a host pointer (*os.File) satisfies an
// interface through the promoted methods.

type F struct {
	*os.File
}

type namer interface {
	Name() string
}

// A declared field at depth 0 shadows a promoted method of the same
// name: &shadowed is not an io.Writer in gc.

type shadowed struct {
	*bytes.Buffer
	Write int
}

// The same method promoted through two same-depth embeds is ambiguous:
// &ambiguous is not an io.Writer either.

type ambiguous struct {
	*os.File
	*bytes.Buffer
}

func main() {
	var t T
	n, err := io.Copy(&t, strings.NewReader("hello"))
	fmt.Println(n, err, t.String())

	f := F{os.Stdout}
	var c namer = f
	fmt.Println(c.Name())

	var w io.Writer = &t
	fmt.Fprint(w, "!")
	fmt.Println(t.String())

	_, ok := any(&shadowed{}).(io.Writer)
	fmt.Println(ok)
	_, ok2 := any(&ambiguous{}).(io.Writer)
	fmt.Println(ok2)
}
