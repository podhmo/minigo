package stdfmt

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdruntime "github.com/podhmo/minigo/internal/stdlib/goruntime"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	stdjson "github.com/podhmo/minigo/internal/stdlib/json"
	stdtime "github.com/podhmo/minigo/internal/stdlib/time"
	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters, out func() io.Writer) {
	bind("fmt", map[string]runtime.Value{
		// fmt's interfaces are needed as types by interpreted sources that
		// reflect on them or assert (e.g. template's fmt.Stringer probes,
		// math/big's compile-time fmt.Scanner assertion).
		// Stringer/GoStringer carry their requirement as a real interface
		// AST (same pattern as `error`) so the facade's signature
		// machinery can report NumMethod/Method/MethodByName and check
		// Implements against `String() string`.
		"Stringer": &runtime.TypeDef{
			Name: "fmt.Stringer", Kind: runtime.KindInterface, MReqs: []string{"String"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "String"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"GoStringer": &runtime.TypeDef{
			Name: "fmt.GoStringer", Kind: runtime.KindInterface, MReqs: []string{"GoString"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "GoString"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"Formatter": &runtime.TypeDef{Name: "fmt.Formatter", Kind: runtime.KindInterface, MReqs: []string{"Format"}},
		"Scanner":   &runtime.TypeDef{Name: "fmt.Scanner", Kind: runtime.KindInterface, MReqs: []string{"Scan"}},
		"State":     &runtime.TypeDef{Name: "fmt.State", Kind: runtime.KindInterface, MReqs: []string{"Write", "Width", "Precision", "Flag"}},
		// Print/Fprint/Sprint need the flat script args: gc's operand
		// spacing reads the reflect Kind, so a named type over string
		// (json.Number) suppresses the space — the host-shaped args hide
		// that inside fmtValue.
		"Print": pffn("fmt.Print", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return stdlib.RetErr(fprintOperands(out(), a, flat))
		}, fmt.Print),
		"Println": ffn("fmt.Println", -1, 0, func(a []any) (any, error) { return stdlib.RetErr(fmt.Fprintln(out(), a...)) }, fmt.Println),
		"Printf": ffn("fmt.Printf", 0, 1, func(a []any) (any, error) {
			return stdlib.RetErr(fmt.Fprintf(out(), stdlib.Str(a[0]), a[1:]...))
		}),
		// Fprint* take an explicit writer — os.Stdout/os.Stderr arrive as
		// GoValue (unwrapped by fmtArg to the native *os.File).
		"Fprint": pffn("fmt.Fprint", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fprintOperands(w, a[1:], flat[1:]))
		}),
		"Fprintf": vffn("fmt.Fprintf", 1, 2, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fmt.Fprintf(w, stdlib.Str(a[1]), a[2:]...))
		}),
		"Fprintln": vffn("fmt.Fprintln", -1, 1, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fmt.Fprintln(w, a[1:]...))
		}),
		"Sprint": pffn("fmt.Sprint", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a, flat); err != nil {
				return nil, err
			}
			return buf.String(), nil
		}, fmt.Sprint),
		"Sprintln": ffn("fmt.Sprintln", -1, 0, func(a []any) (any, error) { return fmt.Sprintln(a...), nil }, fmt.Sprintln),
		"Sprintf": ffn("fmt.Sprintf", 0, 1, func(a []any) (any, error) {
			return fmt.Sprintf(stdlib.Str(a[0]), a[1:]...), nil
		}, fmt.Sprintf),
		// The Append family formats like Sprint/Sprintf/Sprintln onto a
		// caller's byte slice. The buffer arg is read from flat (the raw
		// script value): a[i] holds fmtValue boxes for composite args,
		// which byteSlice cannot see through. Source-interpreted packages
		// reach these through e.g. log's output(), which formats via
		// fmt.Appendln.
		"Append": pffn("fmt.Append", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a[1:], flat[1:]); err != nil {
				return nil, err
			}
			return append(stdlib.ByteSlice(flat[0]), buf.Bytes()...), nil
		}, fmt.Append),
		"Appendf": wffn("fmt.Appendf", 1, 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendf(stdlib.ByteSlice(flat[0]), stdlib.Str(a[1]), a[2:]...), nil
		}, fmt.Appendf),
		"Appendln": pffn("fmt.Appendln", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendln(stdlib.ByteSlice(flat[0]), a[1:]...), nil
		}, fmt.Appendln),
		// The Scan family's out-params arrive as script refs; scanFn
		// mirrors each into a host var of the pointee's type, scans, and
		// writes results back through the ref (parse/node.go's
		// complex-literal path needs Sscan).
		"Sscan": scanFn("fmt.Sscan", 1, nil, fmt.Sscan, func(head, ptrs []any) (int, error) { return fmt.Sscan(stdlib.Str(head[0]), ptrs...) }),
		"Sscanf": scanFn("fmt.Sscanf", 2, nil, fmt.Sscanf, func(head, ptrs []any) (int, error) {
			return fmt.Sscanf(stdlib.Str(head[0]), stdlib.Str(head[1]), ptrs...)
		}),
		"Sscanln": scanFn("fmt.Sscanln", 1, nil, fmt.Sscanln, func(head, ptrs []any) (int, error) { return fmt.Sscanln(stdlib.Str(head[0]), ptrs...) }),
		"Scan":    scanFn("fmt.Scan", 0, nil, fmt.Scan, func(_, ptrs []any) (int, error) { return fmt.Scan(ptrs...) }),
		"Scanf":   scanFn("fmt.Scanf", 1, nil, fmt.Scanf, func(head, ptrs []any) (int, error) { return fmt.Scanf(stdlib.Str(head[0]), ptrs...) }),
		"Scanln":  scanFn("fmt.Scanln", 0, nil, fmt.Scanln, func(_, ptrs []any) (int, error) { return fmt.Scanln(ptrs...) }),
		"Fscan":   scanFn("fmt.Fscan", 1, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscan, func(head, ptrs []any) (int, error) { return fmt.Fscan(head[0].(io.Reader), ptrs...) }),
		"Fscanf": scanFn("fmt.Fscanf", 2, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscanf, func(head, ptrs []any) (int, error) {
			return fmt.Fscanf(head[0].(io.Reader), stdlib.Str(head[1]), ptrs...)
		}),
		"Fscanln": scanFn("fmt.Fscanln", 1, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscanln, func(head, ptrs []any) (int, error) { return fmt.Fscanln(head[0].(io.Reader), ptrs...) }),
		// Errorf is hand-bound: %w verbs wrap the cause like Go's
		// fmt.wrapError so errors.Unwrap/Is/As see the chain.
		"Errorf": &runtime.BuiltinFunc{Name: "fmt.Errorf", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("fmt.Errorf needs 1+ args")
			}
			a, flat := fmtArgs(v, args)
			spec := stdlib.Str(a[0])
			// the %w operand indexes come from the spec as written —
			// they index the raw args; rewriteTypeVerbs renumbers the
			// directives and rebuilds the operand list for printing.
			_, wrapPos := rewriteWrapVerbs(spec)
			spec, tail := rewriteTypeVerbs(spec, a, flat, 0, v)
			a = append(a[:1], tail...)
			spec, _ = rewriteWrapVerbs(spec)
			msg := fmt.Sprintf(spec, a[1:]...)
			// like gc: the shape follows the raw count of %w verbs that
			// had an operand — one makes a wrapError (Unwrap() error),
			// several a wrapErrors (Unwrap() []error) over the distinct
			// operands in argument order; a non-error operand wraps
			// nothing.
			var pos []int
			for _, p := range wrapPos {
				if p < len(args)-1 {
					pos = append(pos, p)
				}
			}
			switch len(pos) {
			case 0:
				return stdlib.ErrVal(errors.New(msg)), nil
			case 1:
				return stdlib.ErrVal(&stdlib.WrapError{Msg: msg, Err: stdlib.WrapOperand(v, args[pos[0]+1])}), nil
			}
			slices.Sort(pos)
			var errs []error
			for _, p := range slices.Compact(pos) {
				if err := stdlib.WrapOperand(v, args[p+1]); err != nil {
					errs = append(errs, err)
				}
			}
			return stdlib.ErrVal(&stdlib.WrapErrors{Msg: msg, Errs: errs}), nil
		}},
	})
}

// rewriteWrapVerbs rewrites %w verbs to %v (flags, width and explicit
// [n] indexes kept) and reports the operand index of every %w, in verb
// order, the way gc's doPrintf records p.wrappedErrs: explicit indexes
// move the cursor, `*` width/precision consume an operand, and the list
// may repeat an index (`%[1]w %[1]w`). Indexes past the operands are
// reported too; the caller drops them (gc prints %!w(MISSING) and
// records nothing).
func rewriteWrapVerbs(spec string) (string, []int) {
	if !strings.Contains(spec, "w") {
		return spec, nil
	}
	var sb strings.Builder
	var wrapPos []int
	argNum := 0
	// index parses an explicit `[n]` at spec[i:], moving the cursor.
	index := func(i int) int {
		if i < len(spec) && spec[i] == '[' {
			if k := strings.IndexByte(spec[i:], ']'); k > 1 {
				if n, err := strconv.Atoi(spec[i+1 : i+k]); err == nil && n >= 1 {
					argNum = n - 1
					return i + k + 1
				}
			}
		}
		return i
	}
	digits := func(i int) int {
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			i++
		}
		return i
	}
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			sb.WriteString(spec[i:])
			break
		}
		sb.WriteString(spec[i : i+j])
		i += j
		pct := i
		i++
		for i < len(spec) && strings.IndexByte("#+- 0", spec[i]) >= 0 {
			i++
		}
		i = index(i)
		if i < len(spec) && spec[i] == '*' {
			argNum++
			i++
		} else {
			i = digits(i)
		}
		if i < len(spec) && spec[i] == '.' {
			i = index(i + 1)
			if i < len(spec) && spec[i] == '*' {
				argNum++
				i++
			} else {
				i = digits(i)
			}
		}
		i = index(i)
		if i >= len(spec) {
			sb.WriteString(spec[pct:])
			break
		}
		verb := spec[i]
		i++
		switch verb {
		case '%':
			sb.WriteString(spec[pct:i])
		case 'w':
			sb.WriteString(spec[pct : i-1])
			sb.WriteByte('v')
			wrapPos = append(wrapPos, argNum)
			argNum++
		default:
			sb.WriteString(spec[pct:i])
			argNum++
		}
	}
	return sb.String(), wrapPos
}

// ffn wraps a host fmt-style function: each arg becomes fmtArg so host
// fmt sees the script-shaped wrapper. formatAt is the index of the
// format-string arg (-1 when none); %T verbs there are rewritten to %s
// over the value's script type spelling because host fmt never calls
// Formatter.Format for %T/%p.
func ffn(name string, formatAt, minArgs int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return vffn(name, formatAt, minArgs, func(_ runtime.VMCaller, a []any) (any, error) {
		return f(a)
	}, target...)
}

// pffn is vffn without a format string whose inner fn also receives the
// flat script args — the Print family's operand spacing keys on the
// script-level string kind, which fmtArgs hides inside fmtValue.
func pffn(name string, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	return wffn(name, -1, minArgs, f, target...)
}

// vffn is ffn whose inner fn also receives the VM caller — needed when
// an argument must call back into the script (a script-defined
// io.Writer for fmt.Fprintf, say).
func vffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	return wffn(name, formatAt, minArgs, func(v runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		return f(v, a)
	}, target...)
}

// wffn is the shared fmt-call wrapper: the inner fn receives both the
// host-shaped args and the flat script args (before fmtArg unboxing).
func wffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < minArgs {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, minArgs, len(args))
		}
		a, flat := fmtArgs(v, args)
		if formatAt >= 0 && formatAt < len(a) {
			spec, ok := a[formatAt].(string)
			if !ok && formatAt < len(flat) {
				// the spec may reach the call as a script value — a
				// named string, a cell holding one — that fmtArg boxed
				// for tag-aware rendering. The spec itself must be a
				// plain string for host fmt and for %T/%p rewriting.
				spec, ok = formatString(flat[formatAt])
			}
			if ok {
				ns, tail := rewriteTypeVerbs(spec, a, flat, formatAt, v)
				a[formatAt] = ns
				a = append(a[:formatAt+1], tail...)
			}
		}
		r, err := f(v, a, flat)
		if err != nil {
			return nil, err
		}
		return stdlib.ScriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fmtValue wraps one script value for host fmt: Format renders %v/%+v/%#v
// with Go's composite layout and honors script String/Error/GoString.
type fmtValue struct {
	c     runtime.MemberCaller
	x     runtime.Value
	et    *runtime.TypeDef // declared element/field typedef, for bad-verb naming
	depth int
	// nilSyntax spells a nil slice or map in %#v's T(nil) form under %v
	// too, so REPL output tells it apart from an empty one ([] / map[]).
	nilSyntax bool
}

// zeroState is a flag-less fmt.State for rendering values outside a
// live Format call — %p substitution happens before host fmt runs.
type zeroState struct{}

// captureState delegates formatting flags to the live state but
// records writes: an element inside a composite returns its Format
// output as the element's string for the parent's assembled output —
// writing to the live state would emit the fragment ahead of the
// surrounding brackets (gc renders elements through a sub-buffer).
type captureState struct {
	f   fmt.State
	buf bytes.Buffer
}

// nested returns the state a value inside a composite renders against —
// a capture of the live state; internal stand-ins pass through.
func nested(f fmt.State) fmt.State {
	switch f.(type) {
	case zeroState, *captureState:
		return f
	}
	return &captureState{f: f}
}

// ptrSpelling renders a value for %p — host fmt never calls Format on
// %p so rewriteTypeVerbs substitutes this string. Typed nils print 0x0
// like Go; slices, maps, chans and script pointers print an address;
// anything else produces Go's %!p(type=value) marker.
// precIntOK reports whether v is a valid star-precision operand for the
// %p rewrite — the same rule as gc's intFromArg: any integer kind that
// fits an int (a named int64 like time.Duration qualifies), and for a
// precision non-negative.
func precIntOK(v runtime.Value) bool {
	n := runtime.Unwrap(v)
	if uc, ok := n.(*runtime.UConst); ok {
		if nv, err := runtime.UConstNative(uc); err == nil {
			n = nv
		}
	}
	switch rv := reflect.ValueOf(n); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		return i >= 0 && int64(int(i)) == i
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		return int64(u) >= 0 && uint64(int(u)) == u
	}
	return false
}

// starOperand lifts a *-operand arg to the integer the host fmt reads.
// The marshalled form can keep wrapper boxes (GoValue, UConst) that
// intFromArg cannot see through — a bound time.Duration reaches the
// host as its box — so the raw script value is peeled when the
// marshalled one is not already an integer kind. A non-integer operand
// passes through so the host reports BADWIDTH/BADPREC like gc.
func starOperand(x any, raw runtime.Value) any {
	intKind := func(k reflect.Kind) bool {
		switch k {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
			reflect.Uint64, reflect.Uintptr:
			return true
		}
		return false
	}
	if x != nil && intKind(reflect.ValueOf(x).Kind()) {
		return x
	}
	n := runtime.Unwrap(raw)
	if uc, ok := n.(*runtime.UConst); ok {
		if nv, err := runtime.UConstNative(uc); err == nil {
			n = nv
		}
	}
	if gv, ok := n.(*runtime.GoValue); ok {
		n = gv.V
	}
	if n != nil && intKind(reflect.ValueOf(n).Kind()) {
		return n
	}
	return x
}

// ptrZeroPadWidth reports whether the flags text is a '0'-flagged
// literal width (`030`), for gc's pad-inside-0x %p rule — returns the
// width. A `*` width stays host-side instead.
func ptrZeroPadWidth(flags string) (int, bool) {
	i := 0
	zero, minus := false, false
	for i < len(flags) && strings.IndexByte("#+- 0", flags[i]) >= 0 {
		zero = zero || flags[i] == '0'
		minus = minus || flags[i] == '-'
		i++
	}
	// a left-justified field pads trailing spaces; the 0 flag is inert.
	if !zero || minus {
		return 0, false
	}
	w := 0
	for ; i < len(flags); i++ {
		if flags[i] < '0' || flags[i] > '9' {
			return 0, false
		}
		w = w*10 + int(flags[i]-'0')
	}
	// gc counts the width over the hex digits inside the 0x prefix,
	// not over the whole spelling: %015p of a 12-digit address is
	// "0x" + "000" + addr.
	return w, w > 0
}

func ptrSpelling(c runtime.MemberCaller, x runtime.Value) string {
	zs := zeroState{}
	switch t := x.(type) {
	case runtime.Nil:
		return badVerb('p', "", "<nil>")
	case *runtime.TypedNil:
		if t.Typ != nil {
			switch t.Typ.Kind {
			case runtime.KindSlice, runtime.KindMap, runtime.KindChan,
				runtime.KindFunc, runtime.KindPointer:
				return "0x0"
			}
		}
		return badVerb('p', "", "<nil>")
	case *runtime.IfaceNil:
		// a non-nil interface holding a nil pointer still prints 0x0
		return "0x0"
	case *runtime.GoValue:
		if rv, ok := t.V.(*minireflect.RValue); ok {
			// %p never unwraps the Value — Go emits the
			// %!p(reflect.Value=<underlying %v>) bad-verb form.
			return badVerb('p', "reflect.Value", fmt.Sprintf("%v", fmtRValue(c, rv)))
		}
		return fmt.Sprintf("%p", t.V)
	case *runtime.Slice:
		if stdlib.IsArrayTyp(t.Typ) {
			fv := &fmtValue{c: c, x: t}
			return badVerb('p', stdlib.TypedefSpelling(t.Typ), fv.renderList(t, 'v', zs))
		}
		if len(t.Elems) == 0 {
			return "0x0"
		}
		return fmt.Sprintf("%p", t)
	case *runtime.Map, *runtime.Chan,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef,
		*runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return fmt.Sprintf("%p", t)
	case *runtime.Struct:
		fv := &fmtValue{c: c, x: t}
		return badVerb('p', stdlib.TypedefSpelling(t.Def), fv.render('v', zs))
	case *runtime.Named:
		return ptrSpelling(c, t.V)
	case *runtime.UConst:
		if nv, err := runtime.UConstNative(t); err == nil {
			return ptrSpelling(c, nv)
		}
	}
	fv := &fmtValue{c: c, x: x}
	return badVerb('p', scriptScalarName(x), fv.render('v', zs))
}

// ifaceCallStringPanic is runtime's panic-reporting Stringer probe — a
// script panic raised inside the method surfaces as the third return
// like gc's catchPanic — absent methods and ordinary call failures
// still report ok=false only. The method DOES run on a nil pointer
// arg (a nil-receiver-friendly Error still prints), but a panic from
// one renders <nil> rather than the PANIC marker — gc's catchPanic
// swaps the marker for <nil> when the arg is a nil pointer.
func ifaceCallStringPanic(c runtime.MemberCaller, x runtime.Value, name string) (string, bool, *runtime.Panic) {
	s, ok, pan := runtime.IfaceCallStringPanic(c, x, name)
	if pan != nil && fmtNilPointer(x) {
		return "", false, nil
	}
	return s, ok, pan
}

// fmtNilPointer reports whether x is a nil pointer for fmt's probe
// skip — Named and ref layers peel to the pointer-shaped TypedNil
// underneath.
func fmtNilPointer(x runtime.Value) bool {
	td, ok := stdlib.PeelNil(x)
	return ok && td != nil && td.Kind == runtime.KindPointer
}

// fmtPanicMarker spells gc's catchPanic marker for a Stringer-family
// method that panicked: %!v(PANIC=Error method: boom) — the verb as
// used (no width), the method name, and the panic payload rendered %v.
func fmtPanicMarker(verb rune, method string, p *runtime.Panic) string {
	return fmt.Sprintf("%%!%c(PANIC=%s method: %s)", verb, method, runtime.PanicPrintString(p))
}

// nilGoSyntax spells a typed nil the way Go's %#v does — T(nil), with
// parens around the anonymous pointer/chan/func spellings that need them
// and the top-level []byte special case for byte slices.
func nilGoSyntax(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil>"
	}
	if td.Name == "" {
		switch td.Kind {
		case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
			return "(" + stdlib.TypedefSpelling(td) + ")(nil)"
		case runtime.KindSlice:
			if stdlib.ByteSliceTyp(td) {
				return "[]byte(nil)"
			}
		}
	}
	return stdlib.TypedefSpelling(td) + "(nil)"
}

// badVerb emits Go's %!verb(type=value) marker for an incompatible verb.
func badVerb(verb rune, typ, val string) string {
	if typ == "" {
		return fmt.Sprintf("%%!%c(%s)", verb, val)
	}
	return fmt.Sprintf("%%!%c(%s=%s)", verb, typ, val)
}

// leafBadVerb reports whether verb is incompatible with a scalar leaf —
// Go's fmt then emits %!verb(type=value) instead of the value.
func leafBadVerb(x runtime.Value, verb rune) bool {
	switch x.(type) {
	case string:
		return !strings.ContainsRune("sqxXv", verb)
	case bool:
		return !strings.ContainsRune("tv", verb)
	case float64:
		return !strings.ContainsRune("eEfFgGxXbv", verb)
	case int64:
		return !strings.ContainsRune("dobcxXUqcv", verb)
	}
	return false
}

// scriptScalarName names a scalar leaf the way Go's fmt spells its type
// inside a bad-verb marker.
func scriptScalarName(x runtime.Value) string {
	switch t := x.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "float64"
	case int64:
		return "int"
	case *runtime.UConst:
		if nv, err := runtime.UConstNative(t); err == nil {
			return scriptScalarName(nv)
		}
	case *runtime.Named:
		// an interface-declared tag is never the dynamic type: the
		// payload's own tag (or raw scalar) names it — %T of `var a
		// A = N(1)` for `type A any` prints main.N like gc.
		n := t
		for n.Typ != nil && (n.Typ.IfaceBase || n.Typ.Kind == runtime.KindInterface) {
			nx, ok := n.V.(*runtime.Named)
			if !ok {
				break
			}
			n = nx
		}
		return stdlib.TypedefSpelling(n.Typ)
	case *runtime.GoValue:
		return ScriptTypeString(t)
	}
	return fmt.Sprintf("%T", x)
}

// formatDirective rebuilds a fmt directive from the verb and the
// flags/width a host call set on the state. hash drops the '#' flag for
// scalar leaves inside composites, where %#v keeps decimal ints (the
// 0x spelling is a top-level-only form).
func formatDirective(f fmt.State, verb rune, hash bool) string {
	var sb strings.Builder
	sb.WriteByte('%')
	flags := "+- 0"
	if hash {
		flags = "+-# 0"
	}
	for _, c := range flags {
		if f.Flag(int(c)) {
			sb.WriteByte(byte(c))
		}
	}
	if w, ok := f.Width(); ok {
		sb.WriteString(strconv.Itoa(w))
	}
	if p, ok := f.Precision(); ok {
		sb.WriteString("." + strconv.Itoa(p))
	}
	sb.WriteRune(verb)
	return sb.String()
}

// formatOfNoHash is formatOf without the '#' flag.
func formatOfNoHash(f fmt.State, verb rune) string {
	return formatDirective(f, verb, false)
}

// unsignedIntTyp reports whether td denotes an unsigned integer — the
// declared name or its underlying ident (a `type U uint64` decl).
// Unsigned kinds format their int64-carried bits as unsigned, and %#v
// spells them in hex like Go.
func unsignedIntTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	switch td.Name {
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return true
	}
	x := td.TypeExpr()
	if id, ok := x.(*ast.Ident); ok {
		switch id.Name {
		case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
			return true
		}
	}
	return false
}

// elemVerb picks the verb applied to elements inside a composite: every
// verb descends elementwise like Go's fmt — []int under %q quotes each
// element as a char, under %s each becomes a bad-verb marker — except
// %T which describes the composite itself, not the elements.
func elemVerb(verb rune) rune {
	if verb == 'T' {
		return 'v'
	}
	return verb
}

// sliceBytes reports the slice as bytes when its element type is
// byte/uint8 and every element is an int in byte range — the one slice
// fmt renders as text, not a bracketed list.
func sliceBytes(v *runtime.Slice) ([]byte, bool) {
	if !stdlib.ByteSliceTyp(v.Typ) {
		return nil, false
	}
	bs := make([]byte, len(v.Elems))
	for i, e := range v.Elems {
		n, ok := runtime.Unwrap(e).(int64)
		if !ok || n < 0 || n > 255 {
			return nil, false
		}
		bs[i] = byte(n)
	}
	return bs, true
}

// funcSigSpelling renders a function value's signature for %T and the
// %#v pointer wrapper — `func(int) int`, `func(...int)` — falling back
// to `func()` when the declaration isn't reachable (plain builtins).
func funcSigSpelling(v runtime.Value) string {
	if s, ok := runtime.FuncGoSpelling(v); ok {
		return s
	}
	if bf, ok := v.(*runtime.BuiltinFunc); ok && bf.Target != nil {
		// a bound host func spells its real signature — host fmt's
		// %T of a func IS the signature (func(...interface {}) (int, error))
		return fmt.Sprintf("%T", bf.Target)
	}
	return "func()"
}

// chanTypSpelling spells a channel's typedef for the %#v wrapper —
// `chan int`/`chan<- T`/`<-chan T` — `chan interface{}` when unknown.
func chanTypSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "chan interface{}"
	}
	return stdlib.TypedefSpelling(td)
}

// formatString recovers a plain string from a script value that reached
// a fmt call's format slot as a named string or a cell holding one.
func formatString(x runtime.Value) (string, bool) {
	for {
		switch v := x.(type) {
		case string:
			return v, true
		case *runtime.Named:
			x = v.V
		case *fmtValue:
			// a bound-fmt result boxed for rendering can land in a
			// named string slot — the spec is the boxed string.
			x = v.x
		case *runtime.GoValue:
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			return "", false
		default:
			if dv, ok := runtime.Deref(x); ok {
				x = dv
				continue
			}
			return "", false
		}
	}
}

// rewriteTypeVerbs substitutes args for the verbs host fmt handles
// without calling Formatter — %T (type spelling) and %p (address) —
// rewriting each spec verb to %s over a pre-rendered string. Positional
// indexes %[n] count arguments after the format string, as does
// implicit order. The rewrite renumbers every directive to sequential
// order and rebuilds the argument list to match, so a shared operand —
// "%v %[1]p" — keeps its original value for %v while %p sees the
// address string; mutating the operand slot in place would corrupt
// both — the rebuilt list can outgrow the original tail, so callers
// replace a[formatAt+1:] with the returned tail. Exotic mixes of %[n]
// and * in one directive, or operands past the argument list, pass
// through untouched.
func rewriteTypeVerbs(spec string, a []any, rawArgs []runtime.Value, formatAt int, c runtime.MemberCaller) (string, []any) {
	vals := rawArgs[formatAt+1:]
	off := formatAt + 1
	type dir struct {
		start, end int   // the directive's span in spec
		pos        int   // resolved operand index, 0-based
		verb       byte  // final verb letter
		stars      []int // operand positions each * consumed, in order
		starIdx    bool  // mixes * and %[n] — bail out below
	}
	var dirs []dir
	argNum, maxArg := 0, 0
	reordered := false
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			break
		}
		i += j
		pct := i
		i++ // past '%'
		if i >= len(spec) {
			break
		}
		pos := -1
		var stars []int
		var star, indexed bool
	scan:
		for i < len(spec) {
			ch := spec[i]
			switch {
			case ch == '[':
				k := i + 1
				for k < len(spec) && spec[k] != ']' {
					k++
				}
				if n, err := strconv.Atoi(spec[i+1 : k]); err == nil {
					pos = n - 1
					// after %[n] the next implicit argument is n (Go
					// fmt: "subsequent verbs will use arguments
					// n+1, n+2", 1-based).
					argNum = n
					indexed = true
					reordered = true
				}
				i = k + 1
			case ch == '*':
				if pos < 0 {
					stars = append(stars, argNum)
					argNum++
					if argNum > maxArg {
						maxArg = argNum
					}
				}
				star = true
				i++
			case ch == '#' || ch == '+' || ch == '-' || ch == ' ' || ch == '.' || (ch >= '0' && ch <= '9'):
				i++
			default:
				break scan
			}
		}
		if i >= len(spec) {
			break
		}
		verb := spec[i]
		i++
		if verb == '%' {
			continue
		}
		if pos < 0 {
			pos = argNum
			argNum++
		}
		if pos+1 > maxArg {
			maxArg = pos + 1
		}
		dirs = append(dirs, dir{start: pct, end: i, pos: pos, verb: verb, stars: stars, starIdx: star && indexed})
	}
	// rebuild only when every operand resolves inside the arg list and
	// no directive mixes * with an index — anything else keeps the
	// original spec (and operands) so host fmt reports it as before.
	for _, d := range dirs {
		if d.starIdx || d.pos < 0 || d.pos >= len(vals) || off+d.pos >= len(a) {
			return spec, a[off:]
		}
		for _, sp := range d.stars {
			if sp < 0 || sp >= len(vals) || off+sp >= len(a) {
				return spec, a[off:]
			}
		}
	}
	var sb strings.Builder
	var na []any
	prev := 0
	for _, d := range dirs {
		sb.WriteString(spec[prev:d.start])
		// flags/width survive; the [n] index does not — newArgs is
		// emitted in directive order, so every operand is implicit.
		tail := spec[d.start+1 : d.end-1]
		var fb strings.Builder
		precAt, precStars := -1, 0
		for k := 0; k < len(tail); {
			if tail[k] == '[' {
				for k < len(tail) && tail[k] != ']' {
					k++
				}
				k++
				continue
			}
			if tail[k] == '.' && precAt < 0 {
				precAt = fb.Len()
			} else if precAt >= 0 && tail[k] == '*' {
				precStars++
			}
			fb.WriteByte(tail[k])
			k++
		}
		flags := fb.String()
		stars := d.stars
		badPrec := false
		var spelling string
		pBadVerb := false
		if d.verb == 'p' {
			spelling = ptrSpelling(c, vals[d.pos])
			pBadVerb = strings.HasPrefix(spelling, "%!")
			if pBadVerb && !strings.ContainsRune(spelling, '=') {
				// a %!p(<nil>) marker consumes no operand — its width/
				// precision operands leave the operand list too.
				stars = nil
			} else if precAt >= 0 {
				// a * precision operand is validated even on the
				// bad-verb path: a non-integer or negative operand
				// prints %!(BADPREC) ahead of the marker. A valid one
				// stays so the marker's inner %v honors it; for a plain
				// pointer gc ignores precision entirely, so the text
				// and its operands always leave the rebuilt directive
				// (%.3p → "0x2" otherwise).
				bad := false
				for _, sp := range d.stars[len(d.stars)-precStars:] {
					if !precIntOK(vals[sp]) {
						bad = true
					}
				}
				if bad || !pBadVerb {
					badPrec = bad
					flags = flags[:precAt]
					stars = stars[:len(stars)-precStars]
				}
			}
		}
		for _, sp := range stars {
			na = append(na, starOperand(a[off+sp], vals[sp]))
		}
		if badPrec {
			// gc's marker precedes the formatted directive: write it
			// as escaped text before the directive's '%' opens.
			sb.WriteString("%%!(BADPREC)")
		}
		switch d.verb {
		case 'T':
			// %T is %s over the type spelling — every flag, width and
			// precision applies (%.1T prints "i"), and star operands
			// diagnose through the host fmt as themselves.
			sb.WriteByte('%')
			sb.WriteString(flags)
			sb.WriteByte('s')
			na = append(na, ScriptTypeString(vals[d.pos]))
		case 'p':
			if pBadVerb {
				// gc renders the arg inside the marker's parens:
				// %!p(int=         1) — the value is its %v with the
				// directive's own flags/width/precision applied. Rebuild
				// the marker around a %v of the raw arg so the host fmt
				// applies them and consumes the operands.
				inner := spelling[len("%!p(") : len(spelling)-1]
				if eq := strings.IndexByte(inner, '='); eq >= 0 {
					sb.WriteString("%%!p(")
					sb.WriteString(inner[:eq+1])
					sb.WriteByte('%')
					sb.WriteString(flags)
					sb.WriteByte('v')
					sb.WriteByte(')')
					na = append(na, a[off+d.pos])
				} else {
					sb.WriteString("%%")
					sb.WriteString(spelling[1:])
				}
				break
			}
			sb.WriteByte('%')
			if w, ok := ptrZeroPadWidth(flags); ok && strings.HasPrefix(spelling, "0x") && len(spelling)-2 < w {
				// gc's '0' flag pads the digits inside the 0x prefix
				// to the width, which %s cannot express — pad the
				// spelled address directly.
				spelling = "0x" + strings.Repeat("0", w-(len(spelling)-2)) + spelling[2:]
				sb.WriteByte('s')
			} else {
				sb.WriteString(flags)
				sb.WriteByte('s')
			}
			na = append(na, spelling)
		default:
			sb.WriteByte('%')
			sb.WriteString(flags)
			sb.WriteByte(d.verb)
			na = append(na, a[off+d.pos])
		}
		prev = d.end
	}
	sb.WriteString(spec[prev:])
	// args past the highest consumed position print as %!(EXTRA ...) —
	// carry them across untouched. Go suppresses the EXTRA report once
	// any %[n] index reordered the arg list.
	if !reordered {
		na = append(na, a[off+maxArg:]...)
	}
	return sb.String(), na
}

// formatOf rebuilds a fmt directive from the verb and the flags/width a
// host call set on the state — for scalar leaf values the host formatter
// does the real work.
func formatOf(f fmt.State, verb rune) string {
	return formatDirective(f, verb, true)
}

func withWidth(f fmt.State, s string) string {
	return fmt.Sprintf(formatOf(f, 's'), s)
}

// callFormat invokes a script fmt.Formatter — Format(fmt.State, rune) —
// handing the live printer through as a host value so the method's
// writes land on the real output (math/big's %d). An absent or
// panicking method reports false, like IfaceCallString.
// callFormatReport invokes a script Formatter like callFormat, and
// also reports a panic the call raised — fmt's catchPanic turns it into
// the %!v(PANIC=Format method: ...) marker rather than a silent miss.
func callFormatReport(c runtime.MemberCaller, x runtime.Value, f fmt.State, verb rune) (bool, *runtime.Panic) {
	m, ok := runtime.IfaceMember(c, x, "Format")
	if !ok {
		return false, nil
	}
	_, err := c.Call(m, []runtime.Value{
		&runtime.GoValue{V: f},
		&runtime.UConst{V: constant.MakeInt64(int64(verb)), Rune: true},
	})
	if err != nil {
		var p *runtime.Panic
		if errors.As(err, &p) {
			// gc's catchPanic swaps the marker for <nil> on a nil
			// pointer arg — suppressing it here lets the value fall
			// through to its own <nil> render.
			if fmtNilPointer(x) {
				return false, nil
			}
			return false, p
		}
		return false, nil
	}
	return true, nil
}

// fprintOperands renders the fmt.Print family's operands with gc's
// doPrint spacing: a space between operands only when neither is a
// string — where "string" is the reflect Kind, so a named type over
// string (json.Number) also suppresses it. The host-shaped args are
// *fmtValue wrappers (reflect sees Ptr), so the named-string check
// reads the flat script values.
func fprintOperands(w io.Writer, a []any, flat []runtime.Value) (int, error) {
	var buf bytes.Buffer
	prevStr := false
	for i, arg := range a {
		isStr := arg != nil && reflect.TypeOf(arg).Kind() == reflect.String
		if !isStr && i < len(flat) {
			isStr = isNamedString(flat[i])
		}
		if i > 0 && !isStr && !prevStr {
			buf.WriteByte(' ')
		}
		fmt.Fprint(&buf, arg) // bytes.Buffer writes never fail
		prevStr = isStr
	}
	return w.Write(buf.Bytes())
}

// isNamedString reports whether a script value is a named type whose
// underlying kind is string: Named layers unwrap to the stored scalar.
func isNamedString(x runtime.Value) bool {
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			_, isStr := x.(string)
			return isStr
		}
		x = n.V
	}
}

// fmtArgs maps script call args to host fmt args: a multi-value call
// result (Tuple) spreads into individual operands — Go treats
// `fmt.Sprint(f())` as `Sprint(v0, v1, ...)` so its space-between-
// operands rule (only when neither is a string) sees the real scalars.
// Returns the host args plus the flattened script args for spec
// rewriting (%T/%p positional lookup).
func fmtArgs(v runtime.MemberCaller, args []runtime.Value) ([]any, []runtime.Value) {
	a := make([]any, 0, len(args))
	flat := make([]runtime.Value, 0, len(args))
	for _, x := range args {
		if tup, ok := x.(*runtime.Tuple); ok {
			for _, e := range tup.Elems {
				a = append(a, fmtArg(v, e))
				flat = append(flat, e)
			}
			continue
		}
		a = append(a, fmtArg(v, x))
		flat = append(flat, x)
	}
	return a, flat
}

// fmtArg routes a script value into a host fmt call: scalars unbox to
// Go natives; composites keep their script shape inside a fmtValue.
func fmtArg(v runtime.MemberCaller, x runtime.Value) any {
	switch x := x.(type) {
	case *runtime.Named:
		// a fmtValue box inside the payload is already the argument
		// box — re-wrapping hides the inner value from %T rewriting
		// and %v alike (a Sprintf-accumulated named string slot can
		// carry one). Recurse on the boxed value so it renders and
		// types as itself.
		if fv, ok := x.V.(*fmtValue); ok {
			return fmtArg(v, fv.x)
		}
		// the tag only names the declared type — a host-boxed payload
		// still unwraps to fmtRValue, while other payloads keep
		// fmtValue's tag-aware rendering (float32 tags, named scalars).
		if gv, ok := x.V.(*runtime.GoValue); ok {
			return fmtArg(v, gv)
		}
		// a still-constant payload materializes at the declared width
		// first: `fmt.Println(uint64(1<<64 - 1))` is max-uint where the
		// untyped default reading overflows int inside fmtValue.
		mc, _ := v.(runtime.ConstMaterializer)
		if mv, ok := hostbridge.ConstAtWidth(mc, x); ok {
			return fmtArg(v, runtime.Tag(x.Typ, mv))
		}
		return &fmtValue{c: v, x: x}
	case int64:
		// script ints store int64 but spell int — bad-verb markers
		// (%!s(int=1)) and %T-adjacent spellings need the real width.
		return int(x)
	case float64, string, bool:
		return x
	case *runtime.UConst:
		nv := hostbridge.MustNativeConst(x)
		if i, ok := nv.(int64); ok {
			return int(i)
		}
		return nv
	case *runtime.GoValue:
		if rv, ok := x.V.(*minireflect.RValue); ok {
			return fmtRValue(v, rv)
		}
		return hostbridge.GoNative(x)
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil named host slice (net.IP) crosses as the host nil so
		// fmt runs its String method on the nil receiver ("<nil>")
		if rt := hostbridge.HostNilType(runtime.BoxedNilTyp(x)); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		if tn, ok := x.(*runtime.TypedNil); ok {
			if rt := hostbridge.HostNilType(tn.Typ); rt != nil {
				return reflect.Zero(rt).Interface()
			}
		}
		return &fmtValue{c: v, x: x}
	default:
		return &fmtValue{c: v, x: x}
	}
}

// fmtRValue routes a facade reflect.Value to host fmt the way Go routes
// a reflect.Value: the argument unwraps exactly once — an invalid Value
// prints `<invalid reflect.Value>` and a payload that is itself a Value
// stays wrapped so its String still yields Go's nested `<T Value>` form
// — while every other payload formats as the viewed value under any
// verb. %T/%p are rewritten before this runs (rewriteTypeVerbs).
func fmtRValue(c runtime.MemberCaller, rv *minireflect.RValue) any {
	if !rv.IsValid() {
		return "<invalid reflect.Value>"
	}
	switch u := rv.Payload().(type) {
	case *minireflect.RValue:
		return u
	case minireflect.RValue:
		return &u
	case *runtime.GoValue:
		if inner, ok := u.V.(*minireflect.RValue); ok {
			return inner
		}
		return hostbridge.GoNative(u)
	case runtime.Value:
		return fmtArg(c, u)
	default:
		return u
	}
}

func (s *fmtValue) Format(f fmt.State, verb rune) {
	io.WriteString(f, s.render(verb, f))
}

func (zeroState) Write(b []byte) (int, error) { return len(b), nil }

func (zeroState) Width() (int, bool) { return 0, false }

func (zeroState) Precision() (int, bool) { return 0, false }

func (zeroState) Flag(int) bool { return false }

func (s *captureState) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *captureState) Width() (int, bool) { return s.f.Width() }

func (s *captureState) Precision() (int, bool) { return s.f.Precision() }

func (s *captureState) Flag(ch int) bool { return s.f.Flag(ch) }

func (s *fmtValue) render(verb rune, f fmt.State) string {
	// fmt consults Formatter before the Stringer family, for every verb
	// but %T/%p — a script Formatter writes to the live state itself, so
	// a successful call already emitted its output. zeroState is the
	// flag-less stand-in for nested renderings (bad-verb markers, %p)
	// where Go invokes no Formatter; nothing real writes to it.
	if s.c != nil && verb != 'T' && verb != 'p' {
		if cs, isCap := f.(*captureState); isCap {
			wrote, pan := callFormatReport(s.c, s.x, cs, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return cs.buf.String()
			}
		} else if _, isZero := f.(zeroState); !isZero {
			wrote, pan := callFormatReport(s.c, s.x, f, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return ""
			}
		}
	}
	// Stringer family first, like fmt does.
	if s.c != nil {
		switch verb {
		case 'v':
			if f.Flag('#') {
				// %#v consults GoString, not String/Error.
				if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "GoString"); pan != nil {
					return fmtPanicMarker(verb, "GoString", pan)
				} else if ok {
					return str
				}
				break
			}
			// Go's fmt consults error before Stringer — a value
			// implementing both prints its Error().
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 's':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 'q':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return strconv.Quote(str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return strconv.Quote(str)
			}
		case 'x', 'X':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
		}
	}
	return s.renderValue(s.x, verb, f)
}

func (s *fmtValue) renderValue(x runtime.Value, verb rune, f fmt.State) string {
	// Go's fmt has no render-depth cap — a deeply nested composite
	// prints every level (issue29264), and a cyclic one overflows the
	// host stack just as it does under go run.
	switch v := x.(type) {
	case *runtime.Named:
		// %T keeps the declared name; other verbs render through.
		if verb == 'T' {
			if runtime.IfaceTaggedNil(v.V) != nil {
				return "<nil>" // a nil interface has no dynamic type
			}
			return stdlib.TypedefSpelling(v.Typ)
		}
		// a bound host scalar (time.Month/time.Duration) whose payload is
		// still script-shaped — a stored int64 or a const-domain UConst —
		// renders through the host scalar so String() and every verb
		// apply like gc (%v → "January", %d → 1).
		if td := v.Typ; td != nil && td.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(td, v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), hv)
			}
		}
		// an int64 carrying a uint64/uintptr tag must format its bits as
		// unsigned — host fmt would read the int64 as signed otherwise.
		if unsignedIntTyp(v.Typ) {
			if iv, ok := v.V.(int64); ok {
				return fmt.Sprintf(formatOf(f, verb), uint64(iv))
			}
		}
		// a float64 carrying a float32 tag formats in float32 — the
		// rounded value, not the wider box's digits. A payload still
		// riding the constant domain reads at float32 width the same
		// way (`float32(c)` conversions keep UConst).
		if hostbridge.Float32Tag(v.Typ) {
			if fv, ok := v.V.(float64); ok {
				return fmt.Sprintf(formatOf(f, verb), float32(fv))
			}
			if uc, ok := v.V.(*runtime.UConst); ok {
				if nv, err := runtime.UConstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return fmt.Sprintf(formatOf(f, verb), float32(fv))
					}
				}
			}
		}
		// a complex128 carrying a complex64 tag rounds each half to
		// float32 — same gap as float32, one level wider.
		if hostbridge.Complex64Tag(v.Typ) {
			if cv, ok := hostbridge.Complex64Value(v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), cv)
			}
		}
		return (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: v.V, et: v.Typ, depth: s.depth + 1}).render(verb, f)
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(v)
		if !ok {
			return "<nil>"
		}
		// Go's printPtr descends one level only when the pointee is a
		// composite — struct, array, slice, or map — spelling `&[...]`/
		// `&{...}`/`&map[...]`; every other pointee (scalars, other
		// pointers, chans, funcs) reads as the address. The descent only
		// happens at the top level: a pointer nested inside a composite
		// prints 0x... — []*S{p} renders [0xADDR], and under %#v
		// [(*S)(0xADDR)].
		composite := func(x runtime.Value) bool {
			switch t := x.(type) {
			case *runtime.Struct, *runtime.Slice, *runtime.Map:
				return true
			case *runtime.TypedNil:
				// &ns for a nil slice/map spells &[]/&map[]
				return t.Typ != nil &&
					(t.Typ.Kind == runtime.KindSlice || t.Typ.Kind == runtime.KindMap)
			}
			return false
		}
		if s.depth == 0 && composite(dv) {
			return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: dv, depth: s.depth + 1}).render(verb, nested(f))
		}
		if s.depth == 0 {
			if n, isNamed := dv.(*runtime.Named); isNamed && composite(n.V) {
				return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: n.V, depth: s.depth + 1}).render(verb, nested(f))
			}
		}
		// scalar pointer: Go prints the address — a host pointer repr
		// is the closest readable stand-in; %#v wraps it as (*T)(0xADDR).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf("(*%s)(%p)", ScriptTypeString(dv), v)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Struct:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Def)
		case 'q':
			return strconv.Quote(s.renderValue(x, 'v', f))
		}
		parts := make([]string, len(v.Fields))
		for i, e := range v.Fields {
			fv := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: stdlib.FieldTypOf(v.Def, i), depth: s.depth + 1}).render(elemVerb(verb), nested(f))
			if f.Flag('+') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			if f.Flag('#') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			parts[i] = fv
		}
		sep := " "
		if f.Flag('#') {
			sep = ", "
		}
		body := strings.Join(parts, sep)
		if f.Flag('#') {
			return stdlib.TypedefSpelling(v.Def) + "{" + body + "}"
		}
		return "{" + body + "}"
	case *runtime.Slice:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'p':
			if stdlib.IsArrayTyp(v.Typ) {
				return badVerb(verb, stdlib.TypedefSpelling(v.Typ), s.renderList(v, 'v', f))
			}
			return fmt.Sprintf("%p", v)
		case 's', 'q', 'x', 'X':
			// a byte-wise slice formats as text like Go's fmt does
			if bs, ok := sliceBytes(v); ok {
				switch verb {
				case 's':
					return string(bs)
				case 'q':
					return strconv.Quote(string(bs))
				default:
					return fmt.Sprintf(formatOf(f, verb), bs)
				}
			}
		case 'v':
			// %#v renders a byte slice as []byte{0xNN, ...} — the byte
			// spelling applies only at the top level (reflect cannot
			// distinguish byte from uint8 below it).
			if f.Flag('#') && stdlib.ByteSliceTyp(v.Typ) {
				if bs, ok := sliceBytes(v); ok {
					spell := stdlib.TypedefSpelling(v.Typ)
					if v.Typ.Name == "" && s.depth == 0 && !stdlib.IsArrayTyp(v.Typ) {
						spell = "[]byte"
					}
					parts := make([]string, len(bs))
					for i, b := range bs {
						parts[i] = fmt.Sprintf("0x%x", b)
					}
					return spell + "{" + strings.Join(parts, ", ") + "}"
				}
			}
		}
		return s.renderList(v, verb, f)
	case *runtime.Map:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		// Go's fmt prints maps in sorted-key order (fmtsort), not
		// insertion order — sort a copy of Order the same way.
		order, _ := v.SnapshotKeys()
		sort.SliceStable(order, func(i, j int) bool {
			a, b := order[i], order[j]
			// Mixed-type keys (map[any]) compare by the concrete
			// type first, like fmtsort's interface rule.
			ra, ta := mapKeyRank(a)
			rb, tb := mapKeyRank(b)
			if ra != rb {
				return ra < rb
			}
			if ta != tb {
				return ta < tb
			}
			if stdlib.LessScript(a, b) {
				return true
			}
			if stdlib.LessScript(b, a) {
				return false
			}
			// unordered kinds (bool, composites, mixed): order by the
			// rendered key like fmtsort's fallback.
			ka := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: a, depth: s.depth + 1}).render('v', nested(f))
			kb := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: b, depth: s.depth + 1}).render('v', nested(f))
			return ka < kb
		})
		kt, vt := stdlib.MapElemTyps(v.Typ)
		ev := elemVerb(verb)
		parts := make([]string, 0, len(order))
		for _, k := range order {
			e, _ := v.Get(k)
			kr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: k, et: kt, depth: s.depth + 1}).render(ev, nested(f))
			vr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: vt, depth: s.depth + 1}).render(ev, nested(f))
			parts = append(parts, kr+":"+vr)
		}
		if f.Flag('#') {
			return stdlib.TypedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
		}
		return "map[" + strings.Join(parts, " ") + "]"
	case *runtime.Tuple:
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, depth: s.depth + 1}).render(verb, nested(f))
		}
		return strings.Join(parts, " ")
	case *runtime.TypedNil:
		if verb == 'T' {
			return stdlib.TypedefSpelling(v.Typ)
		}
		if rt := hostbridge.HostNilType(v.Typ); rt != nil {
			// a nil named host slice (net.IP) renders as the host nil
			// would — its String method on the nil receiver
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case *runtime.IfaceNil:
		if v.Typ != nil && v.Typ.Kind == runtime.KindInterface {
			// a nil interface value prints like a bare nil — <nil> at
			// top level, the slot's static type inside %#v composites.
			if verb == 'v' && f.Flag('#') && s.et != nil {
				return nilGoSyntax(s.et)
			}
			if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
				return "<nil>"
			}
			return badVerb(verb, "", "<nil>")
		}
		if verb == 'T' {
			return stdlib.TypedefSpelling(v.Typ)
		}
		if rt := hostbridge.HostNilType(v.Typ); rt != nil {
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case runtime.Nil:
		// %#v spells a nil sitting in an interface-typed field or
		// element with the slot's static type — error(nil),
		// interface {}(nil) — the way reflect sees the declared type.
		// Every other verb (and an untyped top-level nil) keeps <nil>.
		if verb == 'v' && f.Flag('#') && s.et != nil {
			return nilGoSyntax(s.et)
		}
		// a nil interface element inside a composite renders <nil>
		// under every verb; top-level non-%v verbs get the marker.
		if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
			return "<nil>"
		}
		return badVerb(verb, "", "<nil>")
	case *runtime.Chan:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'v':
			// %#v spells pointer-shaped values as (T)(0xADDR).
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", chanTypSpelling(v.Typ), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, stdlib.TypedefSpelling(v.Typ), fmt.Sprintf("%p", v))
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch verb {
		case 'T':
			return funcSigSpelling(v)
		case 'v':
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", funcSigSpelling(v), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, funcSigSpelling(v), fmt.Sprintf("%p", v))
	case *runtime.GoValue:
		if verb == 'T' {
			// a proxy box spells its API type, not the box's name.
			if rt := stdtime.ProxyHostTypeOf(v.V); rt != nil {
				return rt.String()
			}
		}
		return fmt.Sprintf(formatOf(f, verb), v.V)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return stdlib.TypedefSpelling(v)
	}
	return s.leaf(x, verb, f)
}

// renderList renders a slice's bracketed element list — every verb
// descends elementwise like Go's fmt ([]int under %q quotes each element
// as a char, under %s each becomes a %!s marker).
func (s *fmtValue) renderList(v *runtime.Slice, verb rune, f fmt.State) string {
	et := stdlib.SliceElemTyp(v.Typ)
	ev := elemVerb(verb)
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: et, depth: s.depth + 1}).render(ev, nested(f))
	}
	if f.Flag('#') && verb == 'v' {
		return stdlib.TypedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// leaf formats a scalar value: compatible verbs go to host fmt after
// normalizing to the script-canonical Go type (script ints are int64 but
// spell int), incompatible verbs produce Go's %!verb(type=value) marker
// spelled with the declared element type when the leaf has one.
func (s *fmtValue) leaf(x runtime.Value, verb rune, f fmt.State) string {
	if u, ok := x.(*runtime.UConst); ok {
		x = hostbridge.MustNativeConst(u)
	}
	if leafBadVerb(x, verb) {
		name := ""
		// an interface-typed element (incl. any) names the value's
		// dynamic type, not the interface — []any{1} gives %!v(int=1)
		if s.et != nil && s.et.Name != "any" {
			if _, isIface := s.et.TypeExpr().(*ast.InterfaceType); !isIface {
				name = stdlib.TypedefSpelling(s.et)
			}
		}
		if name == "" {
			name = scriptScalarName(x)
		}
		return badVerb(verb, name, (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: x, depth: s.depth + 1}).render('v', nested(f)))
	}
	if i, ok := x.(int64); ok {
		// script ints store int64 but spell int — including inside
		// composites, where %#v elements stay decimal (the 0x-hex int
		// spelling is a top-level-only form).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf(formatOfNoHash(f, verb), int(i))
		}
		return fmt.Sprintf(formatOf(f, verb), int(i))
	}
	return fmt.Sprintf(formatOf(f, verb), x)
}

// nilTyp renders a typed nil like Go's fmt: a nil slice or map prints its
// empty composite ([] or map[]) under every verb — a nil []byte renders
// like the empty string — while a nil pointer, chan or func prints
// <nil>/0x0/0 like the nil address it is. %#v uses the Go-syntax
// conversion form T(nil): []string(nil), map[string]int(nil), (*int)(nil).
func (s *fmtValue) nilTyp(verb rune, f fmt.State, td *runtime.TypeDef) string {
	if verb == 'v' && f.Flag('#') {
		return nilGoSyntax(td)
	}
	if verb == 'v' && s.nilSyntax && td != nil &&
		(td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) {
		return nilGoSyntax(td)
	}
	if td == nil {
		return "<nil>"
	}
	switch td.Kind {
	case runtime.KindSlice:
		if stdlib.ByteSliceTyp(td) {
			switch verb {
			case 's', 'x', 'X':
				return ""
			case 'q':
				return strconv.Quote("")
			}
		}
		if verb == 'p' {
			return "0x0"
		}
		return "[]"
	case runtime.KindMap:
		if verb == 'p' {
			return "0x0"
		}
		return "map[]"
	case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
		switch verb {
		case 'v':
			return "<nil>"
		case 'p':
			return "0x0"
		case 'd', 'o', 'b', 'x', 'X', 'U', 'c':
			// a nil address formats as 0 under the numeric verbs
			return fmt.Sprintf(formatOf(f, verb), 0)
		}
		return badVerb(verb, stdlib.TypedefSpelling(td), "<nil>")
	}
	return "<nil>"
}

// scanFn adapts fmt's Scan family: the out-params arrive as script refs,
// which host fmt cannot store through, so each is mirrored into a host
// variable of the pointee's type, the real fmt call scans into those, and
// each result is written back through the ref. heads counts the leading
// non-pointer args (a format string or reader); headFn converts any
// prefix of them it needs to (nil: none) and scanFn goNatives the rest,
// so a converter like scanReader never has to know the arg count.
func scanFn(name string, heads int, headFn func(runtime.MemberCaller, []runtime.Value) ([]any, error), target any, call func(head, ptrs []any) (int, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < heads {
			return nil, fmt.Errorf("%s needs at least %d args, got %d", name, heads, len(args))
		}
		var head []any
		if headFn != nil {
			var err error
			if head, err = headFn(v, args[:heads]); err != nil {
				return nil, err
			}
		}
		for i := len(head); i < heads; i++ {
			head = append(head, hostbridge.GoNative(args[i]))
		}
		ptrs := make([]any, len(args)-heads)
		tds := make([]*runtime.TypeDef, len(args)-heads)
		for i, ref := range args[heads:] {
			cur, ok := runtime.Deref(ref)
			if !ok {
				return stdlib.RetErr(0, fmt.Errorf("%s: argument %d is not a pointer", name, i+heads+1))
			}
			// the host mirror erases the pointee's declared tag —
			// `var m MyInt; Sscan("5", &m)` — so capture it for the
			// write-back below.
			tds[i] = runtime.TagOf(cur)
			t := reflect.TypeOf(hostbridge.GoNative(cur))
			if t == nil {
				return stdlib.RetErr(0, fmt.Errorf("%s: cannot infer scan target type: argument %d", name, i+heads+1))
			}
			rv := reflect.New(t)
			// Seed with the current pointee: gc leaves args the scan
			// never reached (or failed to store) untouched.
			rv.Elem().Set(reflect.ValueOf(hostbridge.GoNative(cur)))
			ptrs[i] = rv.Interface()
		}
		n, err := call(head, ptrs)
		// gc stores whatever it managed to scan even on error, so the
		// write-back runs unconditionally.
		for i, ref := range args[heads:] {
			out := stdlib.ScriptVal(reflect.ValueOf(ptrs[i]).Elem().Interface())
			if tds[i] != nil {
				out = runtime.Tag(tds[i], out)
			}
			runtime.SetRef(ref, out)
		}
		return stdlib.RetErr(n, err)
	}}
}

// scanReader converts an Fscan family's leading reader arg, accepting
// both host io.Reader values and script-defined readers.
func scanReader(ad *stdio.Adapters, v runtime.MemberCaller, args []runtime.Value) ([]any, error) {
	r, err := ad.AsReaderVM(v, args[0])
	if err != nil {
		return nil, err
	}
	return []any{r}, nil
}

// scriptTypeString spells a value's type the way Go's %T does —
// "[]int", "main.Point" — using the typedef, not the Go wrapper type.
func ScriptTypeString(x runtime.Value) string {
	switch t := x.(type) {
	case nil, runtime.Nil:
		// a nil interface has no dynamic type — %T spells <nil>.
		return "<nil>"
	case *runtime.UConst:
		return t.DefaultName()
	case *runtime.Named:
		if stdlib.NilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Struct:
		return stdlib.TypedefSpelling(t.Def)
	case *runtime.Slice:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Map:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Chan:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.TypedNil:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.IfaceNil:
		if stdlib.NilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.PanicNilError:
		return "*runtime.PanicNilError"
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(t); ok {
			// the pointee slot's stamped declared type names the
			// pointee's static type — `&i` on an interface-typed var
			// spells *main.I, not *main.S of its stored value. A ref
			// operand reads it through the ref itself: a FieldRef's
			// declared field type, an IndexRef's element type.
			if td := stdlib.PointeeDeclTyp(t); td != nil {
				return "*" + stdlib.TypedefSpelling(td)
			}
			if c := stdlib.AsCell(dv); c != nil && c.Typ != nil {
				return "*" + stdlib.TypedefSpelling(c.Typ)
			}
			// *interface{} / *error keeps the pointee's declared name —
			// the pointer itself is the dynamic type, not <nil>.
			if td := stdlib.NilIfaceTyp(dv); td != nil {
				return "*" + stdlib.TypedefSpelling(td)
			}
			return "*" + ScriptTypeString(dv)
		}
		return "unsafe.Pointer"
	case *runtime.GoValue:
		if se, ok := t.V.(*hostbridge.ScriptError); ok {
			return ScriptTypeString(se.V)
		}
		if stdjson.IsEncoder(t.V) {
			return "*json.Encoder"
		}
		switch t.V.(type) {
		case stdruntime.CleanupHandle:
			return "runtime.Cleanup"
		case *minireflect.RValue:
			return "reflect.Value"
		case *minireflect.RType:
			return "*reflect.rtype"
		case *minireflect.MapIter:
			return "*reflect.MapIter"
		case *minireflect.StructField:
			return "reflect.StructField"
		case *minireflect.Method:
			return "reflect.Method"
		}
		if at, ok := t.V.(stdlib.APITypeBox); ok {
			// a facade spells its API type, not the box's name.
			return at.APIType().String()
		}
		if rt := stdtime.ProxyHostTypeOf(t.V); rt != nil {
			// a proxy box spells its API type, not the box's name.
			return rt.String()
		}
		return fmt.Sprintf("%T", t.V)
	case int64:
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return funcSigSpelling(t)
	default:
		return fmt.Sprintf("%T", x)
	}
}

// UnwrapFmtArg peels the fmt argument box off a `&x` reference — used as
// the io Adapters' UnwrapArg by every installer that hands raw args to
// host calls.
func UnwrapFmtArg(v any) any {
	if fv, ok := v.(*fmtValue); ok {
		return fv.x
	}
	return v
}

// NewFmtValue boxes a script value for host fmt.Format — the repl hooks
// hand script values to Go formatters through it.
func NewFmtValue(c runtime.MemberCaller, x runtime.Value) fmt.Formatter {
	return &fmtValue{c: c, x: x, nilSyntax: true}
}

// mapKeyRank ranks a map key by its concrete type like go's
// internal/fmtsort orders interface keys — the type first, then the
// value. Go orders types by the rtype's address, which happens to be a
// fixed layout for the predeclared types (string < int8 < ... < bool)
// and allocation luck for everything else; the predeclared ranks pin
// that layout and other types fall into deterministic groups ordered
// by their typedef spell.
func mapKeyRank(v runtime.Value) (int, string) {
	var spell string
	switch x := v.(type) {
	case nil, runtime.Nil, *runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return 0, ""
	case int64:
		spell = "int"
	case float64:
		spell = "float64"
	case string:
		spell = "string"
	case bool:
		spell = "bool"
	case complex128:
		spell = "complex128"
	case *runtime.UConst:
		// an untyped constant's dynamic type is its default type.
		switch x.V.Kind() {
		case constant.Int:
			spell = "int"
		case constant.Float:
			spell = "float64"
		case constant.String:
			spell = "string"
		case constant.Bool:
			spell = "bool"
		case constant.Complex:
			spell = "complex128"
		default:
			return 60, ""
		}
	case *runtime.Named:
		if x.Typ == nil {
			return 60, ""
		}
		// the tag of a host-typed named — "time.Duration" — must
		// match its GoValue and unboxed forms' spelling below;
		// host types rank after the predeclared set like go's
		// rtype layout does.
		if rt := runtime.HostReflectType(x.Typ); rt != nil {
			if _, basic := fmtKeyBasicRank[rt.String()]; !basic {
				return 50, rt.String()
			}
			spell = rt.String()
		} else {
			spell = stdlib.TypedefSpelling(x.Typ)
		}
	case *runtime.Struct:
		if x.Def != nil && stdlib.IsArrayTyp(x.Def) {
			return 10, stdlib.TypedefSpelling(x.Def)
		}
		if x.Def != nil {
			return 12, stdlib.TypedefSpelling(x.Def)
		}
		return 12, ""
	case *runtime.Chan:
		if x.Typ != nil {
			return 11, stdlib.TypedefSpelling(x.Typ)
		}
		return 11, ""
	case *runtime.GoValue:
		if rt := reflect.TypeOf(x.V); rt != nil {
			if _, basic := fmtKeyBasicRank[rt.String()]; !basic {
				return 50, rt.String()
			}
			spell = rt.String()
		} else {
			return 0, ""
		}
	default:
		// a raw host named scalar (a bound const's payload) spells
		// like its boxed Named/GoValue forms.
		if _, rt, ok := runtime.HostNamedPayload(v); ok {
			return 50, rt.String()
		}
		return 60, fmt.Sprintf("%T", v)
	}
	if r, ok := fmtKeyBasicRank[spell]; ok {
		return r, ""
	}
	// Named and host types group before the predeclared set, ordered
	// among themselves by spell — their go order is the rtype's
	// address, which no spelling can reproduce.
	return 20, spell
}

// fmtKeyBasicRank is the predeclared types' layout order in go's
// fmtsort output (string < int8 < ... < bool), as printed by go1.27.
var fmtKeyBasicRank = map[string]int{
	"string":     30,
	"int8":       31,
	"uint8":      32,
	"int16":      33,
	"uint16":     34,
	"int32":      35,
	"uint32":     36,
	"int64":      37,
	"uint64":     38,
	"int":        39,
	"uint":       40,
	"uintptr":    41,
	"complex64":  42,
	"complex128": 43,
	"float32":    44,
	"float64":    45,
	"bool":       46,
}
