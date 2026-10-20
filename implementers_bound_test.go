package minigo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// TestImplementersBoundEmbed pins inspect.Implementers for interfaces
// embedding host-bound ones: a binding that carries its method specs
// (fmt.Stringer) is a real requirement, one that carries only names
// (io.Reader) is an unknown requirement and traps — never an empty
// one that every type in the package satisfies.
func TestImplementersBoundEmbed(t *testing.T) {
	e := newEngine(t)
	stringers := "S,Mixed,StrAlias,ViaAlias,Str,PStr,Both,Wrap,EmbStringer,Own"
	for _, tc := range []struct {
		fn   string
		args []string
		want string
	}{
		{"ImplBound", []string{"S"}, stringers},
		{"ImplBound", []string{"ViaAlias"}, stringers},
		{"ImplBound", []string{"Own"}, stringers},
		{"ImplBound", []string{"Mixed"}, "Mixed,Both"},
		{"ImplBoundHost", []string{"fmt", "Stringer"}, stringers},
	} {
		got := run(t, e, "./testdata/inspectuse", tc.fn, values(tc.args)...)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s%v mismatch (-want +got):\n%s", tc.fn, tc.args, diff)
		}
	}

	for _, tc := range []struct {
		fn   string
		args []string
		want string // a substring of the trap message
	}{
		{"ImplBound", []string{"R"}, "R embeds host-bound io.Reader, whose method set is unknown"},
		{"ImplBound", []string{"MixedR"}, "MixedR embeds host-bound io.Reader, whose method set is unknown"},
		{"ImplBound", []string{"B"}, "B is a constraint interface"},
		{"ImplBoundHost", []string{"io", "Reader"}, "host-bound io.Reader has no readable method set"},
		{"ImplBoundHost", []string{"strings", "Builder"}, "Builder is not an interface type"},
	} {
		_, err := e.Run(context.Background(), "./testdata/inspectuse", tc.fn, values(tc.args)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s%v: want trap containing %q, got %v", tc.fn, tc.args, tc.want, err)
		}
	}
}

func values(ss []string) []runtime.Value {
	vs := make([]runtime.Value, len(ss))
	for i, s := range ss {
		vs[i] = s
	}
	return vs
}
