package minigo_test

import (
	"context"
	"strings"
	"testing"
)

// TestUnsafePointer covers unsafe.Pointer as a bound type: the symbol
// resolves in type position (var, field, param, conversion target), a
// pointer value converts to it from any *T and back to any *T, and the
// Named-tagged payload keeps write-through, identity and nil-ness. gc's
// compile-time rejections (direct assignment, dereference, non-pointer
// sources) surface as traps; uintptr -> unsafe.Pointer is the one
// documented boundary since no ref can be minted from an integer.
func TestUnsafePointer(t *testing.T) {
	e := newEngine(t)

	t.Run("ok", func(t *testing.T) {
		cases := []struct {
			fn   string
			want any
		}{
			// the symbol resolves and nil-ness works
			{"PtrNonNil", int64(1)},
			{"ZeroIsNil", int64(1)},
			{"NilConverts", int64(1)},

			// identity: same storage is equal, different is not
			{"SameAddrEq", int64(1)},
			{"DiffAddrNe", int64(1)},

			// *T -> unsafe.Pointer -> *T keeps write-through
			{"RoundTripRead", int64(7)},
			{"RoundTripWrite", int64(42)},
			{"FieldAddrWrite", int64(9)},
			{"IndexAddrWrite", int64(183)},
			{"TypedNilRoundTrip", int64(1)},
			{"NilToOtherPtr", int64(1)},

			// declared slots: struct fields (text/template's UPI),
			// params, returns, slice elements
			{"FieldRoundTrip", int64(42)},
			{"ArgPasses", int64(5)},
			{"SliceElem", int64(2)},

			// assertions and type spelling keep the unsafe.Pointer tag
			{"AssertSelf", int64(1)},
			{"AssertOtherFails", int64(1)},
			{"TypeName", "unsafe.Pointer"},
			{"TypeSwitch", int64(1)},

			// `type P = unsafe.Pointer` and `type Mine unsafe.Pointer`
			{"AliasConvert", int64(3)},
			{"NamedConvert", int64(6)},

			// unsafe.Pointer -> uintptr mints the ref's address
			{"AddrNonZero", int64(1)},
			{"AddrStable", int64(1)},
		}
		for _, c := range cases {
			got := run(t, e, "./testdata/unsafeptr", c.fn)
			if got != c.want {
				t.Errorf("%s: got %v (%T), want %v (%T)", c.fn, got, got, c.want, c.want)
			}
		}
	})

	t.Run("trap", func(t *testing.T) {
		// every one of these is a compile-time rejection in gc except
		// NilDerefBack (runtime panic) and UintptrToPtr (boundary note)
		cases := []struct {
			fn  string
			msg string
		}{
			{"DerefUnsafe", "cannot indirect unsafe.Pointer"},
			{"AssignRawPtr", "cannot use"},
			{"FieldRawPtr", "cannot use"},
			{"SliceConv", "cannot convert []int to unsafe.Pointer"},
			{"FuncConv", "cannot convert"},
			{"NilDerefBack", "nil pointer dereference"},
			{"UintptrToPtr", "cannot convert"},
		}
		for _, c := range cases {
			_, err := e.Run(context.Background(), "./testdata/unsafeptr", c.fn)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Errorf("%s: expected %q trap, got %v", c.fn, c.msg, err)
			}
		}
	})
}
