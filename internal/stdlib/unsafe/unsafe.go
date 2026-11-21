// Package stdunsafe is the lazy installer for the bound "unsafe" package
// (P2-12): sizes/offsets answer from DECLARED types through minireflect,
// pointer args ride &s[i] refs so zero-copy string packing holds.
package stdunsafe

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"reflect"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, hooks minireflect.Hooks) {
	// unsafe/runtime: the fixed runtime primitives source cannot reach
	// (plan §11). Sizes are 64-bit host approximations over the boxed
	// representation; NumGoroutine is pinned to 1 — the VM is
	// single-threaded by design.
	// unsafe.StringData interns each string's byte backing so the same
	// string value resolves to the same element pointer — pointer
	// identity for a shared backing is the only guarantee unsafe offers
	// here, and it is what callers like x/tools' stringptr rely on.
	strDataBacks := map[string]*runtime.Slice{}

	uintptrTyp := &runtime.TypeDef{Name: "uintptr", Kind: runtime.KindNamedBasic}

	bind("unsafe", map[string]runtime.Value{
		// unsafe.Pointer: a named pointer to the opaque ArbitraryType.
		// Values carry it as a Named tag over the real ref, so *T <->
		// unsafe.Pointer conversions keep write-through and identity.
		// The Elem marker is unresolvable on purpose: the coercion
		// funnels reject a direct `*p`/`u = &x` like gc does, and the
		// conversion paths special-case the name before any pointee
		// check runs.
		"Pointer": &runtime.TypeDef{
			Name: "unsafe.Pointer",
			Kind: runtime.KindPointer,
			Elem: &runtime.TypeDef{Name: "unsafe.ArbitraryType", Kind: runtime.KindNamedBasic},
		},
		"Sizeof": &runtime.BuiltinFunc{Name: "unsafe.Sizeof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// gc's result is an untyped uintptr constant — the closest
			// runtime shape is an int64 tagged uintptr.
			return runtime.Tag(uintptrTyp, unsafeSizeOf(hooks, args[0], unsafeStaticTyp(hooks, args))), nil
		}},
		"Alignof": &runtime.BuiltinFunc{Name: "unsafe.Alignof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return runtime.Tag(uintptrTyp, unsafeAlignOf(hooks, args[0], unsafeStaticTyp(hooks, args))), nil
		}},
		"Offsetof": &runtime.BuiltinFunc{Name: "unsafe.Offsetof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// The compiler rewrites unsafe.Offsetof(s.f) to pass the
			// base and the field name — the selector's value alone
			// cannot name its field.
			if len(args) != 2 {
				return nil, errors.New("unsafe.Offsetof requires a field selector operand")
			}
			name, ok := unsafeStringArg(args[1])
			if !ok {
				return nil, errors.New("unsafe.Offsetof requires a field selector operand")
			}
			off, err := unsafeFieldOffset(hooks, args[0], name)
			if err != nil {
				return nil, err
			}
			return runtime.Tag(uintptrTyp, int64(off)), nil
		}},
		// Element pointers are &s[i] refs over a script slice, so the
		// StringData/String and SliceData/Slice round trips used for
		// zero-copy string packing (x/tools' event labels) hold.
		"StringData": &runtime.BuiltinFunc{Name: "unsafe.StringData", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			str, _ := runtime.Unwrap(args[0]).(string)
			if str == "" {
				// "" still yields a (zero-length) element ref: Go leaves its
				// result unspecified, and a nil would not convert to a
				// pointer type over unsafe.Pointer (label's stringptr).
				str = "\x00"
			}
			if strDataBacks[str] == nil {
				strDataBacks[str] = unsafeBytes(str)
			}
			return &runtime.IndexRef{Base: strDataBacks[str], Key: int64(0)}, nil
		}},
		"SliceData": &runtime.BuiltinFunc{Name: "unsafe.SliceData", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			sl, ok := runtime.Unwrap(args[0]).(*runtime.Slice)
			if !ok || sl.Len() == 0 && cap(sl.Elems) == 0 {
				return runtime.NIL, nil
			}
			return &runtime.IndexRef{Base: sl, Key: int64(0)}, nil
		}},
		"String": &runtime.BuiltinFunc{Name: "unsafe.String", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			elems, err := stdlib.UnsafeElems(args[0], args[1])
			if err != nil {
				return nil, err
			}
			return string(stdlib.ByteSlice(&runtime.Slice{Elems: elems})), nil
		}},
		"Slice": &runtime.BuiltinFunc{Name: "unsafe.Slice", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := int(stdlib.Int64Of(hostbridge.GoNative(args[1])))
			// a nil pointer with len 0 is legal and reports the nil slice
			// (Go panics only when len != 0) — typed nils keep their
			// element type so the result is still a []T nil.
			if tn, ok := runtime.Unwrap(args[0]).(*runtime.TypedNil); ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
				if n == 0 {
					return &runtime.TypedNil{Typ: sliceTypFromPtr(tn.Typ)}, nil
				}
				return nil, errors.New("unsafe: ptr is nil and len is not zero")
			}
			elems, err := stdlib.UnsafeElems(args[0], args[1])
			if err != nil {
				return nil, err
			}
			var typ *runtime.TypeDef
			if ref, ok := runtime.Unwrap(args[0]).(*runtime.IndexRef); ok {
				if sl := ref.Slice(); sl != nil {
					typ = sliceTypFromPtr(sl.Typ)
				}
			}
			return &runtime.Slice{Elems: elems, Typ: typ}, nil
		}},
	})
}

// unsafeSizeOf reports unsafe.Sizeof on amd64: the answer comes from the
// operand's DECLARED type — a struct's fields pad per their declared
// widths, an array's element type sizes it — never the runtime values
// slots happen to hold. Operands without a recoverable typedef (untyped
// literals, host boxes) fall back to the boxed representation's width.
// unsafeStaticTyp reads the declared typedefs the compiler attaches
// to an unsafe.Sizeof/Alignof call: args[1] is the operand's own
// static typedef (nil when the operand has no static type), args[2]
// the indexed container's for a `c[i]` operand — the operand's
// declared type is the container's element type.
func unsafeStaticTyp(hooks minireflect.Hooks, args []runtime.Value) *runtime.TypeDef {
	if len(args) > 2 {
		if ctd, ok := args[2].(*runtime.TypeDef); ok && ctd != nil {
			// parr[i] auto-derefs a pointer container: peel the stars
			// so the element typedef comes from the indexed base.
			for ctd.Kind == runtime.KindPointer {
				next, err := hooks.ElemOf(ctd)
				if err != nil || next == nil || next == ctd {
					break
				}
				ctd = next
			}
			// Only a real container yields an element type — a
			// string's is not byte in this typedef model, and a
			// non-container's own typedef is not an answer.
			container := ctd.Kind == runtime.KindSlice ||
				ctd.Kind == runtime.KindMap || ctd.Kind == runtime.KindChan
			if !container {
				switch ctd.TypeExpr().(type) {
				case *ast.ArrayType, *ast.MapType, *ast.ChanType:
					container = true
				}
			}
			if container {
				if etd, err := hooks.ElemOf(ctd); err == nil && etd != nil {
					return etd
				}
			}
		}
	}
	if len(args) > 1 {
		if td, ok := args[1].(*runtime.TypeDef); ok {
			return td
		}
	}
	return nil
}

func unsafeSizeOf(hooks minireflect.Hooks, v runtime.Value, declared *runtime.TypeDef) int64 {
	if declared != nil {
		if declared.HostNew != nil {
			// a host-bound type's layout lives in reflect — its typedef
			// has no script field list. HostNew allocates a pointer —
			// peel it to the value type.
			rt := declared.HostType()
			for rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
			return int64(rt.Size())
		}
		return int64(minireflect.SizeOf(hooks, declared))
	}
	if td := sizeDeclTyp(v); td != nil {
		return int64(minireflect.SizeOf(hooks, td))
	}
	switch x := v.(type) {
	case bool:
		return 1
	case int64, float64:
		return 8
	case complex128:
		return 16
	case string:
		return 16
	case *runtime.Slice:
		return 24
	case *runtime.Map, *runtime.Chan, *runtime.Cell:
		return 8
	case *runtime.GoValue:
		// a host box knows its real size — complex128 materializes
		// to a GoValue, pointer-shaped boxes report 8.
		if t := reflect.TypeOf(x.V); t != nil {
			return int64(t.Size())
		}
		return 8
	case *runtime.UConst:
		if mv, err := runtime.UConstNative(x); err == nil && mv != x {
			return unsafeSizeOf(hooks, mv, nil)
		}
		return 8
	case runtime.Nil, *runtime.IfaceNil:
		return 16 // interface pair
	default:
		return 8 // pointer-sized boxes
	}
}

func unsafeAlignOf(hooks minireflect.Hooks, v runtime.Value, declared *runtime.TypeDef) int64 {
	if declared != nil {
		if declared.HostNew != nil {
			rt := declared.HostType()
			for rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
			return int64(rt.Align())
		}
		return int64(minireflect.AlignOf(hooks, declared))
	}
	if td := sizeDeclTyp(v); td != nil {
		return int64(minireflect.AlignOf(hooks, td))
	}
	if n := unsafeSizeOf(hooks, v, nil); n < 8 {
		if n < 1 {
			return 1 // alignment is always at least 1
		}
		return n
	}
	return 8
}

// unsafeStringArg reads a string-valued builtin argument — the field
// name the compiler emits for unsafe.Offsetof(s.f). Named and
// cell-boxed strings unwrap like they do everywhere else.
func unsafeStringArg(v runtime.Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case *runtime.UConst:
		if x.V != nil && x.V.Kind() == constant.String {
			return constant.StringVal(x.V), true
		}
	case *runtime.Named:
		return unsafeStringArg(x.V)
	case *runtime.Cell:
		return unsafeStringArg(x.Elem)
	}
	return "", false
}

// unsafeFieldOffset reports the byte offset of a named field in a
// struct base, laid out by the fields' DECLARED types — minireflect's
// fieldOffset machinery, shared with reflect's StructField.Offset.
// (Computing offsets from the runtime values fields happen to hold
// misplaces every later field: an `any` holding an int64 read as 8
// where the declared interface pair is 16.) A host-boxed struct answers
// through reflect's real field offset instead.
func unsafeFieldOffset(hooks minireflect.Hooks, base runtime.Value, name string) (int64, error) {
	switch x := base.(type) {
	case *runtime.TypeDef:
		// a statically-resolved base arrives as its typedef — a nil
		// *Pair still offsets Pair's field. Peel the stars; a
		// non-struct typedef has no fields.
		td := x
		for td.Kind == runtime.KindPointer {
			next, err := hooks.ElemOf(td)
			if err != nil || next == nil || next == td {
				break
			}
			td = next
		}
		if td.HostNew != nil {
			// a host-bound struct's layout lives in reflect — its
			// typedef has no script field list.
			rt := td.HostType()
			for rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
			if rt.Kind() == reflect.Struct {
				if sf, ok := rt.FieldByName(name); ok {
					return int64(sf.Offset), nil
				}
			}
			return 0, fmt.Errorf("unsafe.Offsetof: %s has no field %s", rt, name)
		}
		off, err := minireflect.OffsetOf(hooks, td, name)
		if err != nil {
			return 0, err
		}
		return int64(off), nil
	case *runtime.Cell:
		return unsafeFieldOffset(hooks, x.Elem, name)
	case *runtime.Named:
		return unsafeFieldOffset(hooks, x.V, name)
	case *runtime.Struct:
		if x.Def == nil {
			break
		}
		off, err := minireflect.OffsetOf(hooks, x.Def, name)
		if err != nil {
			return 0, err
		}
		return int64(off), nil
	case *runtime.GoValue:
		rv := reflect.ValueOf(x.V)
		for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
			rv = rv.Elem()
		}
		if rv.IsValid() && rv.Kind() == reflect.Struct {
			if sf, ok := rv.Type().FieldByName(name); ok {
				return int64(sf.Offset), nil
			}
			return 0, fmt.Errorf("unsafe.Offsetof: %s has no field %s", rv.Type(), name)
		}
	}
	return 0, fmt.Errorf("unsafe.Offsetof is not supported for %T", base)
}

// unsafeBytes copies str into a script []byte backing for StringData —
// the shared shape every string->[]byte projection produces
// (runtime.BytesOf).
func unsafeBytes(str string) *runtime.Slice {
	return runtime.BytesOf(str)
}

// sizeDeclTyp is the operand's declared type for unsafe.Sizeof/Alignof
// when the operand itself carries one (an untyped literal, a host box
// reports nil and the caller falls back to runtime-value widths).
func sizeDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch v.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		// a pointer value: the operand's declared type is *T where T
		// is the pointee's declared type — the tag stamped on the box
		// belongs under the star (OpBox/OpLocalRef stamp the POINTEE's
		// typedef), it never answers the pointee's layout itself.
		var elem *runtime.TypeDef
		if c, ok := v.(*runtime.Cell); ok {
			elem = c.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: elem}
	}
	return stdlib.ValueDeclTyp(v)
}

// sliceTypFromPtr maps a pointer/array/slice typedef to the unnamed
// []E typedef unsafe.Slice's result carries — never the named slice or
// the [N]E array the pointer was taken from.
func sliceTypFromPtr(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	switch x := td.TypeExpr().(type) {
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.X}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.Elt}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	}
	return td
}
