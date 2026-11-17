package minigo

import (
	"fmt"
	"go/constant"
	"unicode/utf8"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installUnicodeUtf8(h *hostHelpers) {
	e.Bind("unicode/utf8", map[string]runtime.Value{
		"RuneCountInString": h.fn("utf8.RuneCountInString", func(a []any) (any, error) { return utf8.RuneCountInString(str(a[0])), nil }),
		"RuneCount":         h.fn("utf8.RuneCount", func(a []any) (any, error) { return utf8.RuneCount(byteSlice(a[0])), nil }),
		"RuneLen":           h.fn("utf8.RuneLen", func(a []any) (any, error) { return utf8.RuneLen(runeOf(a[0])), nil }),
		"RuneStart":         h.fn("utf8.RuneStart", func(a []any) (any, error) { return utf8.RuneStart(byte(intOf(a[0]))), nil }, utf8.RuneStart),
		"Valid":             h.fn("utf8.Valid", func(a []any) (any, error) { return utf8.Valid(byteSlice(a[0])), nil }, utf8.Valid),
		"ValidString":       h.fn("utf8.ValidString", func(a []any) (any, error) { return utf8.ValidString(str(a[0])), nil }, utf8.ValidString),
		"ValidRune":         h.fn("utf8.ValidRune", func(a []any) (any, error) { return utf8.ValidRune(runeOf(a[0])), nil }, utf8.ValidRune),
		"FullRune":          h.fn("utf8.FullRune", func(a []any) (any, error) { return utf8.FullRune(byteSlice(a[0])), nil }, utf8.FullRune),
		"FullRuneInString":  h.fn("utf8.FullRuneInString", func(a []any) (any, error) { return utf8.FullRuneInString(str(a[0])), nil }, utf8.FullRuneInString),
		"DecodeRuneInString": h.fn("utf8.DecodeRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeRune": h.fn("utf8.DecodeRune", func(a []any) (any, error) {
			r, n := utf8.DecodeRune(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeLastRuneInString": h.fn("utf8.DecodeLastRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeLastRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeLastRune": h.fn("utf8.DecodeLastRune", func(a []any) (any, error) {
			r, n := utf8.DecodeLastRune(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		// gc signature: writes into the caller's []byte (through the
		// script slice's own backing so sibling views see the bytes) and
		// returns the width. A too-small p panics index-out-of-range at
		// p[width-1] before any write; out-of-range and surrogate runes
		// write RuneError's encoding.
		"EncodeRune": &runtime.BuiltinFunc{Name: "utf8.EncodeRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("utf8.EncodeRune needs 2 args, got %d", len(args))
			}
			var elems []runtime.Value
			var styp *runtime.TypeDef
			switch x := runtime.Unwrap(args[0]).(type) {
			case runtime.Nil:
				// an untyped nil encodes like an empty []byte
			case *runtime.TypedNil:
				if x.Typ == nil || x.Typ.Kind != runtime.KindSlice {
					return nil, fmt.Errorf("utf8.EncodeRune on %T", args[0])
				}
				styp = x.Typ
			default:
				s, ok := sliceOf(args[0])
				if !ok {
					return nil, fmt.Errorf("utf8.EncodeRune on %T", args[0])
				}
				elems, styp = s.Elems, s.Typ
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], rune(int64Of(goNative(args[1]))))
			if len(elems) < n {
				// gc's `_ = p[width-1]` bounds check fires first.
				panic(runtime.BoundsPanic(n-1, len(elems)))
			}
			var et *runtime.TypeDef
			if styp != nil {
				et = vc.TypeOf(vc.ElemZero(styp))
			}
			for i, b := range buf[:n] {
				a := runtime.Value(int64(b))
				if et != nil {
					// elements tag like the declared element type, the
					// same conversion an index store applies.
					if cv, err := vc.Convert(et, a); err == nil {
						a = cv
					}
				}
				elems[i] = a
			}
			return int64(n), nil
		}, Target: utf8.EncodeRune},
		"AppendRune": &runtime.BuiltinFunc{Name: "utf8.AppendRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("utf8.AppendRune needs 2 args, got %d", len(args))
			}
			// the append runs on the script slice's own backing — going
			// through goNative would copy it, losing the spare-capacity
			// sharing Go's append preserves (b[:2] sees the rune and the
			// writes through the result).
			var elems []runtime.Value
			var styp *runtime.TypeDef
			switch x := runtime.Unwrap(args[0]).(type) {
			case runtime.Nil:
				// an untyped nil appends like an empty []byte
			case *runtime.TypedNil:
				if x.Typ == nil || x.Typ.Kind != runtime.KindSlice {
					return nil, fmt.Errorf("utf8.AppendRune on %T", args[0])
				}
				styp = x.Typ
			default:
				s, ok := sliceOf(args[0])
				if !ok {
					return nil, fmt.Errorf("utf8.AppendRune on %T", args[0])
				}
				elems, styp = s.Elems, s.Typ
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], rune(int64Of(goNative(args[1]))))
			var et *runtime.TypeDef
			if styp != nil {
				et = vc.TypeOf(vc.ElemZero(styp))
			}
			add := make([]runtime.Value, n)
			for i, b := range buf[:n] {
				a := runtime.Value(int64(b))
				if et != nil {
					// elements tag like the declared element type, the
					// same conversion the append builtin applies.
					if cv, err := vc.Convert(et, a); err == nil {
						a = cv
					}
				}
				add[i] = a
			}
			return &runtime.Slice{Elems: append(elems, add...), Typ: anonSliceTyp("byte")}, nil
		}, Target: utf8.AppendRune},
		"UTFMax":    &runtime.UConst{V: constant.MakeInt64(utf8.UTFMax)},
		"RuneError": &runtime.UConst{V: constant.MakeInt64(utf8.RuneError), Rune: true},
		"RuneSelf":  &runtime.UConst{V: constant.MakeInt64(utf8.RuneSelf)},
	})
}
