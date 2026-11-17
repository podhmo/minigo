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
}
