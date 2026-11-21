package builtin

import (
	"fmt"
	"slices"

	"github.com/podhmo/minigo/runtime"
)

func appendFn(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if _, plain := args[0].(*runtime.Slice); !plain {
		if base, ok := hostSliceBase(args[0]); ok {
			return appendHostSlice(v, base, args[1:])
		}
	}
	var s *runtime.Slice
	var tag *runtime.TypeDef
	var rtyp *runtime.TypeDef
	switch x := args[0].(type) {
	case *runtime.Named:
		tag = x.Typ
		s, _ = x.V.(*runtime.Slice)
		if s == nil {
			// a Named nil slice appends fine, keeping the declared tag
			switch tn := x.V.(type) {
			case *runtime.TypedNil:
				rtyp = tn.Typ
			case *runtime.IfaceNil:
				rtyp = tn.Typ
			default:
				return nil, fmt.Errorf("append on named %s", x.Typ.Name)
			}
		}
	case *runtime.Slice:
		s = x
	case *runtime.Cell:
		s, _ = x.Elem.(*runtime.Slice)
	case *runtime.IfaceNil:
		rtyp = x.Typ
	case *runtime.TypedNil:
		rtyp = x.Typ
	case runtime.Nil:
	default:
		return nil, fmt.Errorf("append on %T", args[0])
	}
	var elems []runtime.Value
	if s != nil {
		elems = s.Elems
		rtyp = s.Typ
	}
	// a trailing `xs...` operand rides as one lazy marker when its
	// slice is virtual — a real spread was already expanded into
	// args by popArgs.
	// Forwards keeps a trailing `xs...` as the Spread marker — the
	// marker also tells the growth rule apart from gc's individual
	// element args, which can take the 32-byte stack-buffer cap.
	var spread *runtime.Slice
	var spreadElems []runtime.Value
	hadSpread := false
	if len(args) > 0 {
		if sp, ok := args[len(args)-1].(*runtime.Spread); ok {
			args = args[:len(args)-1]
			hadSpread = true
			if sp.S.Virtual() {
				spread = sp.S
			} else {
				spreadElems = sp.S.Elems
			}
		}
	}
	// an untyped-constant element converts through the declared
	// element type — append(b, 'i') on []byte is Go's constant
	// conversion; without a declared type it takes its default.
	// et resolves lazily: ElemZero builds a full zero (a struct
	// element walks every field), and a typed composite arg never
	// consults it, so appending structs stays off the builder.
	var et *runtime.TypeDef
	etDone := false
	elemTyp := func() *runtime.TypeDef {
		if !etDone {
			etDone = true
			if rtyp != nil {
				et = v.TypeOf(v.ElemZero(rtyp))
			}
		}
		return et
	}
	add := make([]runtime.Value, 0, len(args)-1+len(spreadElems))
	allArgs := args[1:]
	if len(spreadElems) > 0 {
		allArgs = append(slices.Clone(allArgs), spreadElems...)
	}
	for i := 0; i < len(allArgs); i++ {
		a := allArgs[i]
		if u, ok := a.(*runtime.UConst); ok {
			if elemTyp() != nil {
				cv, err := v.Convert(et, a)
				if err != nil {
					return nil, err
				}
				a = cv
			} else {
				nv, err := runtime.UConstNative(u)
				if err != nil {
					return nil, err
				}
				if cv, isC := nv.(complex128); isC {
					nv = &runtime.GoValue{V: cv}
				}
				a = nv
			}
		} else {
			// the element typedef is resolved only for an arg
			// that can need converting — a typed composite arg
			// (struct, slice, Named) never asks.
			switch a.(type) {
			case int64, int, uint64, rune, float64:
				// a materialized scalar converts like the constant it
				// came from — append(f, 0) on []float64 stores 0.0.
				if et := elemTyp(); et != nil {
					if cv, err := v.Convert(et, a); err == nil {
						a = cv
					}
				}
			case runtime.Nil:
				// untyped nil takes the declared element
				// type's zero — append(s, nil) on []List
				// stores List(nil), not a bare NIL that
				// panics on member select or prints
				// <nil> inside the slice.
				if et := elemTyp(); et != nil {
					cv, err := v.Convert(et, a)
					if err != nil {
						return nil, err
					}
					a = cv
				}
			}
		}
		add = append(add, v.Copy(a))
	}
	if s == nil && len(add) == 0 && rtyp != nil && spread == nil {
		// appending nothing to a nil slice keeps the nil — Go's
		// append(nilSlice) is still nil, not an empty slice.
		res := runtime.Value(&runtime.TypedNil{Typ: rtyp})
		if tag != nil {
			return runtime.Tag(tag, res), nil
		}
		return res, nil
	}
	// a virtual zero-size slice stays virtual — and so does a real
	// one that a virtual spread lands on: the appended elements are
	// all the same single value the type already vends.
	if (s != nil && s.Virtual()) || spread != nil {
		var dstLen, dstCap int64
		var zero runtime.Value
		if s != nil {
			// a real dst keeps its element COUNT in the virtual
			// result — every element is the type's single
			// zero-size value, so the logical length suffices.
			dstLen, dstCap, zero = s.Len(), s.Cap(), s.Zero
		}
		if spread != nil {
			if zero == nil {
				zero = spread.Zero
			}
			if rtyp == nil {
				rtyp = spread.Typ
			}
		}
		// the len overflow panics like Go's growslice; growth picks
		// up exactly the needed length (0-byte elements round to the
		// request, not to a doubling), while spare capacity stays.
		nn := dstLen + int64(len(add))
		if spread != nil {
			nn += spread.Len()
		}
		if nn < 0 {
			panic(runtime.RuntimePanic("growslice: len out of range"))
		}
		nc := dstCap
		if nn > nc {
			nc = nn
		}
		res := &runtime.Slice{N: nn, CapN: nc, Zero: zero, Typ: rtyp}
		if tag != nil {
			return runtime.Tag(tag, res), nil
		}
		return res, nil
	}
	// appending onto the backing array itself keeps Go's sharing
	// semantics: within spare capacity the result aliases the same
	// storage. Past it gc's growslice rounds the new capacity by
	// the SCRIPT element type's size (a byte is 1 byte, not the
	// host slot's 16) plus a 32-byte stack buffer for a small
	// non-spread append on an empty slice.
	var elems2 []runtime.Value
	newLen := len(elems) + len(add)
	grew := newLen > cap(elems)
	if grew {
		es, noscan := appendElemSize(v, rtyp, elemTyp)
		tc := runtime.GrowSliceCap(newLen, len(elems), cap(elems), es, noscan, hadSpread)
		elems2 = make([]runtime.Value, newLen, tc)
		copy(elems2, elems)
		copy(elems2[len(elems):], add)
	} else {
		elems2 = append(elems, add...)
	}
	if grew && rtyp != nil {
		// the fresh backing array holds element copies, like gc's
		// growslice memcpy: a struct element becomes a fresh value,
		// so `b[0].X = v` on the grown slice cannot alias the old
		// array's struct. Copy() shares scalars, slices, maps and
		// pointers — the same as copying a Go slice header.
		for i := range elems {
			elems2[i] = v.Copy(elems2[i])
		}
		{
			zero := v.ElemZero(rtyp)
			if runtime.IsZeroSizeValue(zero) {
				// for a zero-size element Go's growslice never
				// doubles — cap is exactly the new length — while
				// the host []Value grew by its own rule. Clip the
				// spare so cap() agrees.
				elems2 = elems2[:len(elems2):len(elems2)]
			} else {
				// a fresh backing array's spare capacity holds
				// zeros, as growslice clears it: `s[:cap(s)]`
				// must not expose absent values.
				spare := elems2[len(elems2):cap(elems2)]
				for i := range spare {
					spare[i] = v.Copy(zero)
				}
			}
		}
	}
	res := &runtime.Slice{Elems: elems2, Typ: rtyp}
	if tag != nil {
		return runtime.Tag(tag, res), nil // append keeps the declared type
	}
	return res, nil
}
