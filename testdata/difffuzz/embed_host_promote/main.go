package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

type namer interface {
	Name() string
}

// a script struct embedding a host struct promotes its methods: both
// value and pointer embeds satisfy io.Writer through Write.
type F struct {
	bytes.Buffer
}

type G struct {
	*os.File
}

// a declared field shadows the promoted method of the same name.
type shadowed struct {
	*bytes.Buffer
	Write int
}

// the same member through two same-depth embeds is ambiguous and
// absent from the method set.
type ambiguous struct {
	*os.File
	*bytes.Buffer
}

// the shallower promotion wins across depths: A's Write via E is
// depth 2, B's Write depth 1.
type sw struct{}

func (sw) Write(p []byte) (int, error) { return len(p), nil }

type viaA struct{ sw }
type viaB struct{ sw }
type deepAmbiguous struct {
	viaA
	viaB
}
type shallowWins struct {
	viaA
	sw
}

// an interface embed contributes at depth 1: it contests a same-depth
// struct promotion but beats a deeper one.
type ifaceShallow struct {
	io.Writer
	sw
}
type ifaceDeep struct {
	io.Writer
	viaA
}

// two host embeds with identical signatures still contest at depth 1.
type hostAmbiguous struct {
	bytes.Buffer
	strings.Builder
}

func main() {
	var w io.Writer = &F{}
	n, err := fmt.Fprint(w, "abc")
	fmt.Println(n, err, w.(*F).String())

	var c namer = G{os.Stdout}
	fmt.Println(c.Name())

	var h io.Writer = G{os.Stdout}
	fmt.Println(h.Write([]byte("x")))

	_, ok := any(&shadowed{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&ambiguous{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&deepAmbiguous{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&shallowWins{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&ifaceShallow{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&ifaceDeep{}).(io.Writer)
	fmt.Println(ok)
	_, ok = any(&hostAmbiguous{}).(io.Writer)
	fmt.Println(ok)
}
