package main

import (
	"fmt"
	"io"
	"os"
	"text/template"
)

// Script containers crossing a host `any` parameter at their Go type:
// a container holding itself folds back to the same host value, an
// `any` map key may be a struct or a pointer, and the copy-back after
// the call keeps each element's declared type.

type S struct{ A int }
type U8 uint8
type MyInt int8

func (m MyInt) String() string { return fmt.Sprintf("my%d", int8(m)) }

type F float32

func main() {
	m := map[string]any{"a": 1}
	m["self"] = m
	template.Must(template.New("t").Parse("{{.a}} {{.self.a}} {{.self.self.a}}\n")).Execute(os.Stdout, m)
	xs := []any{1, 2}
	xs[1] = xs
	template.Must(template.New("t").Parse("{{index . 0}} {{index (index . 1) 0}}\n")).Execute(os.Stdout, xs)
	nested := map[string][]any{"k": {1}}
	nested["k"] = append(nested["k"], nested)
	template.Must(template.New("t").Parse("{{index .k 0}} {{index (index (index .k 1) \"k\") 0}}\n")).Execute(os.Stdout, nested)

	t := template.Must(template.New("t").Parse("{{range $k, $v := .}}{{$v}}{{end}}\n"))
	t.Execute(os.Stdout, map[any]int{S{1}: 1})
	t.Execute(os.Stdout, map[any]int{&S{1}: 1})
	t.Execute(os.Stdout, map[any]string{1: "a", "k": "b"})

	d := []U8{255, 1}
	t.Execute(io.Discard, d)
	d[0]++
	fmt.Printf("%T %v\n", d[0], d)
	b := []MyInt{1, 2}
	t.Execute(io.Discard, b)
	fmt.Printf("%T %v\n", b[0], b)
	f := []F{1.5}
	t.Execute(io.Discard, f)
	fmt.Printf("%T %v\n", f[0], f)
	p := []int8{1}
	t.Execute(io.Discard, p)
	fmt.Printf("%T %v\n", p[0], p)
	bs := []byte("x")
	t.Execute(io.Discard, bs)
	fmt.Printf("%T %v\n", bs[0], bs)
	var e []U8
	e = append(e, 3)
	t.Execute(io.Discard, e)
	e[0]++
	fmt.Printf("%T %v\n", e[0], e)
	arr := [2]MyInt{3, 4}
	t.Execute(io.Discard, arr)
	fmt.Printf("%T %v\n", arr[0], arr)
}
