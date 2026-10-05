package main

import (
	"fmt"
)

type Point struct{ X, Y int }

func (p Point) String() string    { return fmt.Sprintf("(%d,%d)", p.X, p.Y) }
func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }
func (p *Point) Scale(k int)      { p.X *= k; p.Y *= k }

type Labeled struct {
	Point
	Label string
}

// Counter's String has a pointer receiver: fmt uses it for *Counter only.
type Counter struct{ N int }

func (c *Counter) String() string { return fmt.Sprint("#", c.N) }

type Shape interface {
	Area() int
	Kind() string
}

type Rect struct{ W, H int }

func (r Rect) Area() int      { return r.W * r.H }
func (r Rect) Kind() string   { return "rect" }
func (r Rect) String() string { return fmt.Sprintf("%dx%d", r.W, r.H) }

type Tri struct{ B, H int }

func (t *Tri) Area() int    { return t.B * t.H / 2 }
func (t *Tri) Kind() string { return "tri" }

type MyErr struct{ Code int }

func (e *MyErr) Error() string { return fmt.Sprint("code ", e.Code) }

type Celsius float64

type Number interface{ ~int | ~float64 }

type Ordered interface{ ~int | ~float64 | ~string }

func Sum[T Number](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}

func MaxOf[T Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func Filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

func Reduce[T, A any](xs []T, acc A, f func(A, T) A) A {
	for _, x := range xs {
		acc = f(acc, x)
	}
	return acc
}

func SortedKeys[K Ordered, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func Describe[T any](v T) string { return fmt.Sprintf("%T:%v", v, v) }

func KindOf[T any](v T) string {
	switch any(v).(type) {
	case nil:
		return "nil"
	case int:
		return "int"
	case string:
		return "string"
	case fmt.Stringer:
		return "stringer"
	case error:
		return "error"
	}
	return "other"
}

func Zero[T any]() T { var z T; return z }

func Ptr[T any](v T) *T { return &v }

func JoinS[T fmt.Stringer](xs []T) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "+"
		}
		out += x.String()
	}
	return out
}

type Stack[T any] struct{ items []T }

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }
func (s *Stack[T]) Len() int { return len(s.items) }
func (s *Stack[T]) Pop() (T, bool) {
	var z T
	if len(s.items) == 0 {
		return z, false
	}
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func (p Pair[K, V]) String() string { return fmt.Sprintf("%v=%v", p.Key, p.Val) }

var (
	v_pt_0  Point          = Point{}
	v_pt_1  Point          = Point{1, 2}
	v_pt_2  Point          = Point{-3, 4}
	v_pp_0  *Point         = nil
	v_pp_1  *Point         = &Point{5, 6}
	v_lab_0 Labeled        = Labeled{}
	v_lab_1 Labeled        = Labeled{Point{1, 2}, "a"}
	v_sh_0  Shape          = nil
	v_sh_1  Shape          = Rect{2, 3}
	v_sh_2  Shape          = &Tri{3, 4}
	v_sh_3  Shape          = (*Tri)(nil)
	v_err_0 error          = nil
	v_err_1 error          = &MyErr{404}
	v_err_2 error          = (*MyErr)(nil)
	v_any_0 any            = nil
	v_any_1 any            = 1
	v_any_2 any            = "s"
	v_any_3 any            = Point{1, 2}
	v_any_4 any            = &Point{3, 4}
	v_any_5 any            = []int{1}
	v_any_6 any            = 3.5
	v_any_7 any            = Rect{1, 1}
	v_any_8 any            = Celsius(2)
	v_is_0  []int          = nil
	v_is_1  []int          = []int{}
	v_is_2  []int          = []int{3, 1, 2}
	v_is_3  []int          = []int{5, -1, 5, 0}
	v_n_0   int            = 0
	v_n_1   int            = 1
	v_n_2   int            = 2
	v_n_3   int            = 3
	v_n_4   int            = -1
	v_n_5   int            = 5
	v_s_0   string         = ""
	v_s_1   string         = "a"
	v_s_2   string         = "héllo"
	v_s_3   string         = "x,y"
	v_b_0   bool           = true
	v_b_1   bool           = false
	v_ss_0  []string       = nil
	v_ss_1  []string       = []string{}
	v_ss_2  []string       = []string{"b", "a", "c"}
	v_ss_3  []string       = []string{"x", "", "y", "x"}
	v_ss_4  []string       = []string{"Hello", "héllo", "HELLO"}
	v_m_0   map[string]int = nil
	v_m_1   map[string]int = map[string]int{}
	v_m_2   map[string]int = map[string]int{"a": 1, "b": 2, "c": 3}
	v_m_3   map[string]int = map[string]int{"x": -1, "": 0}
	v_f_0   float64        = 0.0
	v_f_1   float64        = 1.5
	v_f_2   float64        = -2.25
	v_f_3   float64        = 100.0
	v_f_4   float64        = 0.1
	v_f_5   float64        = 1e6
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

// embT's only method is on the pointer receiver: it joins a struct's
// method set through an embedded *T (or under a pointer parent) but not
// through a value embed.
type embT struct{}

func (*embT) M() {}

// embA reaches embT by value: its own set carries nothing of embT's.
type embA struct{ embT }

// embC sees embT twice — through embA (value path) and directly as *T.
// The value-path visit must not hide the *T embed's pointer receivers.
type embC struct {
	embA
	*embT
}

// embD is the plain counter-case: only the pointer embed exists.
type embD struct{ *embT }

// embE is the value-only counter-case: no path through a pointer, so
// embT's pointer receivers stay out of its set.
type embE struct{ embA }

func main() {
	// a sibling embed path visiting the same type as a value must not
	// consume the visit — the direct *T embed still contributes M.
	try(0, func() any {
		var v any = embC{embA{}, &embT{}}
		_, ok := v.(interface{ M() })
		return ok
	})
	try(1, func() any {
		var v any = embD{&embT{}}
		_, ok := v.(interface{ M() })
		return ok
	})
	// the receiver rule still holds the other way: a value-only path
	// never imports pointer receivers.
	try(2, func() any {
		var v any = embE{embA{}}
		_, ok := v.(interface{ M() })
		return ok
	})
	// and the promoted call itself dispatches on the same value.
	try(3, func() any {
		c := embC{embA{}, &embT{}}
		c.M()
		return "called"
	})
}
