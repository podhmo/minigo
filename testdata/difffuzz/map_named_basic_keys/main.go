package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"
)

// Map keys of a named basic type keep their type: fmt prints them via
// String (ordered by the underlying value, not by String), range yields
// them typed with their methods, and maps.Clone/Copy, delete, generic
// helpers, reflect, json and a host template see the declared key type.
// Keys of distinct named types stay distinct keys in a map[any].

type K int

func (k K) String() string { return strings.Repeat("k", int(k)) }

type K2 int

type S string

func (s S) Up() string { return strings.ToUpper(string(s)) }

type F float64

type B bool

func (b B) String() string {
	if b {
		return "yes"
	}
	return "no"
}

type M time.Month

type P int

func (p P) Double() int { return int(p) * 2 }

// TK is string-kinded: json uses the string itself, not MarshalText.
type TK string

func (t TK) MarshalText() ([]byte, error) { return []byte("T-" + string(t)), nil }

type TI int

func (t TI) MarshalText() ([]byte, error) { return []byte(fmt.Sprint("I-", int(t))), nil }

func keysOf[Km comparable, V any](m map[Km]V) []Km {
	var out []Km
	for k := range m {
		out = append(out, k)
	}
	return out
}

func names[Km interface {
	~int
	String() string
}](m map[Km]int) []string {
	var out []string
	for k := range m {
		out = append(out, k.String())
	}
	sort.Strings(out)
	return out
}

func main() {
	fmt.Println(map[time.Month]int{time.March: 3, time.January: 1})
	fmt.Println(map[K]int{10: 10, 2: 2, 1: 1})
	fmt.Printf("%v|%+v|%#v|%#v\n", map[K]int{1: 1}, map[K]int{2: 2}, map[K2]int{1: 1}, map[S]int{"a": 1})
	fmt.Printf("%d %x %s\n", map[K]int{3: 3}, map[K2]int{255: 1}, map[S]string{"q": "r"})
	fmt.Println(map[S]int{"b": 1, "a": 2}, map[F]int{1.5: 1}, map[B]int{true: 1, false: 0})
	fmt.Println(map[M]int{3: 1, 1: 2})
	fmt.Println(map[time.Weekday]string{time.Monday: "gym", time.Friday: "pizza"})
	fmt.Println(map[fs.FileMode]bool{fs.ModeDir | 0o755: true, 0o644: false})
	fmt.Println(map[slog.Level]string{slog.LevelDebug: "d", slog.LevelError: "e"})
	fmt.Println(map[time.Duration]int{time.Second: 1, time.Millisecond: 2})
	fmt.Println(map[time.Month]map[K]int{time.May: {3: 3}}, map[K]time.Month{4: time.April})
	fmt.Println(fmt.Sprint(map[K]bool{1: true}), fmt.Sprintf("%v", map[K]K{1: 2}))

	// range: k and k, v forms keep the key's type and methods
	m := map[K]int{1: 1}
	for k := range m {
		fmt.Printf("%T %v %s\n", k, k, k.String())
	}
	for k, v := range m {
		fmt.Printf("%T %v %d\n", k, k, v)
	}
	for k := range map[P]int{3: 1} {
		fmt.Printf("%T %v %d\n", k, k, k.Double())
	}
	for k := range map[time.Month]int{time.June: 6} {
		fmt.Printf("%T %v %s\n", k, k, k.String())
	}
	for k := range map[M]int{M(time.March): 1} {
		fmt.Printf("%T %v\n", k, k)
	}
	var a any
	for k := range map[S]int{"hi": 1} {
		a = k
		fmt.Println(k.Up())
	}
	_, isS := a.(S)
	_, isStr := a.(string)
	fmt.Println(isS, isStr)
	type local int
	for k := range map[local]string{1: "a"} {
		fmt.Printf("%T\n", k)
	}

	// index stores, ++, delete, conversions
	w := map[K]int{}
	var x K = 4
	w[x] = 1
	w[K(2)]++
	w[3] += 3
	n := 5
	w[K(n)] = 5
	delete(w, 3)
	delete(w, K(n))
	fmt.Println(w, len(w), names(w))
	var ks []K
	for k := range w {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	fmt.Printf("%T %v %d\n", ks, ks, w[ks[0]])
	gk := keysOf(map[K]int{3: 3})
	fmt.Printf("%T %v\n", gk, gk)
	sk := slices.Sorted(maps.Keys(map[K]int{2: 1, 1: 2}))
	fmt.Printf("%T %v\n", sk, sk)

	// maps helpers
	c := maps.Clone(m)
	fmt.Println(c, maps.Equal(c, map[K]int{1: 1}))
	for k := range c {
		fmt.Printf("clone %T\n", k)
	}
	d := map[K]int{}
	maps.Copy(d, map[K]int{2: 2})
	for k := range d {
		fmt.Printf("copy %T %v\n", k, k)
	}
	delete(d, 2)
	fmt.Println(d, len(d))

	// keys from host results
	hm := map[time.Month]int{}
	hm[time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).Month()] = 2
	for k := range hm {
		fmt.Printf("%T %v\n", k, k)
	}
	fmt.Println(hm, hm[time.February])
	wm := map[time.Weekday]bool{}
	day := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		wm[day.AddDate(0, 0, i).Weekday()] = true
	}
	fmt.Println(wm, wm[time.Saturday])
	rk := map[reflect.Kind]int{reflect.Int: 1}
	rk[reflect.TypeOf("").Kind()] = 2
	fmt.Println(rk, rk[reflect.String])
	for k, v := range rk {
		if v == 2 {
			fmt.Printf("%T %v\n", k, k)
		}
	}
	tk := map[token.Token]int{token.ADD: 1}
	e, _ := parser.ParseExpr("a - b")
	tk[e.(*ast.BinaryExpr).Op] = 2
	fmt.Println(tk, tk[token.SUB])
	for k, v := range tk {
		if v == 2 {
			fmt.Printf("%T %v %v\n", k, k, k.IsOperator())
		}
	}

	// nested maps
	nested := map[K]map[time.Month][]S{1: {time.May: {"a"}}}
	nested[1][time.July] = append(nested[1][time.July], "b")
	fmt.Println(nested)
	for k, inner := range nested {
		for mk, v := range inner {
			if mk == time.July {
				fmt.Printf("%T %T %v %v\n", k, mk, mk, v)
			}
		}
	}

	// map[any]: keys of distinct types with one payload stay distinct
	ma := map[any]int{K(1): 1, 1: 2, P(1): 3}
	ma[K2(1)] = 4
	for k := range map[K]int{1: 0} {
		ma[k] += 10
	}
	fmt.Println(len(ma), ma[K(1)], ma[1], ma[P(1)], ma[K2(1)])
	for k, v := range ma {
		if v == 11 {
			fmt.Printf("%T\n", k)
		}
	}
	fmt.Println(map[any]int{K(1): 1})

	// json
	j, err := json.Marshal(map[K]int{1: 1})
	fmt.Println(string(j), err)
	j, err = json.Marshal(map[TK]int{"a": 1})
	fmt.Println(string(j), err)
	j, err = json.Marshal(map[TI]int{3: 1})
	fmt.Println(string(j), err)
	j, err = json.Marshal(map[time.Month]int{3: 1})
	fmt.Println(string(j), err)
	um := map[K]int{5: 5}
	err = json.Unmarshal([]byte(`{"1":1,"2":2}`), &um)
	fmt.Println(um, err)
	for k, v := range um {
		if v == 5 {
			fmt.Printf("%T %v\n", k, k)
		}
	}

	// reflect
	rv := reflect.ValueOf(map[K]int{2: 1})
	for _, k := range rv.MapKeys() {
		fmt.Println(k.Type(), k.Interface())
	}
	it := rv.MapRange()
	for it.Next() {
		fmt.Println(it.Key().Type(), it.Key().Interface(), it.Value())
	}
	rw := reflect.ValueOf(map[K]int{})
	rw.SetMapIndex(reflect.ValueOf(K(3)), reflect.ValueOf(9))
	fmt.Println(rw.Interface(), rw.MapIndex(reflect.ValueOf(K(3))))

	// a host template ranging over a map of host named keys
	tmpl := template.Must(template.New("x").Parse("{{range $k, $v := .}}{{$k}}={{$v}};{{end}}\n"))
	tmpl.Execute(os.Stdout, map[time.Weekday]int{time.Monday: 1, time.Tuesday: 2})
	tmpl.Execute(os.Stdout, map[time.Month]int{time.May: 1})
}
