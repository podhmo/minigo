// Package main exercises boxed host named maps and slices (url.Values,
// http.Header, net.IP) that the embedder hands in as host values: they
// keep their box for method dispatch, and index writes, delete, clear,
// range and slicing reach the host value itself.
package main

import (
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Handler writes a header by index, edits the query and ranges over it.
func Handler(w http.ResponseWriter, r *http.Request) string {
	w.Header()["X-A"] = []string{"1"}
	w.Header()["X-B"] = append(w.Header()["X-B"], "2")
	q := r.URL.Query()
	q["z"] = []string{"9"}
	n := 0
	for range r.URL.Query() {
		n++
	}
	return fmt.Sprint(sortedPairs(q), " ", w.Header().Get("X-A"), w.Header().Get("X-B"), " ", n)
}

// Values deletes, assigns and ranges, then deletes while ranging: a key
// deleted before its turn is not produced.
func Values(q url.Values) string {
	q["z"] = []string{"9"}
	delete(q, "a")
	delete(q, "missing")
	before := sortedPairs(q)
	seen := 0
	for k := range q {
		seen++
		for k2 := range q {
			if k2 != k {
				delete(q, k2)
			}
		}
	}
	after := len(q)
	clear(q)
	return fmt.Sprint(before, " ", seen, after, len(q))
}

// IP slices, ranges and writes a net.IP; the slice keeps its host type.
func IP(ip net.IP) string {
	s := fmt.Sprintf("%v %d %T|", ip[:2], len(ip[1:3:4]), ip[1:])
	for i, b := range ip {
		s += fmt.Sprint(i, ":", b, " ")
	}
	ip[0] = 9
	return s + ip.String()
}

// SliceOOB slices a net.IP past its capacity.
func SliceOOB(ip net.IP) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	hi := 5
	_ = ip[1:hi]
	return ""
}

// SetOOB writes a net.IP past its end.
func SetOOB(ip net.IP) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	i := 4
	ip[i] = 1
	return ""
}

// Nested writes an element of a map's slice value: it shares the host
// backing, as in Go.
func Nested(q url.Values) string {
	q["tag"][0] = "X"
	q["tag"][1] += "!"
	return sortedPairs(q)
}

// NestedMissing writes into the nil slice of a missing key.
func NestedMissing(q url.Values) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	q["nope"][0] = "x"
	return ""
}

// ContainsFunc filters with slices.ContainsFunc, nil slice included.
func ContainsFunc(q url.Values) string {
	var none []string
	isGo := func(t string) bool { return t == "go" }
	return fmt.Sprint(slices.ContainsFunc(q["tag"], isGo), slices.ContainsFunc(none, isGo), slices.IndexFunc(none, isGo))
}

// Helpers reads a boxed host map and slice through maps/slices helpers
// and writes through a map of boxed host maps.
func Helpers(h http.Header, ip net.IP) string {
	keys := slices.Sorted(maps.Keys(h))
	vals := slices.Collect(maps.Values(h))
	m := map[string]http.Header{"k": h}
	m["k"]["C"] = []string{"3"}
	return fmt.Sprint(keys, len(vals), slices.Contains(ip, 3), slices.Index(ip, 4), slices.Max(ip), slices.Equal(ip, ip[:]), h.Get("C"))
}

func sortedPairs(q map[string][]string) string {
	var ps []string
	for k, v := range q {
		ps = append(ps, k+"="+strings.Join(v, ","))
	}
	sort.Strings(ps)
	return strings.Join(ps, "&")
}

// holder keeps a header in a script struct field.
type holder struct{ H http.Header }

// NestedTuple assigns elements of a header's slice values in tuple
// assignments: every operand pins the host backing, mixed with script
// slices and a host slice, and a direct tuple of map elements too.
func NestedTuple(h http.Header, ip net.IP) string {
	h["A"][0], h["A"][1] = h["A"][1], h["A"][0]
	s := fmt.Sprint(h["A"])
	h["A"][0], h["A"][1] = "p", "q"
	s += fmt.Sprint(h["A"])
	h["A"][0], h["B"][0] = "r", "s"
	s += fmt.Sprint(h["A"], h["B"])
	loc := []string{"x", "y"}
	loc[0], h["A"][1] = h["A"][1], loc[0]
	s += fmt.Sprint(loc, h["A"])
	h["A"][0], ip[0] = "v", 9
	ip[1], ip[2] = ip[2], ip[1]
	h["A"], h["B"] = h["B"], h["A"]
	return s + fmt.Sprint(h, ip)
}

// NestedTupleOrder pins the container operand before the stores land:
// the element write goes to the old slice, the map ends up holding the
// new one, whichever side comes first.
func NestedTupleOrder(h http.Header) string {
	h["A"][0], h["A"] = "v", []string{"n0", "n1"}
	s := fmt.Sprint(h["A"])
	h["B"], h["B"][0] = []string{"m0"}, "w"
	return s + fmt.Sprint(h["B"])
}

// NestedTupleMissing writes into the nil slice of a missing key in a
// tuple: the first store panics and the second never runs.
func NestedTupleMissing(h http.Header) (msg string) {
	defer func() { msg = fmt.Sprint(recover(), h["A"]) }()
	h["nope"][0], h["A"][0] = "x", "y"
	return ""
}

// NestedContainers writes nested elements reached through a pointer to
// a host struct, a script struct field, a slice and a map of headers,
// both singly and in tuples.
func NestedContainers(h http.Header) string {
	r := &http.Request{Header: h}
	r.Header["A"][0] = "r0"
	r.Header["A"][1], r.Header["B"][0] = "r1", "rb"
	s := fmt.Sprint(h)
	w := holder{h}
	w.H["A"][0] = "w0"
	w.H["A"][1], w.H["B"][0] = "w1", "wb"
	s += fmt.Sprint(h)
	hs := []http.Header{h}
	hs[0]["A"][0] = "s0"
	hs[0]["A"][1], hs[0]["B"][0] = "s1", "sb"
	s += fmt.Sprint(h)
	m := map[string]http.Header{"k": h}
	m["k"]["A"][0] = "m0"
	m["k"]["A"][1], m["k"]["B"][0] = "m1", "mb"
	return s + fmt.Sprint(h)
}

// NestedPtr writes through a pointer to the host map and slice: element
// stores, tuples, compound assignment, and the address of an element.
func NestedPtr(h http.Header, ip net.IP) string {
	p := &h
	(*p)["A"][0] = "v"
	(*p)["A"][1] += "!"
	(*p)["B"][0], (*p)["A"][0] = (*p)["A"][0], "t"
	(*p)["Z"] = []string{"z"}
	s := fmt.Sprint(h)
	q := &ip
	(*q)[1] = 9
	(*q)[2], (*q)[3] = (*q)[3], (*q)[2]
	s += fmt.Sprint(ip)
	e := &h["A"][0]
	*e = "e"
	*e += "+"
	f := &ip[0]
	*f += 5
	*f++
	g, k := &ip[1], &ip[2]
	*g, *k = *k, *g
	return s + fmt.Sprint(h["A"], ip, *e, *f)
}

// NestedAddrMissing takes the address of an element of a missing key's
// nil slice: the write through it panics like the index does.
func NestedAddrMissing(h http.Header) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	p := &h["nope"][0]
	*p = "x"
	return ""
}

func main() {}
