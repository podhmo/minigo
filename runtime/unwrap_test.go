package runtime

import (
	"errors"
	"go/constant"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDeepUnwrap(t *testing.T) {
	errBoom := errors.New("boom")

	cases := []struct {
		name string
		in   Value
		want any
	}{
		{"nil", nil, nil},
		{"NIL", NIL, nil},
		{"IfaceNil untagged", &IfaceNil{}, nil},
		{"IfaceNil interface tag", &IfaceNil{Typ: &TypeDef{Kind: KindInterface}}, nil},
		{"IfaceNil concrete tag", &IfaceNil{Typ: &TypeDef{Kind: KindStruct, Name: "E"}}, nil},
		{"TypedNil", &TypedNil{Typ: &TypeDef{Kind: KindPointer, Name: "*E"}}, nil},
		{"basic", int64(7), int64(7)},
		{"string", "x", "x"},
		{"Named", &Named{Typ: &TypeDef{Name: "MyInt"}, V: int64(3)}, int64(3)},
		{"Cell", &Cell{Elem: "c"}, "c"},
		{"GoValue", &GoValue{V: errBoom}, errBoom},
		{"GoValue struct", &GoValue{V: struct{ N int }{N: 1}}, struct{ N int }{N: 1}},
		{"UConst int", &UConst{V: constant.MakeInt64(42)}, int64(42)},
		{"UConst string", &UConst{V: constant.MakeString("s")}, "s"},
		{"UConst rune", &UConst{V: constant.MakeInt64(97), Rune: true}, int64(97)},
		{
			"UConst unmaterializable",
			&UConst{V: constant.MakeUnknown()},
			nil, // checked separately below — passes through
		},
		{
			"Tuple",
			&Tuple{Elems: []Value{int64(1), "two", &Cell{Elem: true}}},
			[]any{int64(1), "two", true},
		},
		{
			"Slice",
			&Slice{Elems: []Value{int64(1), &Named{Typ: &TypeDef{Name: "I"}, V: int64(2)}}},
			[]any{int64(1), int64(2)},
		},
		{
			"Slice virtual",
			&Slice{N: 3, Zero: "z"},
			[]any{"z", "z", "z"},
		},
		{
			"Map",
			func() Value {
				m := &Map{}
				m.Insert("a", &UConst{V: constant.MakeInt64(1)})
				m.Insert("b", &Slice{Elems: []Value{"x"}})
				return m
			}(),
			map[any]any{"a": int64(1), "b": []any{"x"}},
		},
		{
			"nested containers",
			&Tuple{Elems: []Value{
				&Slice{Elems: []Value{
					&Map{Pairs: map[Value]Value{"k": &GoValue{V: "host"}}, Order: []Value{"k"}, Keys: []Value{"k"}},
				}},
			}},
			[]any{[]any{map[any]any{"k": "host"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "UConst unmaterializable" {
				got := DeepUnwrap(c.in)
				if _, ok := got.(*UConst); !ok {
					t.Errorf("got %T, want *UConst passthrough", got)
				}
				return
			}
			if diff := cmp.Diff(c.want, DeepUnwrap(c.in), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestErrorOf(t *testing.T) {
	errBoom := errors.New("boom")
	ifaceTD := &TypeDef{Kind: KindInterface, Name: "error"}

	for _, c := range []struct {
		name string
		in   Value
		want error
	}{
		{"nil", nil, nil},
		{"NIL", NIL, nil},
		{"iface nil", &IfaceNil{Typ: ifaceTD}, nil},
		{"Named nil", &Named{Typ: &TypeDef{Name: "E"}, V: NIL}, nil},
		{"boxed error", &GoValue{V: errBoom}, errBoom},
	} {
		if got := ErrorOf(c.in); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	for _, in := range []Value{int64(1), "err", &GoValue{V: "not an error"}} {
		if err := ErrorOf(in); err == nil {
			t.Errorf("ErrorOf(%T): expected a contract violation", in)
		}
	}
}
