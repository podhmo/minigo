package minigo

import (
	"fmt"
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
		switch x := args[0].(type) {
		case *runtime.Cell:
			return lenOf(x.Elem)
		default:
			return lenOf(x)
		}
	})
	bf("cap", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		switch x := args[0].(type) {
		case *runtime.Cell:
			return capOf(x.Elem)
		default:
			return capOf(x)
		}
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
		// appending onto the backing array itself keeps Go's sharing
		// semantics: within spare capacity the result aliases the same
		// storage, past it the host append allocates a fresh array.
		res := &runtime.Slice{Elems: append(elems, args[1:]...), Typ: rtyp}
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
		key := runtime.CanonicalKey(runtime.Unwrap(args[1]))
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
			m.Pairs = map[runtime.Value]runtime.Value{}
			m.Order = nil
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
		parts := make([]any, len(x.Fields))
		for i, e := range x.Fields {
			parts[i] = display(e)
		}
		return fmt.Sprintf("{%v}", joinDisplay(parts))
	case *runtime.Map:
		var sb strings.Builder
		sb.WriteString("map[")
		for i, k := range x.Order {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(fmt.Sprintf("%v:%v", display(k), display(x.Pairs[runtime.CanonicalKey(k)])))
		}
		sb.WriteByte(']')
		return sb.String()
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
