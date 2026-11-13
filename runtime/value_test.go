package runtime

import (
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func structKey(name string, fields ...Value) *Struct {
	def := &TypeDef{Name: name, Kind: KindStruct}
	for range fields {
		def.Fields = append(def.Fields, "F")
	}
	return &Struct{Def: def, Fields: fields}
}

func TestMapOps(t *testing.T) {
	m := &Map{}

	m.Insert("a", int64(1))
	m.Insert("b", int64(2))
	m.Insert("a", int64(3)) // overwrite: order keeps the first slot

	if got, want := m.Len(), 2; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if v, ok := m.Get("a"); !ok || v != int64(3) {
		t.Fatalf("Get(a) = %v, %v; want 3, true", v, ok)
	}
	if _, ok := m.Get("zzz"); ok {
		t.Fatalf("Get(zzz) reported present")
	}

	var order []Value
	for i := 0; i < m.Len(); i++ {
		k, _ := m.At(i)
		order = append(order, k)
	}
	if diff := cmp.Diff([]Value{"a", "b"}, order); diff != "" {
		t.Fatalf("insertion order mismatch (-want +got):\n%s", diff)
	}
}

func TestMapCompositeAndNaNKeys(t *testing.T) {
	m := &Map{}

	// composite keys compare by content: two separately built equal
	// structs are one key.
	m.Insert(structKey("P", int64(1), "x"), "first")
	m.Insert(structKey("P", int64(1), "x"), "second")
	m.Insert(structKey("P", int64(2), "x"), "other")
	if got, want := m.Len(), 2; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if v, _ := m.Get(structKey("P", int64(1), "x")); v != "second" {
		t.Fatalf("Get(P{1,x}) = %v, want second", v)
	}

	// a NaN key is stored but unreachable — the canonical nonce differs
	// on every compute, like Go's never-equal NaN semantics.
	m.Insert(math.NaN(), "nan")
	if got, want := m.Len(), 3; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if _, ok := m.Get(math.NaN()); ok {
		t.Fatalf("Get(NaN) reported reachable")
	}
	if m.Delete(math.NaN()) {
		t.Fatalf("Delete(NaN) reported present")
	}
}

func TestMapDeleteAndClear(t *testing.T) {
	m := &Map{}
	m.Insert("a", int64(1))
	m.Insert("b", int64(2))

	if !m.Delete("a") {
		t.Fatalf("Delete(a) reported absent")
	}
	if _, ok := m.Get("a"); ok {
		t.Fatalf("Get(a) still present after Delete")
	}
	// the order slot leaves too — a stale entry would range as a:<nil>.
	if got, want := m.Len(), 1; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}
	if k, _ := m.At(0); k != "b" {
		t.Fatalf("At(0) key = %v, want b", k)
	}
	if m.Delete("a") {
		t.Fatalf("Delete(a) reported present twice")
	}

	m.Clear()
	if got := m.Len(); got != 0 {
		t.Fatalf("Len after Clear = %d, want 0", got)
	}
	if _, ok := m.Get("b"); ok {
		t.Fatalf("Get(b) still present after Clear")
	}
}

// mapKeysOf lists m's keys in insertion order.
func mapKeysOf(m *Map) []Value {
	var out []Value
	for i := range m.Len() {
		k, _ := m.At(i)
		out = append(out, k)
	}
	return out
}

// TestMapHeadersShareContents: a second header over a map sees every
// insert, delete and clear made through the first, and the other way
// round, while keeping its own type tag.
func TestMapHeadersShareContents(t *testing.T) {
	named := &TypeDef{Name: "M", Kind: KindMap}
	for _, tc := range []struct {
		name string
		new  func() *Map
	}{
		{"NewMap", func() *Map { return NewMap(nil, false) }},
		{"zero Map", func() *Map { return &Map{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.new()
			b := a.Header()
			b.Typ = named
			if a.Typ != nil || b.Typ != named {
				t.Fatalf("Typ: a=%v b=%v; the tag is per header", a.Typ, b.Typ)
			}

			a.Insert("x", int64(1))
			b.Insert("y", int64(2))
			if diff := cmp.Diff([]Value{"x", "y"}, mapKeysOf(a)); diff != "" {
				t.Errorf("a keys after inserts (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(mapKeysOf(a), mapKeysOf(b)); diff != "" {
				t.Errorf("headers disagree on keys (-a +b):\n%s", diff)
			}

			// a delete through either header leaves no ghost in the other
			if !b.Delete("x") {
				t.Errorf("b.Delete(x) reported absent")
			}
			if _, ok := a.Get("x"); ok || a.Len() != 1 {
				t.Errorf("a still holds x after b.Delete: len=%d", a.Len())
			}
			if k, v := a.At(0); k != "y" || v != int64(2) {
				t.Errorf("a.At(0) = %v, %v; want y, 2", k, v)
			}
			if !a.Delete("y") || b.Len() != 0 {
				t.Errorf("b.Len after a.Delete(y) = %d, want 0", b.Len())
			}

			// a walk begun through one header survives a delete through
			// the other: the delete copies the order slices, not shifts
			a.Insert("p", int64(1))
			a.Insert("q", int64(2))
			w := a.BeginWalk()
			b.Delete("p")
			if k, ok := w.Key(0); ok {
				t.Errorf("walk reports deleted key %v present", k)
			}
			if k, ok := w.Key(1); !ok || k != "q" {
				t.Errorf("walk slot 1 = %v, %v; want q, true", k, ok)
			}

			// clear through either header empties both
			a.Insert("r", int64(3))
			b.Clear()
			if a.Len() != 0 || b.Len() != 0 {
				t.Errorf("after b.Clear: a.Len=%d b.Len=%d", a.Len(), b.Len())
			}
			a.Insert("s", int64(4))
			if v, ok := b.Get("s"); !ok || v != int64(4) {
				t.Errorf("b.Get(s) = %v, %v after a.Insert", v, ok)
			}
		})
	}
}

// TestMapClone: a clone keeps the type tag and entries but not the
// storage.
func TestMapClone(t *testing.T) {
	td := &TypeDef{Name: "M", Kind: KindMap}
	m := NewMap(td, false)
	m.Insert("a", int64(1))
	m.Insert("b", int64(2))
	c := m.Clone()
	if c.Typ != td {
		t.Errorf("clone Typ = %v, want %v", c.Typ, td)
	}
	m.Delete("a")
	c.Insert("c", int64(3))
	if diff := cmp.Diff([]Value{"a", "b", "c"}, mapKeysOf(c)); diff != "" {
		t.Errorf("clone keys (-want +got):\n%s", diff)
	}
	if _, ok := m.Get("c"); ok || m.Len() != 1 {
		t.Errorf("original reflects clone writes: len=%d", m.Len())
	}
	if got := (&Map{}).Clone().Len(); got != 0 {
		t.Errorf("clone of zero Map has %d entries", got)
	}
}

// TestFoldRepeatedFrames: consecutive identical frames (recursion dumps)
// keep their first maxRunShown verbatim then fold the rest into
// "... repeated N more times ..." — ~50 real frames stay visible for
// debugging instead of a thousand-line dump or a 2-line stub.
func TestFoldRepeatedFrames(t *testing.T) {
	f := func(s string) string { return `File "t.go", line 1, in ` + s + `()` }

	// a run at or under maxRunShown renders untouched — small repeats
	// are real context, not noise
	frames := []string{f("f"), f("f"), f("f"), f("f")}
	got := renderFrames(frames)
	if diff := cmp.Diff(strings.Join(frames, "\n"), got); diff != "" {
		t.Errorf("short identical run (-want +got):\n%s", diff)
	}

	// a long run keeps its first maxRunShown, then one marker line
	frames = make([]string, maxRunShown+10)
	for i := range frames {
		frames[i] = f("f")
	}
	got = renderFrames(frames)
	want := strings.Repeat(f("f")+"\n", maxRunShown) + "... repeated 10 more times ..."
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("long identical run (-want +got):\n%s", diff)
	}

	// mixed runs fold independently, singletons pass through
	frames = append([]string{f("a")}, rep(f("b"), maxRunShown+2)...)
	frames = append(frames, f("c"))
	want = f("a") + "\n" + strings.Repeat(f("b")+"\n", maxRunShown) +
		"... repeated 2 more times ...\n" + f("c")
	got = renderFrames(frames)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mixed runs (-want +got):\n%s", diff)
	}

	// distinct entries are untouched
	frames = []string{f("a"), f("b"), f("c")}
	if got := renderFrames(frames); got != strings.Join(frames, "\n") {
		t.Errorf("distinct frames changed:\n%s", got)
	}

	// folding precedes the cap: >maxTracebackEntries identical frames
	// render as maxRunShown frames + marker, not head/tail elision
	frames = make([]string, maxTracebackEntries*2)
	for i := range frames {
		frames[i] = f("f")
	}
	got = renderFrames(frames)
	want = strings.Repeat(f("f")+"\n", maxRunShown) + "... repeated 1950 more times ..."
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cap-after-fold (-want +got):\n%s", diff)
	}
}

func rep(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
