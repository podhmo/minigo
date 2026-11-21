package typedef

import (
	"bufio"
	"go/ast"
	"io"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
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

// A typedef interned first as a name-only tag gains HostNew from the
// carrier that arrives later; a source twin of a host type declares the
// unexported methods reflect never lists (exprNode on go/ast's nodes).
// The HostNew walk must keep both — losing the declared set made an
// inferred *ast.Ident fail the ast.Expr constraint.
func TestMethodWalkHostCarriesDeclaredMethods(t *testing.T) {
	td := &runtime.TypeDef{
		Name:    "ast.Ident",
		Kind:    runtime.KindStruct,
		HostNew: func() any { return &ast.Ident{} },
		Methods: map[string]*runtime.Function{
			"exprNode": {Name: "exprNode"},
		},
	}
	r := New(Deps{})
	ms := r.MethodWalkU(td, true, map[walkKey]bool{})
	if !ms.Names["exprNode"] || ms.Funcs["exprNode"] == nil {
		t.Fatalf("declared method missing from host walk: names=%v funcs=%v", ms.Names, ms.Funcs)
	}
	if !ms.Names["End"] {
		t.Fatalf("reflect method missing from host walk: %v", ms.Names)
	}
	mi, err := r.TypeMethodInfoU(td, true)
	if err != nil {
		t.Fatal(err)
	}
	if !mi.Names["exprNode"] {
		t.Fatalf("declared method missing from method info: %v", mi.Names)
	}
}
