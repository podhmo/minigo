// Package main exercises boxed host named maps and slices (url.Values,
// http.Header, net.IP) that the embedder hands in as host values: they
// keep their box for method dispatch, and index writes, delete, clear,
// range and slicing reach the host value itself.
package main

import (
	"fmt"
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

func sortedPairs(q map[string][]string) string {
	var ps []string
	for k, v := range q {
		ps = append(ps, k+"="+strings.Join(v, ","))
	}
	sort.Strings(ps)
	return strings.Join(ps, "&")
}

func main() {}
