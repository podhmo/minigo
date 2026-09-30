package minigo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

// TestConversions covers `T(x)` conversion calls: the special string <->
// []byte/[]rune forms, identical-underlying slice/map/chan/ptr/struct
// conversions through named types and aliases, and []T under generics.
// Illegal forms (mismatched element/key/field types) must trap like real Go.
func TestConversions(t *testing.T) {
	e := newEngine(t)

	t.Run("ok", func(t *testing.T) {
		cases := []struct {
			fn   string
			want any
		}{
			// string <-> []byte / []rune
			{"ByteFromString", int64(304)},
			{"RuneFromString", int64(733)},
			{"StringFromBytes", "yo!"},
			{"StringFromRunes", "abz"},
			{"ByteFromByteSlice", int64(124)},
			{"ByteSliceFromUint8", int64(213)},
			{"Uint8SliceFromByte", int64(213)},

			// named slice types and aliases
			{"NamedByteCast", int64(305)},
			{"NamedByteCastBack", "hi"},
			{"NamedByteNil", int64(1)},
			{"ChainByteCast", int64(304)},
			{"AliasByteCast", int64(304)},
			{"NamedSliceCast", int64(21)},
			{"NamedSliceOfNamed", int64(7)},
			{"NamedToNamedSlice", int64(15)},
			{"NamedElemCast", int64(304)},
			{"NamedElemCastBack", "hi"},

			// generics: []T with the bound element type
			{"GenericCast", int64(9)},
			{"GenericSliceCast", int64(42)},
			{"GenericNamedSlice", int64(6)},
			{"GenericNamedSliceString", int64(21)},
			{"GenericTByteCast", int64(304)},
			{"GenericWrapElem", int64(41)},

			// maps / chans / pointers / structs with identical underlying
			{"NamedMapCast", int64(5)},
			{"NamedChanCast", int64(7)},
			{"NamedPtrCast", int64(9)},
			{"StructCast", int64(8)},

			// nil and misc
			{"NilToSliceOK", int64(1)},
			{"SlicePtrCast", int64(1)},
			{"ByteOfStringIdx", int64(-23)},

			// slicing/append keep the declared tag; typed nil through chains
			{"SlicedRuneString", "él"},
			{"SlicedNamedSlice", int64(15)},
			{"AppendKeepsByteTag", "hi"},
			{"AppendKeepsRuneTag", "hé"},
			{"AppendOnTypedNil", "ü"},
			{"ChainNilRetag", int64(1)},

			// nested generics and anonymous struct sources
			{"NestedGenericCast", int64(6)},
			{"NestedGenericLit", int64(4)},
			{"NestedGenericElemAssign", int64(11)},
			{"AnonStructCast", int64(5)},

			// declared pointer/func identity: `x.(P)` checks the tag
			{"PtrAssertCastOK", int64(4)},
			{"PtrAssertBindOK", int64(1)},
			{"PtrAssertAnonBad", int64(1)},
			{"PtrAssertNamedBad", int64(1)},
			{"PtrAssertNilOK", int64(1)},
			{"PtrFieldThrough", int64(7)},
			{"PtrFieldSet", int64(9)},
			{"PtrSetInd", int64(7)},

			// declared func types: methods on F, asserts check identity
			{"FnMethodCall", int64(33)},
			{"FnAssertCastOK", int64(5)},
			{"FnAssertBindOK", int64(1)},
			{"FnAssertAnonBad", int64(1)},

			// named containers assert by declared identity
			{"MapAssertBindOK", int64(1)},
			{"MapAssertOtherBad", int64(1)},
			{"MapAssertUnderlyingBad", int64(1)},
			{"MapAssertCastOK", int64(1)},
			{"SliceAssertBindOK", int64(1)},
			{"SliceAssertUnderlyingBad", int64(1)},
			{"ChanAssertBindOK", int64(1)},
			{"ChanAssertOtherBad", int64(1)},

			// nil receivers on nilable declared types bind like Go
			{"NilSliceRecv", int64(0)},
			{"NilFnSelectBind", int64(1)},

			// instantiated generics assert on their type arguments
			{"WrapAssertSameOK", int64(1)},
			{"WrapAssertOtherBad", int64(1)},
			{"WrapAssertNamedArgBad", int64(1)},
			{"WrapAssertByteOK", int64(1)},
			{"WrapAssertRuneOK", int64(1)},
		}
		for _, c := range cases {
			got := run(t, e, "./testdata/conversions", c.fn)
			if got != c.want {
				t.Errorf("%s: got %v (%T), want %v (%T)", c.fn, got, got, c.want, c.want)
			}
		}
	})

	t.Run("trap", func(t *testing.T) {
		// every one of these is also rejected by the Go compiler
		cases := []struct {
			fn  string
			msg string
		}{
			{"ByteFromInts", "cannot convert []int to []byte"},
			{"NamedSliceRoundTrip", "cannot convert []int to Ints"},
			{"SliceCastToInts", "cannot convert Ints to []int"},
			{"GenericTByteCastBad", "cannot convert string to []T"},
			{"NamedMapCastBad", "cannot convert M1 to MInt"},
			{"StructCastBad", "cannot convert Sq3 to Sq2"},
			{"SliceToStringBad", "cannot convert []int to string"},
			{"SliceToSliceBad", "cannot convert []rune to []byte"},
			{"StringToSliceBad", "cannot convert string to []int"},
			{"PtrToSliceBad", "cannot convert *byte to []byte"},
			{"NilToSliceBad", "cannot convert []rune to []byte"},
			{"AnonStructCastBad", "cannot convert struct{} to Sq2"},

			// a declared pointer keeps Sq's methods out of its method
			// set, and *p = v coerces to the pointee's declared type
			{"PtrNoPromote", "has no field or method Inc"},
			{"PtrSetIndBad", "cannot use Sq2 as Sq"},
			{"PtrNilDeref", "nil pointer dereference"},
			{"NilPtrValueMethod", "nil pointer dereference"},
		}
		for _, c := range cases {
			_, err := e.Run(context.Background(), "./testdata/conversions", c.fn)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Errorf("%s: expected %q trap, got %v", c.fn, c.msg, err)
			}
		}
	})

	t.Run("string on non-integer elements", func(t *testing.T) {
		// a byte-family slice with non-int64 elements is unreachable in
		// typed Go but can be host-injected — it must trap, not skip.
		for _, fn := range []string{"AnyToString", "AnyToStringRune"} {
			_, err := e.Run(context.Background(), "./testdata/conversions", fn,
				&runtime.Slice{Elems: []runtime.Value{"x"}})
			if err == nil || !strings.Contains(err.Error(), "cannot convert") {
				t.Errorf("%s: expected conversion trap, got %v", fn, err)
			}
		}
	})

	t.Run("cross-package element identity", func(t *testing.T) {
		// []Foo in package A and []Foo in package B are different types.
		_, err := e.Run(context.Background(), "./testdata/convident", "CrossPkgSliceCast")
		if err == nil || !strings.Contains(err.Error(), "cannot convert") {
			t.Fatalf("CrossPkgSliceCast: expected conversion trap, got %v", err)
		}
		cases := []struct {
			fn   string
			want any
		}{
			{"SamePkgSliceCast", int64(2)},
			{"AliasElemCast", int64(3)},
		}
		for _, c := range cases {
			if got := run(t, e, "./testdata/convident", c.fn); got != c.want {
				t.Errorf("%s: got %v, want %v", c.fn, got, c.want)
			}
		}
	})

	t.Run("method Recv is stamped", func(t *testing.T) {
		got := run(t, e, "./testdata/conversions", "PtrTyp")
		td, ok := got.(*runtime.TypeDef)
		if !ok {
			t.Fatalf("PtrTyp: got %T, want *runtime.TypeDef", got)
		}
		m, ok := td.Methods["Inc"]
		if !ok {
			t.Fatalf("Sq has no method Inc")
		}
		if m.Recv != "Sq" {
			t.Fatalf("Inc.Recv = %q, want Sq", m.Recv)
		}
	})
}
