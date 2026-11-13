// Package main exercises nil named host maps (url.Values, http.Header)
// and slices (net.IP) that a host call or the embedder hands in: they
// are typed nils, so `== nil` holds while %T, method calls, len, range,
// index, delete, clear, json and template still see the host type, and a
// non-nil one is unaffected.
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"text/template"
)

// box keeps a host map in a script struct field.
type box struct{ V url.Values }

// Equal compares a request's nil Form and Trailer against nil in every
// form a comparison takes.
func Equal(r *http.Request) string {
	var a, b url.Values = r.Form, nil
	s := fmt.Sprint(r.Trailer == nil, r.Form == nil, r.Header == nil, r.Form != nil, nil == r.Form, a == nil, b == nil)
	switch r.Form {
	case nil:
		s += " nil"
	default:
		s += " set"
	}
	p := &r.Form
	return s + fmt.Sprint(" ", *p == nil, len(*p), isNil(r.Form), isNil(url.Values{}), isNil(nil))
}

func isNil(v url.Values) bool { return v == nil }

// Spelling prints the type and value of a nil Form every way.
func Spelling(r *http.Request) string {
	return fmt.Sprintf("%T %v %d %#v %s|%v", r.Form, r.Form, len(r.Form), r.Form, r.Form, r.Trailer)
}

// Reads indexes, ranges over and calls methods on a nil map.
func Reads(r *http.Request) string {
	n := 0
	for range r.Form {
		n++
	}
	v, ok := r.Form["a"]
	s := fmt.Sprint(n, len(r.Form["a"]), v == nil, ok, r.Form["a"] == nil)
	s += fmt.Sprint(" ", r.Form.Get("a") == "", r.Form.Has("a"), r.Form.Encode() == "")
	s += fmt.Sprint(" ", r.Trailer.Clone() == nil, r.Trailer.Values("x") == nil, r.Trailer.Get("x") == "")
	keys := slices.Sorted(maps.Keys(r.Form))
	return s + fmt.Sprint(" ", keys, len(keys), maps.Clone(r.Form) == nil)
}

// Writes deletes from and clears a nil map, then assigns into it and
// calls a method that does.
func Writes(r *http.Request) (msg string) {
	delete(r.Form, "a")
	clear(r.Form)
	msg = fmt.Sprint(len(r.Form), " ")
	defer func() { msg += fmt.Sprint(recover()) }()
	func() {
		defer func() { msg += fmt.Sprint(recover(), " ") }()
		r.Form["a"] = []string{"1"}
	}()
	r.Trailer.Set("a", "b")
	return msg
}

// ClearNil clears nil maps and slices of every kind: a no-op.
func ClearNil(v url.Values, ip net.IP) string {
	var m map[string]int
	var s []int
	clear(m)
	clear(s)
	clear(v)
	clear(ip)
	return fmt.Sprint(m == nil, s == nil, v == nil, ip == nil, len(m), len(s), len(v), len(ip))
}

// Interface stores a nil Form in an interface: it keeps its type, so it
// is not == nil there, and asserts back to a nil url.Values.
func Interface(r *http.Request) string {
	var i any = r.Form
	u, ok := i.(url.Values)
	s := fmt.Sprintf("%v %T %v %v", i == nil, i, ok, u == nil)
	switch x := i.(type) {
	case url.Values:
		s += fmt.Sprint(" values ", x == nil)
	default:
		s += " other"
	}
	var ip any = net.ParseIP("bad")
	nip, ok := ip.(net.IP)
	return s + fmt.Sprint(" ", ip == nil, ok, nip == nil)
}

// Assign moves a nil Form through variables, fields, composites and a
// host struct field.
func Assign(r *http.Request) string {
	var f url.Values = r.Form
	f2 := r.Form
	s := fmt.Sprint(f == nil, f2 == nil)
	b := box{V: r.Form}
	s += fmt.Sprint(" ", b.V == nil, b.V.Get("a") == "")
	vs := []url.Values{r.Form, {"a": {"1"}}}
	m := map[string]url.Values{"n": r.Form}
	s += fmt.Sprint(" ", vs[0] == nil, vs[1] == nil, m["n"] == nil, m["z"] == nil, len(vs), len(m))
	r2 := &http.Request{Form: r.Form, Trailer: r.Trailer}
	s += fmt.Sprint(" ", r2.Form == nil, r2.Trailer == nil, len(r2.Form))
	r.Form = url.Values{}
	s += fmt.Sprint(" ", r.Form == nil)
	r.Form = nil
	return s + fmt.Sprint(r.Form == nil)
}

// Host hands a nil Form to host code that takes `any`: json, reflect and
// a template see a nil url.Values.
func Host(r *http.Request) string {
	b, _ := json.Marshal(r.Form)
	b2, _ := json.Marshal(map[string]any{"f": r.Form, "t": r.Trailer})
	s := string(b) + string(b2)
	s += fmt.Sprint(" ", reflect.ValueOf(r.Form).IsNil(), reflect.TypeOf(r.Form), reflect.DeepEqual(r.Form, url.Values(nil)))
	t := template.Must(template.New("t").Parse("{{len .}}|{{.}}|{{range $k, $v := .}}x{{end}}"))
	var sb strings.Builder
	err := t.Execute(&sb, r.Form)
	return s + fmt.Sprint(" ", sb.String(), err)
}

// NonNil checks that a populated Form keeps working: not nil, readable,
// writable, typed, and unequal to a nil one.
func NonNil(r *http.Request) string {
	_ = r.ParseForm()
	q := r.URL.Query()
	q["z"] = []string{"9"}
	var i any = r.Form
	return fmt.Sprintf("%v %v %T %d %s %v %v %v %v %v|%s", r.Form == nil, r.Form != nil, r.Form, len(r.Form), r.Form.Get("a"),
		r.PostForm == nil, i == nil, q == nil, len(q), reflect.DeepEqual(r.Form, url.Values(nil)), q.Encode())
}

// Handed takes nil boxes the embedder passes in as host values.
func Handed(h http.Header, ip net.IP, v url.Values) string {
	var i any = v
	n := 0
	for range h {
		n++
	}
	for range ip {
		n++
	}
	s := fmt.Sprint(h == nil, ip == nil, v == nil, len(h), len(ip), len(v), h["A"] == nil, n)
	s += fmt.Sprintf(" %T %T %T %v %v %v", h, ip, v, i == nil, v, ip)
	return s + fmt.Sprint(" ", h.Get("A") == "", v.Get("a") == "", ip.String(), ip.IsLoopback())
}

func main() {}
