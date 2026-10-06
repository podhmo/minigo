// `*s = T{...}` inside a pointer method must land in the caller's variable
// when the receiver address is taken implicitly (v.M(), x.f.M()) — found
// via go/scanner.Scanner.Init (`*s = Scanner{...}`) while running go/parser
// on grafana sources (realworld grafana-openapi).
package main

import "fmt"

type S struct {
	a, b int
	tags []string
}

func (s *S) Init(a int) { *s = S{a: a}; s.b = 2 }
func (s *S) Set(a int)  { s.a = a }
func (s *S) Reset()     { *s = S{} }

type P struct{ s S }

type Scanner struct {
	src    []byte
	offset int
	ch     rune
}

func (s *Scanner) Init(src []byte) {
	*s = Scanner{src: src, ch: ' '}
	s.next()
}

func (s *Scanner) next() {
	if s.offset < len(s.src) {
		s.ch = rune(s.src[s.offset])
		s.offset++
	}
}

func main() {
	var v S
	v.Set(7)
	fmt.Println("set:", v)
	v.Init(1)
	fmt.Println("var:", v)
	(&v).Init(3)
	fmt.Println("explicit &:", v)

	pp := &P{}
	pp.s.Init(4)
	fmt.Println("field via ptr:", pp.s)

	var p P
	p.s.Init(5)
	fmt.Println("field via value:", p.s)

	// the overwrite must not leak into copies taken before it.
	w := S{a: 9, tags: []string{"x"}}
	before := w
	w.Reset()
	fmt.Println("reset:", w, "copy kept:", before)

	arr := [2]S{{a: 1}, {a: 2}}
	arr[1].Init(6)
	fmt.Println("array elem:", arr)

	var sc Scanner
	sc.Init([]byte("go"))
	fmt.Printf("scanner: ch=%q offset=%d\n", sc.ch, sc.offset)
}
