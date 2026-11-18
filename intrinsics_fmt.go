package minigo

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

	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installFmt(h *hostHelpers) {
	e.bindBound("fmt", map[string]runtime.Value{
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
		"Print": h.pffn("fmt.Print", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return retErr(fprintOperands(h.out(), a, flat))
		}, fmt.Print),
		"Println": h.ffn("fmt.Println", -1, 0, func(a []any) (any, error) { return retErr(fmt.Fprintln(h.out(), a...)) }, fmt.Println),
		"Printf": h.ffn("fmt.Printf", 0, 1, func(a []any) (any, error) {
			return retErr(fmt.Fprintf(h.out(), str(a[0]), a[1:]...))
		}),
		// Fprint* take an explicit writer — os.Stdout/os.Stderr arrive as
		// GoValue (unwrapped by fmtArg to the native *os.File).
		"Fprint": h.pffn("fmt.Fprint", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fprintOperands(w, a[1:], flat[1:]))
		}),
		"Fprintf": h.vffn("fmt.Fprintf", 1, 2, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintf(w, str(a[1]), a[2:]...))
		}),
		"Fprintln": h.vffn("fmt.Fprintln", -1, 1, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintln(w, a[1:]...))
		}),
		"Sprint": h.pffn("fmt.Sprint", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a, flat); err != nil {
				return nil, err
			}
			return buf.String(), nil
		}, fmt.Sprint),
		"Sprintln": h.ffn("fmt.Sprintln", -1, 0, func(a []any) (any, error) { return fmt.Sprintln(a...), nil }, fmt.Sprintln),
		"Sprintf": h.ffn("fmt.Sprintf", 0, 1, func(a []any) (any, error) {
			return fmt.Sprintf(str(a[0]), a[1:]...), nil
		}, fmt.Sprintf),
		// The Append family formats like Sprint/Sprintf/Sprintln onto a
		// caller's byte slice. The buffer arg is read from flat (the raw
		// script value): a[i] holds fmtValue boxes for composite args,
		// which byteSlice cannot see through. Source-interpreted packages
		// reach these through e.g. log's output(), which formats via
		// fmt.Appendln.
		"Append": h.pffn("fmt.Append", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a[1:], flat[1:]); err != nil {
				return nil, err
			}
			return append(byteSlice(flat[0]), buf.Bytes()...), nil
		}, fmt.Append),
		"Appendf": h.wffn("fmt.Appendf", 1, 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendf(byteSlice(flat[0]), str(a[1]), a[2:]...), nil
		}, fmt.Appendf),
		"Appendln": h.pffn("fmt.Appendln", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendln(byteSlice(flat[0]), a[1:]...), nil
		}, fmt.Appendln),
		// The Scan family's out-params arrive as script refs; scanFn
		// mirrors each into a host var of the pointee's type, scans, and
		// writes results back through the ref (parse/node.go's
		// complex-literal path needs Sscan).
		"Sscan":   h.scanFn("fmt.Sscan", 1, nil, fmt.Sscan, func(head, ptrs []any) (int, error) { return fmt.Sscan(str(head[0]), ptrs...) }),
		"Sscanf":  h.scanFn("fmt.Sscanf", 2, nil, fmt.Sscanf, func(head, ptrs []any) (int, error) { return fmt.Sscanf(str(head[0]), str(head[1]), ptrs...) }),
		"Sscanln": h.scanFn("fmt.Sscanln", 1, nil, fmt.Sscanln, func(head, ptrs []any) (int, error) { return fmt.Sscanln(str(head[0]), ptrs...) }),
		"Scan":    h.scanFn("fmt.Scan", 0, nil, fmt.Scan, func(_, ptrs []any) (int, error) { return fmt.Scan(ptrs...) }),
		"Scanf":   h.scanFn("fmt.Scanf", 1, nil, fmt.Scanf, func(head, ptrs []any) (int, error) { return fmt.Scanf(str(head[0]), ptrs...) }),
		"Scanln":  h.scanFn("fmt.Scanln", 0, nil, fmt.Scanln, func(_, ptrs []any) (int, error) { return fmt.Scanln(ptrs...) }),
		"Fscan":   h.scanFn("fmt.Fscan", 1, h.scanReader, fmt.Fscan, func(head, ptrs []any) (int, error) { return fmt.Fscan(head[0].(io.Reader), ptrs...) }),
		"Fscanf":  h.scanFn("fmt.Fscanf", 2, h.scanReader, fmt.Fscanf, func(head, ptrs []any) (int, error) { return fmt.Fscanf(head[0].(io.Reader), str(head[1]), ptrs...) }),
		"Fscanln": h.scanFn("fmt.Fscanln", 1, h.scanReader, fmt.Fscanln, func(head, ptrs []any) (int, error) { return fmt.Fscanln(head[0].(io.Reader), ptrs...) }),
		// Errorf is hand-bound: %w verbs wrap the cause like Go's
		// fmt.wrapError so errors.Unwrap/Is/As see the chain.
		"Errorf": &runtime.BuiltinFunc{Name: "fmt.Errorf", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("fmt.Errorf needs 1+ args")
			}
			a, flat := fmtArgs(v, args)
			spec := str(a[0])
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
				return errVal(errors.New(msg)), nil
			case 1:
				return errVal(&wrapError{msg: msg, err: wrapOperand(v, args[pos[0]+1])}), nil
			}
			slices.Sort(pos)
			var errs []error
			for _, p := range slices.Compact(pos) {
				if err := wrapOperand(v, args[p+1]); err != nil {
					errs = append(errs, err)
				}
			}
			return errVal(&wrapErrors{msg: msg, errs: errs}), nil
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
func (h *hostHelpers) ffn(name string, formatAt, minArgs int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.vffn(name, formatAt, minArgs, func(_ runtime.VMCaller, a []any) (any, error) {
		return f(a)
	}, target...)
}

// pffn is vffn without a format string whose inner fn also receives the
// flat script args — the Print family's operand spacing keys on the
// script-level string kind, which fmtArgs hides inside fmtValue.
func (h *hostHelpers) pffn(name string, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.wffn(name, -1, minArgs, f, target...)
}

// vffn is ffn whose inner fn also receives the VM caller — needed when
// an argument must call back into the script (a script-defined
// io.Writer for fmt.Fprintf, say).
func (h *hostHelpers) vffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.wffn(name, formatAt, minArgs, func(v runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		return f(v, a)
	}, target...)
}

// wffn is the shared fmt-call wrapper: the inner fn receives both the
// host-shaped args and the flat script args (before fmtArg unboxing).
func (h *hostHelpers) wffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
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
		return scriptVal(r), nil
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
		if isArrayTyp(t.Typ) {
			fv := &fmtValue{c: c, x: t}
			return badVerb('p', typedefSpelling(t.Typ), fv.renderList(t, 'v', zs))
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
		return badVerb('p', typedefSpelling(t.Def), fv.render('v', zs))
	case *runtime.Named:
		return ptrSpelling(c, t.V)
	case *runtime.UConst:
		if nv, err := uconstNative(t); err == nil {
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
	td, ok := peelNil(x)
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
			return "(" + typedefSpelling(td) + ")(nil)"
		case runtime.KindSlice:
			if byteSliceTyp(td) {
				return "[]byte(nil)"
			}
		}
	}
	return typedefSpelling(td) + "(nil)"
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
		if nv, err := uconstNative(t); err == nil {
			return scriptScalarName(nv)
		}
	case *runtime.Named:
		return typedefSpelling(t.Typ)
	case *runtime.GoValue:
		return scriptTypeString(t)
	}
	return fmt.Sprintf("%T", x)
}

// isArrayTyp reports whether td denotes a fixed-length array ([N]T) —
// arrays have no addressable backing pointer under %p.
func isArrayTyp(td *runtime.TypeDef) bool {
	at, ok := td.TypeExpr().(*ast.ArrayType)
	return ok && at.Len != nil
}

// byteSliceTyp reports whether td's element type is byte/uint8 — the one
// slice fmt renders as text rather than a bracketed list.
func byteSliceTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Elem != nil {
		return typedefSpelling(td.Elem) == "uint8"
	}
	at, ok := td.TypeExpr().(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	return ok && (id.Name == "byte" || id.Name == "uint8")
}

// elemTyp wraps a composite element's type AST so leaf rendering can
// spell bad-verb markers with the declared element type. Only idents
// naming a declared type carry the package — builtin type names spell
// unqualified like Go.
func elemTyp(e ast.Expr, pkg *runtime.Package) *runtime.TypeDef {
	if e == nil {
		return nil
	}
	if id, ok := e.(*ast.Ident); ok {
		td := &runtime.TypeDef{Name: id.Name, Kind: runtime.KindNamedBasic}
		if pkg != nil && pkg.Index != nil {
			if itd, ok := pkg.Index.Types[id.Name]; ok && itd.Decl != nil {
				// an alias name never spells — the declared slot's
				// type is the target's (`type A = B` IS B).
				if ts, ok := itd.Decl.Spec.(*ast.TypeSpec); ok && ts.Assign.IsValid() {
					return elemTyp(ts.Type, pkg)
				}
				td.Pkg = pkg
			}
		}
		return td
	}
	return &runtime.TypeDef{Pkg: pkg, Anon: e}
}

// sliceElemTyp resolves a slice/array typedef's element typedef.
func sliceElemTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if td.Elem != nil {
		return td.Elem
	}
	if at, ok := td.TypeExpr().(*ast.ArrayType); ok {
		return bindTyp(td, elemTyp(at.Elt, td.Pkg))
	}
	return nil
}

// mapElemTyps resolves a map typedef's key and value typedefs.
func mapElemTyps(td *runtime.TypeDef) (key, val *runtime.TypeDef) {
	if mt, ok := td.TypeExpr().(*ast.MapType); ok && td != nil {
		return bindTyp(td, elemTyp(mt.Key, td.Pkg)), bindTyp(td, elemTyp(mt.Value, td.Pkg))
	}
	return nil, nil
}

// fieldTypOf resolves the i'th field's typedef from a struct typedef's
// declaration — embedded fields count once and multi-name decls expand.
func fieldTypOf(td *runtime.TypeDef, i int) *runtime.TypeDef {
	st, ok := td.TypeExpr().(*ast.StructType)
	if !ok {
		return nil
	}
	n := 0
	for _, fd := range st.Fields.List {
		cnt := len(fd.Names)
		if cnt == 0 {
			cnt = 1
		}
		if i < n+cnt {
			return bindTyp(td, elemTyp(fd.Type, td.Pkg))
		}
		n += cnt
	}
	return nil
}

// bindTyp resolves a declared element typedef through the enclosing
// typedef's instantiation binds — a `Pair[error, any]` `Key K` field's
// declared type is `K`, but a value stored there statically has the
// bound argument's type. Composite field types keep the binds so a
// nested type-parameter name (a `[]K` field's element) resolves the
// same way one level down.
func bindTyp(td *runtime.TypeDef, et *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || et == nil || len(td.Binds) == 0 {
		return et
	}
	if et.Anon == nil && et.Spec == nil {
		if bv, ok := td.Binds[et.Name]; ok {
			if btd, ok := bv.(*runtime.TypeDef); ok {
				return btd
			}
		}
	}
	if len(et.Binds) == 0 {
		cp := et.CloneUncached()
		cp.Binds = td.Binds
		return cp
	}
	return et
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
	if !byteSliceTyp(v.Typ) {
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
	return typedefSpelling(td)
}

// nilIfaceTyp reports the interface typedef when x is a nil interface
// value — an iface-kind IfaceNil, possibly Named-wrapped — and nil for
// any other value. A nil interface has no dynamic type: %T spells <nil>
// at top level, while under a pointer the declared typedef still shows.
func nilIfaceTyp(x runtime.Value) *runtime.TypeDef {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	return runtime.IfaceTaggedNil(x)
}

// scriptTypeString spells a value's type the way Go's %T does —
// "[]int", "main.Point" — using the typedef, not the Go wrapper type.
func scriptTypeString(x runtime.Value) string {
	switch t := x.(type) {
	case nil, runtime.Nil:
		// a nil interface has no dynamic type — %T spells <nil>.
		return "<nil>"
	case *runtime.UConst:
		return t.DefaultName()
	case *runtime.Named:
		if nilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return typedefSpelling(t.Typ)
	case *runtime.Struct:
		return typedefSpelling(t.Def)
	case *runtime.Slice:
		return typedefSpelling(t.Typ)
	case *runtime.Map:
		return typedefSpelling(t.Typ)
	case *runtime.Chan:
		return typedefSpelling(t.Typ)
	case *runtime.TypedNil:
		return typedefSpelling(t.Typ)
	case *runtime.IfaceNil:
		if nilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return typedefSpelling(t.Typ)
	case *runtime.PanicNilError:
		return "*runtime.PanicNilError"
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(t); ok {
			// the pointee slot's stamped declared type names the
			// pointee's static type — `&i` on an interface-typed var
			// spells *main.I, not *main.S of its stored value. A ref
			// operand reads it through the ref itself: a FieldRef's
			// declared field type, an IndexRef's element type.
			if td := pointeeDeclTyp(t); td != nil {
				return "*" + typedefSpelling(td)
			}
			if c := asCell(dv); c != nil && c.Typ != nil {
				return "*" + typedefSpelling(c.Typ)
			}
			// *interface{} / *error keeps the pointee's declared name —
			// the pointer itself is the dynamic type, not <nil>.
			if td := nilIfaceTyp(dv); td != nil {
				return "*" + typedefSpelling(td)
			}
			return "*" + scriptTypeString(dv)
		}
		return "unsafe.Pointer"
	case *runtime.GoValue:
		if se, ok := t.V.(*scriptError); ok {
			return scriptTypeString(se.v)
		}
		switch t.V.(type) {
		case *jsonEncoder:
			return "*json.Encoder"
		case cleanupHandle:
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
		if rt := proxyHostTypeOf(t.V); rt != nil {
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
		default:
			if dv, ok := runtime.Deref(x); ok {
				x = dv
				continue
			}
			return "", false
		}
	}
}

// refDeclTyp recovers the declared type an &-operand points at when it
// arrives as a tracked ref: a FieldRef reads its field's declared
// typedef (an interface field spells *main.I of its stored concrete),
// an IndexRef its container's declared element typedef. A plain &var
// cell already stamps the same type on itself (cell.Typ), so refs
// share that answer. Nil when the ref kind or declared type is unknown.
func refDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch r := v.(type) {
	case *runtime.FieldRef:
		if st, idx, ok := r.Find(); ok && st != nil && st.Def != nil {
			return fieldTypOf(st.Def, idx)
		}
	case *runtime.IndexRef:
		if s := r.Slice(); s != nil {
			return sliceElemTyp(s.Typ)
		}
		if m := r.Map(); m != nil {
			_, et := mapElemTyps(m.Typ)
			return et
		}
	}
	return nil
}

// valueDeclTyp recovers the declared typedef stamped on a runtime
// value — a struct's Def, a named value's tag, a container's Typ, a
// nil's declared type. Refs and cells carry no typedef of their own (a
// cell's Typ stamps its ELEMENT — see pointeeDeclTyp), and untyped
// literals or host boxes report nil.
func valueDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch x := v.(type) {
	case *runtime.Struct:
		return x.Def
	case *runtime.Named:
		return x.Typ
	case *runtime.Slice:
		return x.Typ
	case *runtime.Map:
		return x.Typ
	case *runtime.Chan:
		return x.Typ
	case *runtime.TypedNil:
		return x.Typ
	case *runtime.IfaceNil:
		return x.Typ
	}
	return nil
}

// pointeeDeclTyp is the declared type an &-operand points at: a tracked
// ref answers through its declared field/element typedef (refDeclTyp),
// a plain &var cell through its own stamped Typ. Nil when the pointee's
// declared type is unknown.
func pointeeDeclTyp(v runtime.Value) *runtime.TypeDef {
	if td := refDeclTyp(v); td != nil {
		return td
	}
	if c := asCell(v); c != nil {
		return c.Typ
	}
	return nil
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
func rewriteTypeVerbs(spec string, a []any, rawArgs []runtime.Value, formatAt int, c runtime.VMCaller) (string, []any) {
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
		sb.WriteByte('%')
		// flags/width survive; the [n] index does not — newArgs is
		// emitted in directive order, so every operand is implicit.
		tail := spec[d.start+1 : d.end-1]
		for k := 0; k < len(tail); {
			if tail[k] == '[' {
				for k < len(tail) && tail[k] != ']' {
					k++
				}
				k++
				continue
			}
			sb.WriteByte(tail[k])
			k++
		}
		for _, sp := range d.stars {
			na = append(na, a[off+sp])
		}
		switch d.verb {
		case 'T':
			sb.WriteByte('s')
			na = append(na, scriptTypeString(vals[d.pos]))
		case 'p':
			sb.WriteByte('s')
			na = append(na, ptrSpelling(c, vals[d.pos]))
		default:
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

// constAtWidth materializes a Named value's still-constant payload at
// its declared type, when the caller can run the VM-side conversion —
// display and fmtArg share it so a UConst never spills its untyped
// reading into output. ok is false when x holds no UConst, the caller
// can't materialize, or the conversion fails.
func constAtWidth(v runtime.CallerRef, x *runtime.Named) (runtime.Value, bool) {
	u, ok := x.V.(*runtime.UConst)
	if !ok {
		return nil, false
	}
	mc, ok := v.(runtime.ConstMaterializer)
	if !ok {
		return nil, false
	}
	mv, err := mc.MaterializeConstErr(u, x.Typ)
	if err != nil {
		return nil, false
	}
	return mv, true
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
		if mv, ok := constAtWidth(v, x); ok {
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
		nv := mustNativeConst(x)
		if i, ok := nv.(int64); ok {
			return int(i)
		}
		return nv
	case *runtime.GoValue:
		if rv, ok := x.V.(*minireflect.RValue); ok {
			return fmtRValue(v, rv)
		}
		return goNative(x)
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil named host slice (net.IP) crosses as the host nil so
		// fmt runs its String method on the nil receiver ("<nil>")
		if rt := vm.HostNilType(runtime.BoxedNilTyp(x)); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		if tn, ok := x.(*runtime.TypedNil); ok {
			if rt := vm.HostNilType(tn.Typ); rt != nil {
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
		return goNative(u)
	case runtime.Value:
		return fmtArg(c, u)
	default:
		return u
	}
}

// typedefSpelling renders a typedef for %T/#v output — it delegates to
// the canonical display speller.
func typedefSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "interface{}"
	}
	return runtime.DisplayName(td)
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
			return typedefSpelling(v.Typ)
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
		if float32Tag(v.Typ) {
			if fv, ok := v.V.(float64); ok {
				return fmt.Sprintf(formatOf(f, verb), float32(fv))
			}
			if uc, ok := v.V.(*runtime.UConst); ok {
				if nv, err := uconstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return fmt.Sprintf(formatOf(f, verb), float32(fv))
					}
				}
			}
		}
		// a complex128 carrying a complex64 tag rounds each half to
		// float32 — same gap as float32, one level wider.
		if complex64Tag(v.Typ) {
			if cv, ok := complex64Value(v.V); ok {
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
			return fmt.Sprintf("(*%s)(%p)", scriptTypeString(dv), v)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Struct:
		switch verb {
		case 'T':
			return typedefSpelling(v.Def)
		case 'q':
			return strconv.Quote(s.renderValue(x, 'v', f))
		}
		parts := make([]string, len(v.Fields))
		for i, e := range v.Fields {
			fv := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: fieldTypOf(v.Def, i), depth: s.depth + 1}).render(elemVerb(verb), nested(f))
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
			return typedefSpelling(v.Def) + "{" + body + "}"
		}
		return "{" + body + "}"
	case *runtime.Slice:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'p':
			if isArrayTyp(v.Typ) {
				return badVerb(verb, typedefSpelling(v.Typ), s.renderList(v, 'v', f))
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
			if f.Flag('#') && byteSliceTyp(v.Typ) {
				if bs, ok := sliceBytes(v); ok {
					spell := typedefSpelling(v.Typ)
					if v.Typ.Name == "" && s.depth == 0 && !isArrayTyp(v.Typ) {
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
			return typedefSpelling(v.Typ)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		// Go's fmt prints maps in sorted-key order (fmtsort), not
		// insertion order — sort a copy of Order the same way.
		order := append([]runtime.Value{}, v.Order...)
		sort.SliceStable(order, func(i, j int) bool {
			a, b := order[i], order[j]
			if lessScript(a, b) {
				return true
			}
			if lessScript(b, a) {
				return false
			}
			// unordered kinds (bool, composites, mixed): order by the
			// rendered key like fmtsort's fallback.
			ka := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: a, depth: s.depth + 1}).render('v', nested(f))
			kb := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: b, depth: s.depth + 1}).render('v', nested(f))
			return ka < kb
		})
		kt, vt := mapElemTyps(v.Typ)
		ev := elemVerb(verb)
		parts := make([]string, 0, len(order))
		for _, k := range order {
			e, _ := v.Get(k)
			kr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: k, et: kt, depth: s.depth + 1}).render(ev, nested(f))
			vr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: vt, depth: s.depth + 1}).render(ev, nested(f))
			parts = append(parts, kr+":"+vr)
		}
		if f.Flag('#') {
			return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
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
			return typedefSpelling(v.Typ)
		}
		if rt := vm.HostNilType(v.Typ); rt != nil {
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
			return typedefSpelling(v.Typ)
		}
		if rt := vm.HostNilType(v.Typ); rt != nil {
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
			return typedefSpelling(v.Typ)
		case 'v':
			// %#v spells pointer-shaped values as (T)(0xADDR).
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", chanTypSpelling(v.Typ), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, typedefSpelling(v.Typ), fmt.Sprintf("%p", v))
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
			if rt := proxyHostTypeOf(v.V); rt != nil {
				return rt.String()
			}
		}
		return fmt.Sprintf(formatOf(f, verb), v.V)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return typedefSpelling(v)
	}
	return s.leaf(x, verb, f)
}

// renderList renders a slice's bracketed element list — every verb
// descends elementwise like Go's fmt ([]int under %q quotes each element
// as a char, under %s each becomes a %!s marker).
func (s *fmtValue) renderList(v *runtime.Slice, verb rune, f fmt.State) string {
	et := sliceElemTyp(v.Typ)
	ev := elemVerb(verb)
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: et, depth: s.depth + 1}).render(ev, nested(f))
	}
	if f.Flag('#') && verb == 'v' {
		return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// leaf formats a scalar value: compatible verbs go to host fmt after
// normalizing to the script-canonical Go type (script ints are int64 but
// spell int), incompatible verbs produce Go's %!verb(type=value) marker
// spelled with the declared element type when the leaf has one.
func (s *fmtValue) leaf(x runtime.Value, verb rune, f fmt.State) string {
	if u, ok := x.(*runtime.UConst); ok {
		x = mustNativeConst(u)
	}
	if leafBadVerb(x, verb) {
		name := ""
		// an interface-typed element (incl. any) names the value's
		// dynamic type, not the interface — []any{1} gives %!v(int=1)
		if s.et != nil && s.et.Name != "any" {
			if _, isIface := s.et.TypeExpr().(*ast.InterfaceType); !isIface {
				name = typedefSpelling(s.et)
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
		if byteSliceTyp(td) {
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
		return badVerb(verb, typedefSpelling(td), "<nil>")
	}
	return "<nil>"
}
