package typedef

import (
	"bufio"
	"io"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// hostDepths pins the promotion depth a bound host type's method set is
// reported at: declared methods sit at 0, members promoted through an
// anonymous field one level deeper than inside it, and an anonymous
// interface field contributes at depth 1 even under a pointer view.
type depthInner struct{}

func (depthInner) Write(p []byte) (int, error) { return len(p), nil }

type depthHost struct{ depthInner }

type depthIfaceHost struct {
	io.Reader
}

func (depthIfaceHost) Write(p []byte) (int, error) { return len(p), nil }

func TestHostDepths(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   reflect.Type
		want map[string]int
	}{
		{
			"readwriter promotes Write at depth 1",
			reflect.TypeOf(bufio.ReadWriter{}),
			map[string]int{"Write": 1, "WriteString": 1, "Read": 1},
		},
		{
			"struct embed promotes at depth 1 under a pointer view",
			reflect.TypeOf((*depthHost)(nil)),
			map[string]int{"Write": 1},
		},
		{
			"interface embed stays depth 1 under a pointer view",
			reflect.TypeOf((*depthIfaceHost)(nil)),
			map[string]int{"Read": 1, "Write": 0},
		},
		{
			"value view sees the same depths",
			reflect.TypeOf(depthIfaceHost{}),
			map[string]int{"Read": 1, "Write": 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hostDepths(tc.rt)
			sub := map[string]int{}
			for m := range tc.want {
				sub[m] = got[m]
			}
			if diff := cmp.Diff(tc.want, sub); diff != "" {
				t.Errorf("depths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
