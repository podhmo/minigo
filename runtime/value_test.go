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
