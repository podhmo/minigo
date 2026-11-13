package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
)

// A named map and its unnamed spelling are two views of one hash table:
// `type M map[K]V` assigned, passed, sent or stored where map[K]V is
// expected (and back) copies a pointer, so inserts, deletes and clear made
// through either view are visible through the other. minigo tags the
// declared type on the map header; re-tagging must share the contents
// instead of forking them (a delete through one view left a ghost entry
// `a:<nil>` in the other).

type M map[string][]string
type N map[string]int
type Any map[any]int

type holder struct {
	plain map[string][]string
	named M
}

func set(h map[string][]string) { h["z"] = []string{"1"} }
func del(h map[string][]string) { delete(h, "a") }
func setM(h M)                  { h["y"] = []string{"9"} }
func clearAll(h map[string]int) { clear(h) }
func plainOf(m M) map[string][]string {
	return m
}
func namedOf(m map[string][]string) M {
	return m
}

func show(label string, m map[string][]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println(label, keys, len(m), m)
}

func main() {
	// pass named as an unnamed parameter: insert
	m := M{"a": {"1"}, "b": {"2"}}
	set(m)
	fmt.Println(m, len(m))

	// delete leaves no ghost entry in the caller's view
	m2 := M{"a": {"1"}, "b": {"2"}}
	del(m2)
	fmt.Println(m2, len(m2))
	for k, v := range m2 {
		fmt.Println("range", k, v)
	}

	// assign to a var of the unnamed type, mutate through both views
	m3 := M{"a": {"1"}}
	var u map[string][]string = m3
	u["q"] = []string{"x"}
	delete(m3, "a")
	m3["r"] = nil
	show("named", m3)
	show("plain", u)

	// the other direction: unnamed to named, by assignment and by call
	un := map[string][]string{"a": {"1"}}
	var nm M = un
	nm["b"] = nil
	setM(un)
	delete(nm, "a")
	show("named", map[string][]string(nm))
	show("plain", un)

	// explicit conversions both ways and results
	c1 := M{"a": {"1"}}
	c2 := map[string][]string(c1)
	c2["c"] = nil
	c3 := M(c2)
	c3["d"] = nil
	r1 := M{"a": nil}
	r2 := plainOf(r1)
	r2["r"] = nil
	r3 := namedOf(r2)
	delete(r3, "a")
	fmt.Println(c1, c2, c3)
	fmt.Println(r1, r2, r3)

	// clear through the other view
	cl := N{"a": 1, "b": 2}
	clearAll(cl)
	fmt.Println(cl, len(cl))
	cl["k"] = 3
	fmt.Println(cl, len(cl))

	// send on a chan of the unnamed type
	ch := make(chan map[string][]string, 1)
	cm := M{"a": nil}
	ch <- cm
	got := <-ch
	got["ch"] = nil
	delete(got, "a")
	fmt.Println(cm, got)

	// store into a struct field, slice element and map element, then
	// mutate through the stored view
	sm := M{"a": nil}
	h := holder{plain: sm}
	h.plain["h"] = nil
	sl := []map[string][]string{sm}
	sl[0]["sl"] = nil
	mm := map[string]map[string][]string{"k": sm}
	mm["k"]["mm"] = nil
	um := map[string][]string{"a": nil}
	h.named = um
	h.named["named"] = nil
	delete(sm, "a")
	show("sm", sm)
	show("h.plain", h.plain)
	show("sl[0]", sl[0])
	show("mm[k]", mm["k"])
	show("um", um)
	show("h.named", h.named)

	// identity through a pointer to the named map
	pm := &m
	(*pm)["p"] = nil
	delete(*pm, "b")
	var pu map[string][]string = *pm
	pu["pu"] = nil
	fmt.Println(len(m), len(*pm), len(pu))

	// clearing through the other view mid-walk ends the walk: entries
	// removed before their turn are not produced, so exactly one
	// iteration runs whichever key came first
	rm := M{"a": nil, "b": nil, "c": nil}
	rv := plainOf(rm)
	iters := 0
	for range rm {
		iters++
		clear(rv)
	}
	fmt.Println(iters, len(rm), len(rv))
	rv["again"] = nil
	for k := range rm {
		fmt.Println("range", k)
	}

	// range after deleting through the other view
	dm := M{"a": nil, "b": nil, "c": nil}
	dv := plainOf(dm)
	delete(dv, "b")
	var seen []string
	for k := range dm {
		seen = append(seen, k)
	}
	slices.Sort(seen)
	fmt.Println(seen, len(dm), len(dv))

	// an any-keyed map keeps hashing by dynamic type through both views
	ak := Any{1: 1, "1": 2}
	var au map[any]int = ak
	au[int8(1)] = 3
	delete(au, "1")
	fmt.Println(len(ak), ak[1], ak[int8(1)], len(au))

	// maps.Clone stays independent
	orig := M{"a": nil}
	cp := maps.Clone(orig)
	cp["b"] = nil
	fmt.Println(len(orig), len(cp))

	// interface boxing round-trip
	var any1 any = sm
	any1.(M)["any"] = nil
	show("sm", sm)

	// host named map types (url.Values, http.Header) over plain maps
	raw := map[string][]string{"a": {"1"}}
	var v url.Values = raw
	v.Add("b", "2")
	v.Del("a")
	fmt.Println(raw, len(raw), v, len(v))
	hd := http.Header{}
	var hp map[string][]string = hd
	hp["X"] = []string{"1"}
	hd.Del("Other")
	fmt.Println(hd, len(hd), hp, len(hp))
}
