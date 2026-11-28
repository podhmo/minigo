package vm

import (
	"go/parser"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func anonTd(t *testing.T, spelling string) *runtime.TypeDef {
	t.Helper()
	e, err := parser.ParseExpr(spelling)
	if err != nil {
		t.Fatalf("parse %q: %v", spelling, err)
	}
	return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: e}
}

// a spelled compare must reject spellings that discard content:
// *struct{X int} and *struct{Y int} both spell *struct{}.
func TestSameTypedefLossySpelling(t *testing.T) {
	v := &VM{}
	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"distinct struct bodies", "*struct{X int}", "*struct{Y int}", false},
		{"distinct func signatures", "*func(int) int", "*func(string)", false},
		{"struct inside a composite", "[]struct{X int}", "[]struct{Y int}", false},
		{"complete spellings still agree", "*pkg.T", "*pkg.T", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := anonTd(t, tc.a), anonTd(t, tc.b)
			if diff := cmp.Diff(tc.want, v.sameTypedef(a, b)); diff != "" {
				t.Errorf("sameTypedef mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestContainerAssertUntaggedHostMap(t *testing.T) {
	underlying := &runtime.TypeDef{Kind: runtime.KindMap, Anon: anonTd(t, "map[string]any").Anon}
	v := &VM{H: Hooks{Underlying: func(td *runtime.TypeDef) (*runtime.TypeDef, error) {
		if td.Kind == runtime.KindNamedBasic {
			return underlying, nil
		}
		return td, nil
	}}}
	derived := &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: "DerivedMap"}
	if v.containerAssert(nil, derived, nil, runtime.KindMap) {
		t.Fatal("untagged host map asserted to a derived map type")
	}
	anon := &runtime.TypeDef{Kind: runtime.KindMap, Anon: underlying.Anon}
	if !v.containerAssert(nil, anon, nil, runtime.KindMap) {
		t.Fatal("untagged host map rejected by the anonymous map assertion")
	}
}
