package main

import (
	"expvar"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Every slices helper that reads a slice argument takes a nil slice as an
// empty one: a bare `var s []int`, a nil of a named slice type, a nil read
// out of a struct field, an `any`, a call result or a generic parameter.
// Each result is printed with its type and nil-ness, since `Insert(nil, 0)`
// is nil, `Repeat(nil, 3)` is an empty non-nil slice, and `Concat(empty)`
// is nil again.

type IDs []int
type Names []string
type Rec struct {
	Name string
	Age  int
}
type Box struct {
	keys []string
	ids  IDs
}
type Set map[string]bool

func try(tag string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v (%T)\n", tag, r, r)
		}
	}()
	f()
}

// tryMsg is try that names only the panic's message.
func tryMsg(tag string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v\n", tag, r)
		}
	}()
	f()
}

// show prints a slice with its type, nil-ness and length.
func show[S ~[]E, E any](tag string, s S) {
	fmt.Printf("%s: %v %T nil=%v len=%d\n", tag, s, s, s == nil, len(s))
}

func nilInts() []int { return nil }

type source interface{ Items() []int }
type emptySource struct{}

func (emptySource) Items() []int { return nil }

// generic wrappers: the slice reaches the helper through a type parameter.
func firstEven[S ~[]E, E ~int](s S) int {
	return slices.IndexFunc(s, func(x E) bool { return x%2 == 0 })
}

func inserted[S ~[]E, E any](s S, v E) S { return slices.Insert(s, 0, v) }

func removed[S ~[]E, E any](s S) S { return slices.Delete(s, 0, 0) }

func main() {
	var ni []int
	var nn IDs
	var ns []string
	var nr []Rec
	var nf []float64
	var nz []struct{}
	var nany []any
	var b Box
	lit := []int(nil)
	var anyv any = []int(nil)
	viaAny := anyv.([]int)
	empty := []int{}
	id3 := IDs{3, 1, 2}
	i3 := []int{3, 1, 2}
	isEven := func(x int) bool { return x%2 == 0 }
	eqInt := func(a, b int) bool { return a == b }
	cmpInt := func(a, b int) int { return a - b }
	var src source = emptySource{}

	// slices.IndexFunc
	fmt.Println("IndexFunc", slices.IndexFunc(ni, isEven), slices.IndexFunc(nn, isEven), slices.IndexFunc(lit, isEven), slices.IndexFunc(viaAny, isEven), slices.IndexFunc(nilInts(), isEven), slices.IndexFunc(b.ids, isEven), slices.IndexFunc(src.Items(), isEven))
	fmt.Println("IndexFunc", slices.IndexFunc(ns, func(s string) bool { return true }), slices.IndexFunc(nr, func(r Rec) bool { return true }), slices.IndexFunc(nz, func(r struct{}) bool { return true }), slices.IndexFunc(nany, func(x any) bool { return true }))
	fmt.Println("IndexFunc", slices.IndexFunc(i3, isEven), slices.IndexFunc(id3, isEven), slices.IndexFunc([]int{1, 3}, isEven), firstEven(ni), firstEven(nn), firstEven(id3))
	calls := 0
	slices.IndexFunc(ni, func(x int) bool { calls++; return true })
	fmt.Println("IndexFunc calls", calls)

	// slices.EqualFunc
	fmt.Println("EqualFunc", slices.EqualFunc(ni, ni, eqInt), slices.EqualFunc(ni, empty, eqInt), slices.EqualFunc(empty, ni, eqInt), slices.EqualFunc(nn, ni, eqInt), slices.EqualFunc(nn, IDs{}, eqInt), slices.EqualFunc(viaAny, nn, eqInt))
	fmt.Println("EqualFunc", slices.EqualFunc(ni, []int{1}, eqInt), slices.EqualFunc([]int{1}, ni, eqInt), slices.EqualFunc(b.ids, b.ids, eqInt), slices.EqualFunc(b.keys, ns, strings.EqualFold), slices.EqualFunc(ns, []string{"a"}, strings.EqualFold))
	fmt.Println("EqualFunc", slices.EqualFunc(ni, []string(nil), func(a int, b string) bool { return false }), slices.EqualFunc(ns, []int{}, func(a string, b int) bool { return false }))
	fmt.Println("EqualFunc", slices.EqualFunc(id3, i3, eqInt), slices.EqualFunc(IDs{1}, []int{1}, eqInt), slices.EqualFunc(id3, IDs{3, 1}, eqInt))

	// slices.BinarySearch / BinarySearchFunc
	fmt.Println(slices.BinarySearch(ni, 3))
	fmt.Println(slices.BinarySearch(nn, 3))
	fmt.Println(slices.BinarySearch(ns, "a"))
	fmt.Println(slices.BinarySearch(b.keys, "k"))
	fmt.Println(slices.BinarySearch(viaAny, 1))
	fmt.Println(slices.BinarySearch(nf, 1.5))
	fmt.Println(slices.BinarySearch(IDs{1, 3, 5}, 3))
	fmt.Println(slices.BinarySearch([]int{1, 3, 5}, 4))
	fmt.Println(slices.BinarySearchFunc(ni, 3, cmpInt))
	fmt.Println(slices.BinarySearchFunc(nn, 3, cmpInt))
	fmt.Println(slices.BinarySearchFunc(b.ids, 3, cmpInt))
	fmt.Println(slices.BinarySearchFunc(nr, "z", func(r Rec, t string) int { return strings.Compare(r.Name, t) }))
	fmt.Println(slices.BinarySearchFunc([]Rec{{"a", 1}, {"c", 2}}, "c", func(r Rec, t string) int { return strings.Compare(r.Name, t) }))

	// slices.Reverse: a nil slice stays nil
	slices.Reverse(ni)
	slices.Reverse(nn)
	slices.Reverse(ns)
	slices.Reverse(b.keys)
	slices.Reverse(viaAny)
	slices.Reverse(nr)
	slices.Reverse(nz)
	fmt.Println("Reverse", ni == nil, nn == nil, ns == nil, b.keys == nil, viaAny == nil, nr == nil, nz == nil)
	slices.Reverse(id3)
	slices.Reverse(empty)
	fmt.Println("Reverse", id3, empty, empty == nil)
	zs := make([]struct{}, 3)
	slices.Reverse(zs)
	fmt.Println("Reverse", len(zs))

	// slices.Insert: into nil builds a slice of the same type
	show("Insert", slices.Insert(ni, 0, 1, 2))
	show("Insert", slices.Insert(nn, 0, 7))
	show("Insert", slices.Insert(ns, 0, "a", "b"))
	show("Insert", slices.Insert(nr, 0, Rec{"x", 1}))
	show("Insert", slices.Insert(b.keys, 0, "k"))
	show("Insert", slices.Insert(viaAny, 0, 1))
	show("Insert", slices.Insert(nf, 0, 1, 2))
	show("Insert", slices.Insert(nany, 0, 1, "x"))
	show("Insert", slices.Insert(ni, 0, []int{4, 5}...))
	show("Insert", slices.Insert(ni, 0, ni...))
	show("Insert", inserted(ni, 9))
	show("Insert", inserted(nn, 9))
	// nothing to insert: s itself comes back, nil stays nil
	show("Insert", slices.Insert(ni, 0))
	show("Insert", slices.Insert(nn, 0))
	show("Insert", slices.Insert(empty, 0))
	show("Insert", slices.Insert(i3, 3, 9))
	show("Insert", slices.Insert(id3, 1, 9, 8))
	show("Insert", slices.Insert([]float64{1}, 0, 2))
	show("Insert", slices.Insert([]Rec{{"a", 1}}, 1, Rec{"b", 2}))
	try("Insert", func() { show("r", slices.Insert(ni, 1, 9)) })
	try("Insert", func() { show("r", slices.Insert(ni, -1, 9)) })
	try("Insert", func() { show("r", slices.Insert(ni, 2)) })
	try("Insert", func() { show("r", slices.Insert([]int{1}, 3, 9)) })
	show("ni", ni)

	// slices.Delete: a nil slice stays nil
	show("Delete", slices.Delete(ni, 0, 0))
	show("Delete", slices.Delete(nn, 0, 0))
	show("Delete", slices.Delete(ns, 0, 0))
	show("Delete", slices.Delete(b.ids, 0, 0))
	show("Delete", slices.Delete(viaAny, 0, 0))
	show("Delete", slices.Delete(nr, 0, 0))
	show("Delete", removed(ni))
	show("Delete", removed(nn))
	show("Delete", slices.Delete(empty, 0, 0))
	try("Delete", func() { show("r", slices.Delete(ni, 0, 1)) })
	try("Delete", func() { show("r", slices.Delete(ni, 1, 0)) })
	try("Delete", func() { show("r", slices.Delete(ni, 1, 1)) })
	try("Delete", func() { show("r", slices.Delete([]int{1, 2}, 1, 5)) })
	// the vacated tail holds zero values, not holes
	dn := []int{1, 2, 3, 4}
	show("Delete", slices.Delete(dn, 1, 3))
	fmt.Println("Delete tail", dn)
	ds := []string{"a", "b", "c"}
	show("Delete", slices.Delete(ds, 0, 2))
	fmt.Printf("Delete tail %q\n", ds)
	dr := []Rec{{"a", 1}, {"b", 2}}
	show("Delete", slices.Delete(dr, 0, 1))
	fmt.Println("Delete tail", dr)
	dd := IDs{3, 1, 2}
	show("Delete", slices.Delete(dd, 0, 1))
	fmt.Println("Delete tail", dd)
	dk := []int{1, 2}
	show("Delete", slices.Delete(dk, 1, 1))
	fmt.Println("Delete tail", dk)

	// slices.Replace: nothing to put in a nil slice leaves it nil
	show("Replace", slices.Replace(ni, 0, 0))
	show("Replace", slices.Replace(nn, 0, 0))
	show("Replace", slices.Replace(ni, 0, 0, 1, 2))
	show("Replace", slices.Replace(nn, 0, 0, 1))
	try("Replace", func() { show("r", slices.Replace(ni, 0, 1, 1)) })

	// slices.Concat: nil when there is nothing at all
	show("Concat", slices.Concat(ni, ni))
	show("Concat", slices.Concat(ni, []int{1}, empty, nil))
	show("Concat", slices.Concat(nn, IDs{1}, IDs(nil)))
	show("Concat", slices.Concat(empty, empty))
	show("Concat", slices.Concat([]string{"a"}, ns, []string{"b"}))
	show("Concat", slices.Concat(b.ids, b.ids))
	fmt.Println("Concat cap", cap(slices.Concat([]int{1}, []int{2})), cap(slices.Concat([]int{1, 2}, []int{3})))

	// slices.Repeat: never nil, exactly len*count long
	show("Repeat", slices.Repeat(ni, 3))
	show("Repeat", slices.Repeat(nn, 3))
	show("Repeat", slices.Repeat(ni, 0))
	show("Repeat", slices.Repeat(i3, 0))
	show("Repeat", slices.Repeat(i3, 2))
	show("Repeat", slices.Repeat(id3, 2))
	fmt.Println("Repeat cap", cap(slices.Repeat([]int{1, 2}, 3)))
	try("Repeat", func() { show("r", slices.Repeat(ni, -1)) })
	try("Repeat", func() { show("r", slices.Repeat([]int{1, 2}, -1)) })

	// slices.Clip / Clone / Grow / Compact / DeleteFunc / Sort*: nil in, nil out
	show("Clip", slices.Clip(ni))
	show("Clip", slices.Clip(nn))
	show("Clone", slices.Clone(ni))
	show("Clone", slices.Clone(nn))
	show("Clone", slices.Clone(viaAny))
	show("Grow", slices.Grow(ni, 0))
	show("Grow", slices.Grow(nn, 0))
	show("Compact", slices.Compact(ni))
	show("Compact", slices.Compact(nn))
	show("CompactFunc", slices.CompactFunc(ni, eqInt))
	show("DeleteFunc", slices.DeleteFunc(ni, isEven))
	show("DeleteFunc", slices.DeleteFunc(nn, isEven))
	slices.Sort(ni)
	slices.SortFunc(nn, cmpInt)
	slices.SortStableFunc(nr, func(a, b Rec) int { return a.Age - b.Age })
	fmt.Println("Sort", ni == nil, nn == nil, nr == nil)
	for c := range slices.Chunk(ni, 2) {
		fmt.Println("Chunk", c)
	}
	for c := range slices.Chunk(nn, 2) {
		fmt.Println("Chunk", c)
	}
	show("AppendSeq", slices.AppendSeq(ni, slices.Values([]int{1, 2})))
	show("AppendSeq", slices.AppendSeq(nn, slices.Values(ni)))
	show("Collect", slices.Collect(slices.Values(ni)))
	fmt.Println("IsSorted", slices.IsSorted(ni), slices.IsSortedFunc(nn, cmpInt))
	fmt.Println("Compare", slices.Compare(ni, empty), slices.Compare(ni, i3), slices.Compare(i3, ni))
	fmt.Println("Index", slices.Index(ni, 1), slices.Contains(nn, 1), slices.ContainsFunc(b.ids, isEven), slices.Equal(ni, empty))

	// elements are copied, not shared with the source slice
	rs := []Rec{{"a", 1}, {"b", 2}}
	c := slices.Clone(rs)
	c[0].Name = "X"
	r := slices.Repeat(rs, 2)
	r[0].Name = "Y"
	cc := slices.Concat(rs, rs)
	cc[0].Name = "W"
	fmt.Println("copies", rs, c, r, cc)
	rec := Rec{"q", 5}
	withRec := slices.Insert(nr, 0, rec)
	rec.Name = "changed"
	fmt.Println("operand copy", withRec, rec)

	// maps.Copy: a nil map is an empty one
	m := map[string]int{"a": 1}
	var nm map[string]int
	maps.Copy(m, nm)
	maps.Copy(nm, nm)
	maps.Copy(nm, map[string]int{})
	var ns2 Set
	set := Set{"s": true}
	maps.Copy(set, ns2)
	maps.Copy(ns2, Set{})
	fmt.Println("maps.Copy", m, set, nm == nil, ns2 == nil)
	tryMsg("maps.Copy", func() { maps.Copy(nm, map[string]int{"b": 2}) })
	tryMsg("maps.Copy", func() { maps.Copy(ns2, Set{"b": true}) })

	// expvar.Map keeps a nil key slice until its first Set
	var ev expvar.Map
	ev.Set("a", new(expvar.Int))
	ev.Add("c", 3)
	ev.Set("b", new(expvar.Int))
	fmt.Println("expvar", ev.String())
	ev.Do(func(kv expvar.KeyValue) { fmt.Println("expvar", kv.Key, kv.Value) })
}
