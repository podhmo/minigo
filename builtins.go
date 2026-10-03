package minigo

import (
	"fmt"
	"go/constant"
	"os"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// builtins returns the predeclared universe: builtin functions and builtin
// type names (as *TypeDef values so `int(x)` is a normal conversion call).
func builtins(e *Engine) *runtime.Env {
	env := runtime.NewEnv()

	bf := func(name string, fn func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error)) {
		env.Set(name, &runtime.BuiltinFunc{Name: name, Fn: fn})
	}

	bf("len", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		x := args[0]
		if c, ok := x.(*runtime.Cell); ok {
			x = c.Elem
		}
		if n, ok := nilArrLen(v, x); ok {
			return n, nil
		}
		return lenOf(x)
	})
	bf("cap", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		x := args[0]
		if c, ok := x.(*runtime.Cell); ok {
			x = c.Elem
		}
		if n, ok := nilArrLen(v, x); ok {
			return n, nil
		}
		return capOf(x)
	})
	bf("append", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
		// an untyped-constant element converts through the declared
		// element type — append(b, 'i') on []byte is Go's constant
		// conversion; without a declared type it takes its default.
		var et runtime.Value
		if rtyp != nil {
			et = v.TypeOf(v.ElemZero(rtyp))
		}
		add := make([]runtime.Value, len(args)-1)
		for i, a := range args[1:] {
			if u, ok := a.(*runtime.UConst); ok {
				if et != nil {
					cv, err := v.Call(et, []runtime.Value{a})
					if err != nil {
						return nil, err
					}
					a = cv
				} else {
					nv, err := uconstNative(u)
					if err != nil {
						return nil, err
					}
					if cv, isC := nv.(complex128); isC {
						nv = &runtime.GoValue{V: cv}
					}
					a = nv
				}
			} else if et != nil {
				// a materialized scalar converts like the constant it
				// came from — append(f, 0) on []float64 stores 0.0.
				switch a.(type) {
				case int64, int, uint64, rune, float64:
					if cv, err := v.Call(et, []runtime.Value{a}); err == nil {
						a = cv
					}
				}
			}
			add[i] = a
		}
		if s == nil && len(add) == 0 && rtyp != nil {
			// appending nothing to a nil slice keeps the nil — Go's
			// append(nilSlice) is still nil, not an empty slice.
			res := runtime.Value(&runtime.TypedNil{Typ: rtyp})
			if tag != nil {
				return runtime.Tag(tag, res), nil
			}
			return res, nil
		}
		// appending onto the backing array itself keeps Go's sharing
		// semantics: within spare capacity the result aliases the same
		// storage, past it the host append allocates a fresh array.
		res := &runtime.Slice{Elems: append(elems, add...), Typ: rtyp}
		if tag != nil {
			return runtime.Tag(tag, res), nil // append keeps the declared type
		}
		return res, nil
	})
	bf("copy", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		sliceOf := func(x runtime.Value) (*runtime.Slice, bool) {
			switch s := x.(type) {
			case *runtime.Named:
				if ss, ok := s.V.(*runtime.Slice); ok {
					return ss, true
				}
			case *runtime.Slice:
				return s, true
			case *runtime.Cell:
				if ss, ok := s.Elem.(*runtime.Slice); ok {
					return ss, true
				}
			case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
				return nil, true // nil slice: copies 0 elements
			}
			return nil, false
		}
		dst, ok1 := sliceOf(args[0])
		src, ok2 := sliceOf(args[1])
		if src == nil && ok1 {
			// copy(dst, "str"): a string source copies into []byte.
			if s, ok := runtime.Unwrap(args[1]).(string); ok {
				src = &runtime.Slice{Elems: make([]runtime.Value, len(s)), Typ: dst.Typ}
				for i := 0; i < len(s); i++ {
					src.Elems[i] = int64(s[i])
				}
				ok2 = true
			}
		}
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("copy on non-slice")
		}
		if dst == nil || src == nil {
			return int64(0), nil
		}
		n := copy(dst.Elems, src.Elems)
		return int64(n), nil
	})
	bf("delete", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		kv := runtime.Unwrap(args[1])
		if u, ok := kv.(*runtime.UConst); ok {
			nv, err := uconstNative(u)
			if err != nil {
				return nil, err
			}
			if cv, isC := nv.(complex128); isC {
				nv = &runtime.GoValue{V: cv}
			}
			kv = nv
		}
		drop := func(m *runtime.Map) {
			m.Delete(kv)
		}
		switch m := args[0].(type) {
		case *runtime.Named:
			if mm, ok := m.V.(*runtime.Map); ok {
				drop(mm)
				return runtime.NIL, nil
			}
			return nil, fmt.Errorf("delete on named %s", m.Typ.Name)
		case *runtime.Map:
			drop(m)
		case *runtime.Cell:
			if mm, ok := m.Elem.(*runtime.Map); ok {
				drop(mm)
				return runtime.NIL, nil
			}
			return nil, fmt.Errorf("delete on %T", args[0])
		case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
			// delete on a nil map is a no-op
		default:
			return nil, fmt.Errorf("delete on %T", args[0])
		}
		return runtime.NIL, nil
	})
	bf("make", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		td, ok := args[0].(*runtime.TypeDef)
		if !ok {
			return nil, fmt.Errorf("make of non-type %T", args[0])
		}
		switch td.Kind {
		case runtime.KindSlice:
			n := int64(0)
			if len(args) > 1 {
				n = int64Of(runtime.Unwrap(args[1]))
			}
			cap := n
			if len(args) > 2 {
				cap = int64Of(runtime.Unwrap(args[2]))
			}
			// Go's makeslice panics once len exceeds its maxAlloc bound —
			// without a check the host make() would die as a real OOM
			// instead of a script panic. Elements are 16-byte Values, so
			// the bound lands below Go's, which is fine: the makeslice.go
			// corpus only asks for panics far above the practical limit.
			const maxSliceElems = 1 << 32
			if n < 0 || n > maxSliceElems {
				panic(runtime.MakeslicePanic("len"))
			}
			if cap < n || cap > maxSliceElems {
				panic(runtime.MakeslicePanic("cap"))
			}
			el := make([]runtime.Value, n, cap)
			zero := runtime.Value(runtime.NIL)
			if ez, ok := v.(interface {
				ElemZero(*runtime.TypeDef) runtime.Value
			}); ok {
				zero = ez.ElemZero(td)
			}
			for i := range el {
				el[i] = zero
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		case runtime.KindMap:
			return &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}, Typ: td}, nil
		case runtime.KindChan:
			buf := int64(0)
			if len(args) > 1 {
				buf, _ = runtime.Unwrap(args[1]).(int64)
			}
			return &runtime.Chan{C: make(chan runtime.Value, int(buf)), Typ: td}, nil
		default:
			return nil, fmt.Errorf("make of kind %d", td.Kind)
		}
	})
	bf("new", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("new expects exactly one argument")
		}
		if td, ok := args[0].(*runtime.TypeDef); ok {
			return &runtime.Cell{Elem: v.Zero(td), Typ: td}, nil
		}
		// Go 1.26: new(expr) allocates and initializes to a copy of the
		// expression's value — the cell is typed by the value's own
		// typedef rather than a declared type form.
		if _, isNil := args[0].(runtime.Nil); isNil {
			return nil, fmt.Errorf("cannot use nil as type or value in new")
		}
		return &runtime.Cell{Elem: v.Copy(args[0]), Typ: v.TypeOf(args[0])}, nil
	})
	bf("close", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		var ch *runtime.Chan
		switch x := args[0].(type) {
		case *runtime.Chan:
			ch = x
		case *runtime.Named:
			ch, _ = x.V.(*runtime.Chan)
		case *runtime.Cell:
			ch, _ = x.Elem.(*runtime.Chan)
		case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
			panic(runtime.CloseNilChanPanic())
		default:
			return nil, fmt.Errorf("close of non-channel %T", args[0])
		}
		if ch == nil || ch.C == nil {
			panic(runtime.CloseNilChanPanic())
		}
		// a second close — or a send past close — panics via the host
		// channel itself, which the VM surfaces as a script panic
		close(ch.C)
		return runtime.NIL, nil
	})
	bf("panic", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		// panic(nil) carries a *PanicNilError since Go 1.21 — recover()
		// reports a non-nil value. A typed nil stays a typed nil (the
		// interface argument is non-nil).
		switch args[0].(type) {
		case runtime.Nil, *runtime.IfaceNil:
			panic(&runtime.Panic{Value: &runtime.PanicNilError{}})
		}
		panic(&runtime.Panic{Value: args[0]})
	})
	bf("recover", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return v.Recover(), nil
	})
	bf("complex", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("complex expects two arguments")
		}
		re, ok1 := argFloat(args[0])
		im, ok2 := argFloat(args[1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("complex on non-numeric")
		}
		// float32 operands yield a complex64 (the narrower side wins —
		// Go types the call by its arguments' type, not float64).
		if namedFloat32(args[0]) || namedFloat32(args[1]) {
			return &runtime.GoValue{V: complex64(complex(re, im))}, nil
		}
		return &runtime.GoValue{V: complex(re, im)}, nil
	})
	bf("real", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if u, ok := args[0].(*runtime.UConst); ok && numericConst(u) {
			// a constant stays a constant: real(1+2i) is the untyped
			// float 1, still materializable to any numeric target.
			return &runtime.UConst{V: constant.Real(u.V)}, nil
		}
		cv, w, ok := argComplex(args[0])
		if !ok {
			return nil, fmt.Errorf("real of %T", args[0])
		}
		if w == 64 {
			return runtime.Tag(&runtime.TypeDef{Name: "float32", Kind: runtime.KindNamedBasic}, float64(real(complex64(cv)))), nil
		}
		return real(cv), nil
	})
	bf("imag", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if u, ok := args[0].(*runtime.UConst); ok && numericConst(u) {
			return &runtime.UConst{V: constant.Imag(u.V)}, nil
		}
		cv, w, ok := argComplex(args[0])
		if !ok {
			return nil, fmt.Errorf("imag of %T", args[0])
		}
		if w == 64 {
			return runtime.Tag(&runtime.TypeDef{Name: "float32", Kind: runtime.KindNamedBasic}, float64(imag(complex64(cv)))), nil
		}
		return imag(cv), nil
	})
	bf("min", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("min needs at least one argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if orderedLess(a, m) {
				m = a
			}
		}
		return m, nil
	})
	bf("max", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("max needs at least one argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if orderedLess(m, a) {
				m = a
			}
		}
		return m, nil
	})
	bf("clear", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		switch m := runtime.Unwrap(args[0]).(type) {
		case *runtime.Map:
			m.Clear()
		case *runtime.Slice:
			zero := runtime.Value(runtime.NIL)
			if ez, ok := v.(interface {
				ElemZero(*runtime.TypeDef) runtime.Value
			}); ok {
				zero = ez.ElemZero(m.Typ)
			}
			for i := range m.Elems {
				m.Elems[i] = zero
			}
		default:
			return nil, fmt.Errorf("clear of %T", args[0])
		}
		return runtime.NIL, nil
	})
	// print/println write to stderr like Go's builtins do; print spaces
	// only between adjacent non-string operands.
	bf("print", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		for i, a := range args {
			if i > 0 {
				_, prevStr := runtime.Unwrap(args[i-1]).(string)
				_, curStr := runtime.Unwrap(a).(string)
				if !prevStr && !curStr {
					fmt.Fprint(os.Stderr, " ")
				}
			}
			fmt.Fprint(os.Stderr, display(a))
		}
		return runtime.NIL, nil
	})
	bf("println", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		parts := make([]any, len(args))
		for i, a := range args {
			parts[i] = display(a)
		}
		fmt.Fprintln(os.Stderr, parts...)
		return runtime.NIL, nil
	})

	// builtin type names
	for _, n := range []string{
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"string", "bool", "byte", "rune",
	} {
		env.Set(n, &runtime.TypeDef{Name: n, Kind: runtime.KindNamedBasic})
	}
	// any / error: predeclared interface typedefs (assertion + decl targets)
	env.Set("any", &runtime.TypeDef{Name: "any", Kind: runtime.KindInterface})
	env.Set("error", &runtime.TypeDef{Name: "error", Kind: runtime.KindInterface, MReqs: []string{"Error"}})
	return env
}

// nilArrLen folds len/cap of a nil *[N]T to its constant N — Go's len
// of an array-typed operand never evaluates the operand.
func nilArrLen(v runtime.VMCaller, x runtime.Value) (runtime.Value, bool) {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	tn, ok := x.(*runtime.TypedNil)
	if !ok || tn.Typ == nil || tn.Typ.Kind != runtime.KindPointer {
		return nil, false
	}
	at := runtime.PtrArrayType(tn.Typ)
	if at == nil {
		return nil, false
	}
	if n, ok := v.ArrayLenOf(&runtime.TypeDef{Anon: at, Pkg: tn.Typ.Pkg, File: tn.Typ.File}); ok {
		return n, true
	}
	return nil, false
}

func lenOf(v runtime.Value) (runtime.Value, error) {
	switch x := v.(type) {
	case *runtime.Named:
		return lenOf(x.V)
	case *runtime.Slice:
		return int64(len(x.Elems)), nil
	case *runtime.Map:
		return int64(x.Len()), nil
	case *runtime.Chan:
		return int64(len(x.C)), nil
	case string:
		return int64(len(x)), nil
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return int64(0), nil // len(nil slice/map/chan) == 0
	default:
		return nil, fmt.Errorf("len of %T", v)
	}
}

func capOf(v runtime.Value) (runtime.Value, error) {
	switch x := v.(type) {
	case *runtime.Named:
		return capOf(x.V)
	case *runtime.Cell:
		return capOf(x.Elem)
	case *runtime.Slice:
		return int64(cap(x.Elems)), nil
	case *runtime.Chan:
		return int64(cap(x.C)), nil
	default:
		return lenOf(v)
	}
}

// orderedLess implements the builtin min/max comparison over ordered
// values: ints, floats and strings compare naturally; a float NaN is
// never less (so a NaN argument wins the fold, matching Go's
// "NaN propagates" rule). Named values compare by their underlying.
func orderedLess(a, b runtime.Value) bool {
	a = runtime.Unwrap(a)
	b = runtime.Unwrap(b)
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return x < y
		case float64:
			return float64(x) < y
		}
	case float64:
		switch y := b.(type) {
		case int64:
			return x < float64(y)
		case float64:
			return x < y
		}
	case string:
		if y, ok := b.(string); ok {
			return x < y
		}
	}
	return false
}

// argFloat reads a builtin argument as float64 (ints promote).
func argFloat(x runtime.Value) (float64, bool) {
	if u, ok := x.(*runtime.UConst); ok {
		f, _ := constant.Float64Val(u.V)
		return f, true
	}
	switch n := runtime.Unwrap(x).(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// argComplex reads a builtin argument as complex128 with its width —
// ints and floats promote to complex128.
func argComplex(x runtime.Value) (complex128, int, bool) {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	switch n := x.(type) {
	case *runtime.GoValue:
		switch c := n.V.(type) {
		case complex64:
			return complex128(c), 64, true
		case complex128:
			return c, 128, true
		}
	}
	if f, ok := argFloat(x); ok {
		return complex(f, 0), 128, true
	}
	return 0, 0, false
}

// numericConst reports whether a constant is numeric — real/imag on a
// non-complex-kind constant is Go's compile-time reject.
func numericConst(u *runtime.UConst) bool {
	switch u.V.Kind() {
	case constant.Int, constant.Float, constant.Complex:
		return true
	}
	return false
}

// namedFloat32 reports whether x carries a float32 typedef.
func namedFloat32(x runtime.Value) bool {
	n, ok := x.(*runtime.Named)
	return ok && float32Tag(n.Typ)
}

func display(v runtime.Value) any {
	switch x := v.(type) {
	case runtime.Nil, *runtime.IfaceNil:
		// print/println spell a nil interface like the runtime's
		// printeface: the (type,value) pair of nil pointers.
		return "(0x0,0x0)"
	case *runtime.TypedNil:
		// print/println spell a nil pointer-ish value like the runtime:
		// 0x0 for pointers/chans/maps/funcs, [0/0]0x0 for a nil slice.
		if x.Typ != nil {
			switch x.Typ.Kind {
			case runtime.KindSlice:
				return "[0/0]0x0"
			case runtime.KindPointer, runtime.KindChan, runtime.KindMap, runtime.KindFunc:
				return "0x0"
			}
		}
		return nil
	case *runtime.UConst:
		nv, err := uconstNative(x)
		if err != nil {
			return err.Error()
		}
		return display(nv)
	case *runtime.Named:
		return display(x.V)
	case *runtime.Cell:
		return display(x.Elem)
	case *runtime.Slice:
		parts := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			parts[i] = display(e)
		}
		return parts
	case *runtime.Struct:
		parts := make([]any, len(x.Fields))
		for i, e := range x.Fields {
			parts[i] = display(e)
		}
		return fmt.Sprintf("{%v}", joinDisplay(parts))
	case *runtime.Map:
		var sb strings.Builder
		sb.WriteString("map[")
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(fmt.Sprintf("%v:%v", display(k), display(e)))
		}
		sb.WriteByte(']')
		return sb.String()
	case *runtime.GoValue:
		// a host box prints its payload — complex128 as (9+10i),
		// not the wrapper's &{...} pointer spelling.
		return x.V
	default:
		return x
	}
}

// Format renders a runtime value for host-side output — the CLI's run
// result and embedding tools print it like Go's %v would.
func Format(v runtime.Value) any {
	return display(v)
}

func joinDisplay(parts []any) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(fmt.Sprint(p))
	}
	return sb.String()
}
