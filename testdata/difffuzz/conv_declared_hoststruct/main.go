package main

import (
	"bytes"
	"fmt"
	"net"
)

// A declared type over a bound host struct converts both ways: the
// value form snapshots the struct and the pointer form re-tags the
// same box.

type MyBuf bytes.Buffer

func main() {
	var b bytes.Buffer
	b.WriteString("hi")
	mb := MyBuf(b)
	b.WriteString("!")
	fmt.Println(b.String())
	fmt.Printf("%T\n", mb)
	bb := bytes.Buffer(mb)
	fmt.Println(bb.Len())
	// assignment copies the struct: mutating the copy leaves mb.
	other := mb
	(*bytes.Buffer)(&other).Reset()
	fmt.Println((*bytes.Buffer)(&mb).Len())
	pmb := (*MyBuf)(bytes.NewBufferString("s"))
	fmt.Printf("%T %v\n", pmb, pmb != nil)
	// a composite literal is a value, so its conversion snapshots too.
	ml := MyBuf(bytes.Buffer{})
	fmt.Println((*bytes.Buffer)(&ml).Len())
	// a declared pointer-underlying tag shares its box like a pointer.
	p := P(bytes.NewBufferString("xy"))
	q := Q(p)
	(*bytes.Buffer)(q).WriteString("!")
	fmt.Println((*bytes.Buffer)(p).String())
	// a declared func's result converts like the literal: its static
	// type is the callee's signature, not the *T box shape.
	mc := MyBuf(makeBuffer())
	fmt.Println((*bytes.Buffer)(&mc).Len())
	// a generic callee's `*T` result must not poison the outer call's
	// arg typing.
	fmt.Println(*g(pointer(1)))
	// a value-shaped box (HostNew spells T) takes field writes through
	// the copy like a plain var — converting does not freeze it.
	var src net.Interface
	src.Index = 3
	dst := NIC(src)
	dst.Index = 1
	fmt.Println(dst.Index, src.Index)
	shadowP()
}

type P *bytes.Buffer
type Q P

type NIC net.Interface

func makeBuffer() bytes.Buffer {
	var b bytes.Buffer
	b.WriteString("zz")
	return b
}

func pointer[T any](x T) *T { return &x }
func g[T any](x *T) *T      { return x }

// a function-local type name shadows the package name an index-chased
// ident resolves to: Q -> B -> package P (*bytes.Buffer) shares.
type B P

func shadowP() {
	type P bytes.Buffer
	type Q B
	bp := B(bytes.NewBufferString("hi"))
	q := Q(bp)
	other := q
	(*bytes.Buffer)(other).WriteString("!")
	fmt.Println((*bytes.Buffer)(q).String())
}
