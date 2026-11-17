package minigo_test

import (
	"context"
	"math"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// maps.Insert over a host-bound map converts yielded script containers
// element-wise to the host map's declared types (review: a []string
// element arrives through goNative as []any and used to be rejected).
func TestHostMapInsert(t *testing.T) {
	e := minigo.NewEngine(".")
	dst := url.Values{}
	e.Bind("hostmaps.test/hm", map[string]runtime.Value{
		"SrcStrs": &runtime.GoValue{V: url.Values{"a": {"1", "2"}, "b": {"3"}}},
		"DstStrs": &runtime.GoValue{V: dst},
		"SrcAny":  &runtime.GoValue{V: map[any]any{"k": "v", "n": 1}},
		"DstAny":  &runtime.GoValue{V: map[any]any{}},
	})
	v, err := e.Run(context.Background(), "./testdata/hostmapbind", "InsertRoundTrip")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if diff := cmp.Diff("[1 2]v", v); diff != "" {
		t.Fatalf("InsertRoundTrip (-want +got):\n%s", diff)
	}
	// the host map itself was written (not a copy)
	if diff := cmp.Diff([]string{"3"}, dst["b"]); diff != "" {
		t.Fatalf("host map dst[b] (-want +got):\n%s", diff)
	}
}

// maps.Values Collect on a host map with a NaN key must not panic —
// a NaN key is not equal to itself, so MapKeys+MapIndex re-lookup
// fails; Collect reads via MapRange.
func TestHostMapValuesNaN(t *testing.T) {
	e := minigo.NewEngine(".")
	e.Bind("hostmaps.test/hm", map[string]runtime.Value{
		"NanMap": &runtime.GoValue{V: map[float64]int{math.NaN(): 7, 2.0: 8}},
	})
	v, err := e.Run(context.Background(), "./testdata/hostmapbind", "CollectValues")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, ok := v.(*runtime.Slice)
	if !ok {
		t.Fatalf("got %v (%T), want *runtime.Slice", v, v)
	}
	if diff := cmp.Diff(2, len(s.Elems)); diff != "" {
		t.Fatalf("values count (-want +got):\n%s", diff)
	}
}
