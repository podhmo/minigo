package minigo

import (
	"fmt"
	"unicode/utf8"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installUnicodeUtf8(h *hostHelpers) {
	tbl := unicodeUtf8GenEntries()
	// gc signature: writes into the caller's []byte (through the
	// script slice's own backing so sibling views see the bytes) and
	// returns the width. A too-small p panics index-out-of-range at
	// p[width-1] before any write; out-of-range and surrogate runes
	// write RuneError's encoding.
	tbl["EncodeRune"] = &runtime.BuiltinFunc{Name: "utf8.EncodeRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
	}, Target: utf8.EncodeRune}
	tbl["AppendRune"] = &runtime.BuiltinFunc{Name: "utf8.AppendRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
	}, Target: utf8.AppendRune}
	e.bindBound("unicode/utf8", tbl)
}
