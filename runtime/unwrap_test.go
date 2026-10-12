package runtime

import (
	"errors"
	"go/ast"
	"go/constant"
	"go/token"
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

func TestDeepUnwrapCycles(t *testing.T) {
	t.Run("self map", func(t *testing.T) {
		m := &Map{}
		m.Insert("self", m)
		got, ok := DeepUnwrap(m).(map[any]any)
		if !ok {
			t.Fatalf("got %T, want map[any]any", DeepUnwrap(m))
		}
		inner, ok := got["self"].(map[any]any)
		if !ok {
			t.Fatalf("got[\"self\"] = %T, want map[any]any", got["self"])
		}
		// the cycle folds back to the partially built container —
		// mutating the inner map must show on the outer one.
		inner["marker"] = true
		if _, ok := got["marker"]; !ok {
			t.Errorf("got[\"self\"] is not the same map as the result")
		}
	})

	t.Run("map-slice mutual cycle", func(t *testing.T) {
		s := &Slice{}
		m := &Map{}
		s.Elems = append(s.Elems, m)
		m.Insert("s", s)
		got, ok := DeepUnwrap(m).(map[any]any)
		if !ok {
			t.Fatalf("got %T, want map[any]any", DeepUnwrap(m))
		}
		xs, ok := got["s"].([]any)
		if !ok {
			t.Fatalf("got[\"s\"] = %T, want []any", got["s"])
		}
		inner, ok := xs[0].(map[any]any)
		if !ok {
			t.Fatalf("got[\"s\"][0] = %T, want map[any]any", xs[0])
		}
		inner["marker"] = true
		if _, ok := got["marker"]; !ok {
			t.Errorf("got[\"s\"][0] is not the same map as the result")
		}
	})

	t.Run("shared subtree is not a cycle", func(t *testing.T) {
		shared := &Slice{Elems: []Value{int64(1)}}
		got, ok := DeepUnwrap(&Slice{Elems: []Value{shared, shared}}).([]any)
		if !ok {
			t.Fatalf("got %T, want []any", got)
		}
		want := []any{[]any{int64(1)}, []any{int64(1)}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
		// a shared subtree folds to the one result object — the
		// input's aliasing carries over, like deepHost.
		if &got[0].([]any)[0] != &got[1].([]any)[0] {
			t.Errorf("shared subtree did not fold to one result object")
		}
	})

	t.Run("ref cycle", func(t *testing.T) {
		c := &Cell{}
		c.Elem = c
		// a cell handed its own referrer stops at the repeat — the
		// honest answer for a reference that never bottoms out.
		if got := DeepUnwrap(c); got != Value(c) {
			t.Errorf("got %v, want the cell itself", got)
		}
	})
}

func TestDeepUnwrapMapKeys(t *testing.T) {
	arrayTD := &TypeDef{
		Kind: KindSlice,
		Anon: &ast.ArrayType{
			Len: &ast.BasicLit{Kind: token.INT, Value: "1"},
			Elt: ast.NewIdent("int"),
		},
	}

	t.Run("script array key stays boxed", func(t *testing.T) {
		arrKey := &Slice{Elems: []Value{int64(1)}, Typ: arrayTD}
		m := &Map{}
		m.Insert(arrKey, "x")
		got, ok := DeepUnwrap(m).(map[any]any)
		if !ok {
			t.Fatalf("got %T, want map[any]any", DeepUnwrap(m))
		}
		// before the fix this panicked "hash of unhashable type
		// []interface {}" — the key survives as its *Slice box.
		if got[arrKey] != "x" {
			t.Errorf("got[arrKey] = %v, want x", got[arrKey])
		}
	})

	t.Run("host array key surfaces as Go array", func(t *testing.T) {
		k := &GoValue{V: [2]int{1, 2}}
		m := &Map{}
		m.Insert(k, "y")
		got, ok := DeepUnwrap(m).(map[any]any)
		if !ok {
			t.Fatalf("got %T, want map[any]any", DeepUnwrap(m))
		}
		if got[[2]int{1, 2}] != "y" {
			t.Errorf("got[[2]int{1,2}] = %v, want y", got[[2]int{1, 2}])
		}
	})

	t.Run("nested map value", func(t *testing.T) {
		arrKey := &Slice{Elems: []Value{int64(7)}, Typ: arrayTD}
		inner := &Map{}
		inner.Insert(arrKey, "v")
		outer := &Map{}
		outer.Insert("inner", inner)
		got, ok := DeepUnwrap(outer).(map[any]any)
		if !ok {
			t.Fatalf("got %T, want map[any]any", DeepUnwrap(outer))
		}
		im, ok := got["inner"].(map[any]any)
		if !ok {
			t.Fatalf("got[\"inner\"] = %T, want map[any]any", got["inner"])
		}
		if im[arrKey] != "v" {
			t.Errorf("inner[arrKey] = %v, want v", im[arrKey])
		}
	})
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
