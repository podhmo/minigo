// A host struct value reaches the io helpers as the host value itself,
// but a pointer converted to a script-defined type keeps that type's
// own Write/Read: (*MyB)(&sb) must not bypass MyB.Write.
package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

type MyB strings.Builder

func (m *MyB) Write(p []byte) (int, error) {
	fmt.Println("MyB.Write", string(p))
	return (*strings.Builder)(m).Write(p)
}

type MyBuf bytes.Buffer

func (m *MyBuf) Read(p []byte) (int, error) {
	n, err := (*bytes.Buffer)(m).Read(p)
	fmt.Println("MyBuf.Read", n, err)
	return n, err
}

func main() {
	var sb strings.Builder
	m := (*MyB)(&sb)
	io.Copy(m, strings.NewReader("conv"))
	fmt.Fprint(m, "fp")
	var w io.Writer = m
	io.WriteString(w, "ws")
	fmt.Println(sb.String())

	var b bytes.Buffer
	b.WriteString("data")
	got, err := io.ReadAll((*MyBuf)(&b))
	fmt.Println(string(got), err)

	var plain strings.Builder
	io.Copy(&plain, strings.NewReader("host"))
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s-%d", plain.String(), plain.Len())
	all, _ := io.ReadAll(&buf)
	fmt.Println(string(all))
}
