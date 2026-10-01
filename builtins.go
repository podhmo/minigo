package minigo

import (
	"fmt"
	"io"

	"github.com/podhmo/minigo/runtime"
)

// builtins returns the predeclared universe: builtin functions and builtin
// type names (as *TypeDef values so `int(x)` is a normal conversion call).
// print/println write to the engine's configured output.
func builtins(e *Engine) *runtime.Env {
	env := runtime.NewEnv()

	out := func() io.Writer {
		if e.out == nil {
			return io.Discard
		}
		return e.out
	}

	bf := func(name string, fn func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error)) {
		env.Set(name, &runtime.BuiltinFunc{Name: name, Fn: fn})
	}

	bf("len", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		switch x := args[0].(type) {
		case *runtime.Cell:
			return lenOf(x.Elem)
		default:
			return lenOf(x)
		}
	})
	bf("cap", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return capOf(args[0])
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
		res := &runtime.Slice{Elems: append(append([]runtime.Value{}, elems...), args[1:]...), Typ: rtyp}
		if tag != nil {
			return &runtime.Named{Typ: tag, V: res}, nil // append keeps the declared type
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
		key := runtime.Unwrap(args[1])
		switch m := args[0].(type) {
		case *runtime.Named:
			if mm, ok := m.V.(*runtime.Map); ok {
				delete(mm.Pairs, key)
				return runtime.NIL, nil
			}
			return nil, fmt.Errorf("delete on named %s", m.Typ.Name)
		case *runtime.Map:
			delete(m.Pairs, key)
		case *runtime.Cell:
			if mm, ok := m.Elem.(*runtime.Map); ok {
				delete(mm.Pairs, key)
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
				n, _ = runtime.Unwrap(args[1]).(int64)
			}
			cap := n
			if len(args) > 2 {
				cap, _ = runtime.Unwrap(args[2]).(int64)
			}
			el := make([]runtime.Value, n, cap)
			for i := range el {
				el[i] = runtime.NIL
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
			panic(&runtime.Panic{Value: "close of nil channel"})
		default:
			return nil, fmt.Errorf("close of non-channel %T", args[0])
		}
		if ch == nil || ch.C == nil {
			panic(&runtime.Panic{Value: "close of nil channel"})
		}
		// a second close — or a send past close — panics via the host
		// channel itself, which the VM surfaces as a script panic
		close(ch.C)
		return runtime.NIL, nil
	})
	bf("panic", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		panic(&runtime.Panic{Value: args[0]})
	})
	bf("recover", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return v.Recover(), nil
	})
	bf("print", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		for _, a := range args {
			fmt.Fprint(out(), display(a))
		}
		return runtime.NIL, nil
	})
	bf("println", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		parts := make([]any, len(args))
		for i, a := range args {
			parts[i] = display(a)
		}
		fmt.Fprintln(out(), parts...)
		return runtime.NIL, nil
	})

	// builtin type names
	for _, n := range []string{
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "string", "bool", "byte", "rune",
	} {
		env.Set(n, &runtime.TypeDef{Name: n, Kind: runtime.KindNamedBasic})
	}
	// any / error: predeclared interface typedefs (assertion + decl targets)
	env.Set("any", &runtime.TypeDef{Name: "any", Kind: runtime.KindInterface})
	env.Set("error", &runtime.TypeDef{Name: "error", Kind: runtime.KindInterface, MReqs: []string{"Error"}})
	return env
}

func lenOf(v runtime.Value) (runtime.Value, error) {
	switch x := v.(type) {
	case *runtime.Named:
		return lenOf(x.V)
	case *runtime.Slice:
		return int64(len(x.Elems)), nil
	case *runtime.Map:
		return int64(len(x.Pairs)), nil
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
	case *runtime.Slice:
		return int64(cap(x.Elems)), nil
	case *runtime.Chan:
		return int64(cap(x.C)), nil
	default:
		return lenOf(v)
	}
}

func display(v runtime.Value) any {
	switch x := v.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return nil
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
		return fmt.Sprintf("%s%+v", x.Def.Name, x.Fields)
	default:
		return x
	}
}
