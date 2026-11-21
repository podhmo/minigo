package stdjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"sync/atomic"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	"github.com/podhmo/minigo/runtime"
)

// typeStr is the %T speller every engine installs — the same pure
// function each time, so the store races harmlessly, but we still go
// through atomic.Value to keep the race detector quiet when engines
// install concurrently.
var typeStr atomic.Value // func(runtime.Value) string

func typeStrOf(x runtime.Value) string {
	return typeStr.Load().(func(runtime.Value) string)(x)
}

func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters, ts func(runtime.Value) string) {
	typeStr.Store(ts)
	bind("encoding/json", map[string]runtime.Value{
		"Number": &runtime.TypeDef{Name: "encoding/json.Number", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("string"), HostNew: func() any { return json.Number("") }},
		// the Marshaler/Unmarshaler interface types — gc dispatches
		// through an interface-holding target (`var i json.Unmarshaler
		// = new(U); json.Unmarshal(data, &i)`), which reaches the same
		// UnmarshalJSON member probe as a concrete pointer.
		"Marshaler": &runtime.TypeDef{Name: "encoding/json.Marshaler", Kind: runtime.KindInterface, MReqs: []string{"MarshalJSON"}},
		// *json.SyntaxError: the bound decoder returns the host error for
		// malformed input, so `err.(*json.SyntaxError)` asserts on it.
		"SyntaxError": stdlib.HostType("encoding/json.SyntaxError", func() any { return &json.SyntaxError{} }),
		// *json.UnsupportedTypeError: Marshal's unsupported-type failure
		// surfaces the host error, so `err.(*json.UnsupportedTypeError)`
		// and errors.As assert on it.
		"UnsupportedTypeError": stdlib.HostType("encoding/json.UnsupportedTypeError", func() any { return &json.UnsupportedTypeError{} }),
		// *json.MarshalerError: a script MarshalJSON/MarshalText failure
		// surfaces the host error (its Type is a scriptReflectType
		// spelling — a script type has no reflect.Type), so the same
		// assert and errors.As apply.
		"MarshalerError": stdlib.HostType("encoding/json.MarshalerError", func() any { return &json.MarshalerError{} }),
		"Unmarshaler":    &runtime.TypeDef{Name: "encoding/json.Unmarshaler", Kind: runtime.KindInterface, MReqs: []string{"UnmarshalJSON"}},
		"Encoder":        stdlib.HostType("encoding/json.Encoder", func() any { return &jsonEncoder{enc: json.NewEncoder(io.Discard)} }),
		// NewEncoder boxes a jsonEncoder — the wrapper, not the host
		// *json.Encoder — so Encode takes its raw script arg through
		// goJSON like Marshal (see jsonEncoder.ScriptMethod).
		"NewEncoder": &runtime.BuiltinFunc{Name: "json.NewEncoder", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.NewEncoder needs 1 arg, got %d", len(args))
			}
			w, err := ad.AsWriterVM(v, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: &jsonEncoder{c: v, enc: json.NewEncoder(w)}}, nil
		}},
		// Decoder/NewDecoder mirror the encoder pair: NewDecoder boxes a
		// jsonDecoder (see its ScriptMethod for the script-arg Decode).
		"Decoder": stdlib.HostType("encoding/json.Decoder", func() any {
			return &jsonDecoder{dec: json.NewDecoder(bytes.NewReader(nil))}
		}),
		"NewDecoder": &runtime.BuiltinFunc{Name: "json.NewDecoder", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.NewDecoder needs 1 arg, got %d", len(args))
			}
			r, err := ad.AsReaderVM(v, args[0])
			if err != nil {
				return nil, err
			}
			// the input is buffered eagerly: jsonInject's error-position
			// collation walks the byte stream, and every consumer of the
			// bound decoder works on bounded files/buffers. A streaming
			// reader (a socket mid-response) blocks here — unlike gc.
			buf, err := io.ReadAll(r)
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: &jsonDecoder{dec: json.NewDecoder(bytes.NewReader(buf))}}, nil
		}},
		// *json.UnmarshalTypeError: a field type mismatch can surface the
		// host error, so `err.(*json.UnmarshalTypeError)` and errors.As
		// assert on it.
		"UnmarshalTypeError": stdlib.HostType("encoding/json.UnmarshalTypeError", func() any { return &json.UnmarshalTypeError{} }),
		// RawMessage carries its host method set (MarshalJSON /
		// UnmarshalJSON), so the nested unmarshaler probe stores raw
		// literals into it.
		"RawMessage": stdlib.HostType("encoding/json.RawMessage", func() any { return &json.RawMessage{} }),
		// Marshal/MarshalIndent take raw runtime args — h.fn's hostbridge.GoNative
		// would stringify *runtime.Struct before goJSON can field-map it.
		"Marshal": &runtime.BuiltinFunc{Name: "json.Marshal", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.Marshal needs 1 arg, got %d", len(args))
			}
			b, err := json.Marshal(goJSON(v, args[0]))
			err = jsonUnsupportedOf(err)
			var se *jsonScriptErr
			if errors.As(err, &se) {
				return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal([]byte{}), stdlib.ErrVal(jsonMarshalerErr(v, se))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(b), stdlib.ErrVal(err)}}, nil
		}},
		"MarshalIndent": &runtime.BuiltinFunc{Name: "json.MarshalIndent", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("json.MarshalIndent needs 3 args, got %d", len(args))
			}
			b, err := json.MarshalIndent(goJSON(v, args[0]), stdlib.Str(hostbridge.GoNative(args[1])), stdlib.Str(hostbridge.GoNative(args[2])))
			err = jsonUnsupportedOf(err)
			var se *jsonScriptErr
			if errors.As(err, &se) {
				return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal([]byte{}), stdlib.ErrVal(jsonMarshalerErr(v, se))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(b), stdlib.ErrVal(err)}}, nil
		}},
		// Two shapes: Unmarshal(data) decodes into the runtime value tree
		// (maps/slices/scalars) and returns it; Unmarshal(data, &v) decodes
		// into the pointer target like the stdlib and returns just error.
		"Unmarshal": &runtime.BuiltinFunc{Name: "json.Unmarshal", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 1 {
				var dec any
				err := json.Unmarshal(stdlib.ByteSlice(hostbridge.GoNative(args[0])), &dec)
				return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(jsonDeep(dec, false)), stdlib.ErrVal(err)}}, nil
			}
			if len(args) != 2 {
				return nil, fmt.Errorf("json.Unmarshal needs 1 or 2 args, got %d", len(args))
			}
			// gc: Unmarshal(nil) / Unmarshal(non-pointer T) is an
			// InvalidUnmarshalError before any decoding happens.
			if _, ok := runtime.Deref(args[1]); !ok {
				atd := v.TypeOf(args[1])
				if atd == nil {
					return stdlib.ErrVal(fmt.Errorf("json: Unmarshal(nil)")), nil
				}
				return stdlib.ErrVal(fmt.Errorf("json: Unmarshal(non-pointer %s)", runtime.DisplayName(atd))), nil
			}
			data := stdlib.ByteSlice(hostbridge.GoNative(args[0]))
			// gc validates the input up front (checkValid): a syntax
			// error reports before any UnmarshalJSON dispatch. The DOM
			// parse itself runs that validation and always reads number
			// literals with UseNumber semantics so jsonScalar parses the
			// literal exactly.
			node, err := jsonDomParse(data)
			if err != nil {
				return stdlib.ErrVal(err), nil
			}
			return jsonInject(v, data, node, args[1], jsonDecFlags{})
		}},
		"Valid": h.Fn("json.Valid", func(a []any) (any, error) { return json.Valid(stdlib.ByteSlice(a[0])), nil }, json.Valid),
		// Indent/Compact (indent.go) take a *bytes.Buffer destination
		// like gc — a generic io.Writer would diverge.
		"Indent":  indentBind,
		"Compact": compactBind,
	})
}

// jsonInject writes the already-decoded dec into the pointer target —
// Unmarshal's post-validation body, shared with Decoder.Decode whose
// data is the one value's literal. A target whose method set offers
// UnmarshalJSON decodes itself — gc's indirect() hands it the raw
// literal instead of walking by shape. The pointer's method set is
// checked, so both value- and pointer-receiver methods qualify.
// gc's indirect() allocates a nil pointer before the call — without it
// the method derefs nil. The member probe runs after the realloc so
// the bound receiver sees the fresh pointee.
// jsonDecFlags carries the decoder-side flags into jsonInject: Number
// keeps json.Number on `any` targets (the caller's UseNumber), Strict
// rejects keys no declared field claims (DisallowUnknownFields).
type jsonDecFlags struct {
	Number bool
	Strict bool
}

func jsonInject(v runtime.VMCaller, data []byte, node *jsonDom, target runtime.Value, flags jsonDecFlags) (runtime.Value, error) {
	{
		if prior0, _ := runtime.Deref(target); jsonNilish(prior0) {
			if ptd := derefTyp(v.TypeOf(target)); ptd != nil && ptd.Kind == runtime.KindPointer {
				if z := jsonAllocPointee(v, ptd); z != nil {
					runtime.SetRef(target, z)
				}
			}
		}
		if m, ok := runtime.IfaceMember(v, target, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			r, cerr := v.Call(m, []runtime.Value{stdlib.ScriptVal(data)})
			if cerr != nil {
				return nil, cerr
			}
			return jsonErrResult(r), nil
		}
		prior, _ := runtime.Deref(target)
		ectx := &jsonErrCtx{keepNum: flags.Number, strict: flags.Strict}
		if std := derefTyp(v.TypeOf(target)); std != nil && std.Kind == runtime.KindStruct && std.Name != "" {
			ectx.strct = jsonBareName(std)
		}
		sv := jsonShape(v, node, derefTyp(v.TypeOf(target)), prior, ectx)
		if !runtime.SetRef(target, sv) {
			return nil, fmt.Errorf("json: cannot assign to %T", target)
		}
		// Like gc, the shaped value is written even on type errors —
		// successfully-decoded fields land and the reported error is
		// the FIRST in input order, not decode order.
		if ectx.fatal != nil {
			return nil, ectx.fatal
		}
		if len(ectx.callErrs) > 0 {
			// Every candidate — by-shape type errors and script call
			// errors alike — competes by input position; gc reports
			// whichever member the input reached first, not the
			// method's error unconditionally.
			best := -1
			var pickErr error
			var pickCall runtime.Value
			for _, e := range ectx.errs {
				if e.pos >= 0 && (best == -1 || e.pos < best) {
					best, pickErr, pickCall = e.pos, e.err, nil
				}
			}
			for _, ce := range ectx.callErrs {
				if ce.pos >= 0 && (best == -1 || ce.pos < best) {
					best, pickErr, pickCall = ce.pos, nil, ce.ev
				}
			}
			if pickCall != nil {
				return pickCall, nil
			}
			if pickErr != nil {
				return stdlib.ErrVal(pickErr), nil
			}
			// no path resolved — the first-recorded call error
			// stands, matching decode order.
			return ectx.callErrs[0].ev, nil
		}
		return stdlib.ErrVal(jsonInputErr(ectx.errs)), nil
	}
}

// jsonSeen marks a container pointer on the current marshal path —
// gc's ptrSeen equivalent. A repeat visit means a reference cycle.

// jsonMarshalerErr renders gc's MarshalerError text for a script
// MarshalJSON/MarshalText failure — `json: error calling MarshalJSON
// for type *T: <err>` (gc's encoder always invokes through an
// address, so the named type spells *T even for a value receiver).
func jsonMarshalerErr(c runtime.TypeMemberCaller, se *jsonScriptErr) error {
	name := "unknown"
	if vt := c.TypeOf(se.operand); vt != nil {
		name = runtime.DisplayName(derefTyp(vt))
		if vt.Kind != runtime.KindPointer {
			name = "*" + name
		}
	}
	// the script error stays the cause — errors.Is/As reach it like
	// MarshalerError's Unwrap.
	me := &json.MarshalerError{Type: scriptReflectType{name: name}, Err: hostbridge.HostErrOf(c, se.sv)}
	if se.method == "MarshalJSON" {
		return me
	}
	// sourceFunc is unexported and an empty one reports "MarshalJSON",
	// so a MarshalText failure wraps its MarshalerError instead —
	// errors.As still matches it through Unwrap.
	return &jsonMarshalTextErr{me: me}
}

func jsonSeen(seen map[any]bool, p any) bool {
	if seen[p] {
		return true
	}
	seen[p] = true
	return false
}

// jsonCycle stands in for a container revisited on the marshal path:
// its MarshalJSON raises gc's exact UnsupportedValueError ("encountered
// a cycle via <type>", naming the container gc's ptrSeen trips on)
// through the host encoder's error channel.
type jsonCycle struct{ via string }

const jsonCyclePrefix = "encountered a cycle via "

// jsonFieldKey maps a struct field to its JSON object key: the `json`
// tag's name wins, a "-" tag skips the field, `omitempty` drops empty
// values on marshal, `,string` wraps scalar fields in quoted literals,
// and an absent tag falls back to the field name (matching
// encoding/json's defaulting).
func jsonFieldKey(def *runtime.TypeDef, name string) (key string, named, omitEmpty, asString, skip bool) {
	if def != nil && def.FTags != nil {
		if tag, ok := def.FTags[name]; ok {
			j := reflect.StructTag(tag).Get("json")
			if j == "-" {
				return "", false, false, false, true
			}
			omit := false
			asStr := false
			if i := strings.IndexByte(j, ','); i >= 0 {
				for _, opt := range strings.Split(j[i+1:], ",") {
					switch opt {
					case "omitempty":
						omit = true
					case "string":
						asStr = true
					}
				}
				j = j[:i]
			}
			if j != "" {
				return j, true, omit, asStr, false
			}
			// an empty name part (`json:",omitempty"`) keeps the
			// field's own name but still applies its options.
			return name, false, omit, asStr, false
		}
	}
	return name, false, false, false, false
}

// jsonFieldIsIface reports whether struct field i is declared with an
// interface type — `,string` on an interface field is ignored on
// marshal (the option keys off the declared kind, not the value's).
// The declared field type comes from the caller's FieldTypes hook —
// the same resolution field stores use, so builtin any/error
// spellings, interface literals and named interface types all answer
// through the resolved typedef's Kind. A hole typedef for an
// unresolvable declaration keeps its AST: an interface literal there
// still counts.
func jsonFieldIsIface(c runtime.FieldTyper, def *runtime.TypeDef, i int) bool {
	if c == nil {
		return false
	}
	fts, err := c.FieldTypes(def)
	if err != nil || i < 0 || i >= len(fts) {
		return false
	}
	ft := fts[i]
	if ft == nil {
		return false
	}
	if ft.Kind == runtime.KindInterface {
		return true
	}
	_, ok := ft.TypeExpr().(*ast.InterfaceType)
	return ok
}

// jsonIsEmpty mirrors encoding/json's isEmptyValue for `omitempty`:
// false, 0, "", and empty/nil containers are dropped.
func jsonIsEmpty(v any) bool {
	switch x := v.(type) {
	case *runtime.Named:
		return jsonIsEmpty(x.V)
	case *runtime.Cell:
		return jsonIsEmpty(x.Elem)
	case bool:
		return !x
	case int64:
		return x == 0
	case float64:
		return x == 0
	case string:
		return x == ""
	case *runtime.Slice:
		return len(x.Elems) == 0
	case *runtime.Map:
		return len(x.Pairs) == 0
	case runtime.Nil, *runtime.IfaceNil, *runtime.TypedNil:
		return true
	}
	return false
}

// orderedObject marshals struct fields in declaration order —
// encoding/json emits struct fields in source order, unlike map keys
// which it sorts, so a plain map[string]any loses the field order.
type orderedObject struct {
	keys []string
	vals []any
}

// jsonErrCtx is the bound decoder's mirror of encoding/json's
// errorContext: strct is the bare name of the root struct target (empty
// for non-struct roots — go1.27 reports Struct only for the root's own
// type), path is the field/index/key chain from the root to the node
// currently being shaped, err keeps the first type error, and errs
// collects every recorded one with its path — the reported error is
// the earliest in input order like gc (a map decode has lost it).
type jsonErrCtx struct {
	strct string
	path  []string
	err   error
	errs  []jsonErrAt
	// keepNum leaves json.Number on `any` targets (UseNumber);
	// strict records an error for input keys no field claims
	// (DisallowUnknownFields).
	keepNum bool
	strict  bool
	// callErrs records every non-nil error a script UnmarshalJSON /
	// UnmarshalText returned — script Values (not
	// *jsonUnmarshalTypeError), each tagged with its member path. gc
	// reports the first error in INPUT order across all error kinds,
	// so each call error competes against the by-shape candidates by
	// position — a second failing member must not replace the first.
	callErrs []jsonCallErr
	// poss tracks the input position of the member pair / array
	// element currently being decoded — each error inherits the
	// innermost enclosing pair's stream ordinal, which is what gc's
	// sequential decoder would report as "first".
	poss []int
	// fatal records a failed METHOD CALL itself (a script panic
	// surfacing as a call error) — gc lets it propagate, so it must
	// not be swallowed into by-shape decoding.
	fatal error
}

// jsonScriptErr ferries a script marshaler's error return through
// encoding/json's own error channel: the runtime value is not an
// error, so the wrapper presents it as one and the Marshal intrinsic
// unwraps it back out. operand is the value whose method failed —
// gc's MarshalerError names that nested type, not the marshal's
// top-level argument.
// jsonEncoder backs the bound encoding/json.Encoder: the host encoder
// writes to the script-supplied writer while Encode marshals its raw
// script arg through goJSON exactly like json.Marshal. A reflected
// Encode(v any) would see vm's host `any` projection instead — struct
// tags, Marshaler dispatch and MarshalText map keys lost, typed
// containers left unwalked, and the call's write-back replacing the
// caller's script maps. SetIndent/SetEscapeHTML forward as-is.
type jsonEncoder struct {
	c   runtime.VMCaller
	enc *json.Encoder
}

// jsonHostArg recovers the script value a host `any` argument was
// marshaled from: vm's reflect-call marshaling projects a script
// struct to an anonymous *struct{...} recorded in vm's structOrigs.
// Restoring the original lets Encode apply goJSON — the projected
// struct alone would lose tags, omitempty and Marshaler dispatch.
// Container args (slices, maps) walk element-wise for nested
// projections; values with no projection pass through unchanged.
func jsonHostArg(v any) any {
	return jsonHostArgSeen(v, map[unsafe.Pointer]bool{})
}

// jsonHostArgSeen is jsonHostArg tracking live container pointers: a
// cyclic host container would otherwise recurse forever. The cycle is
// left in place — goJSONSeen's own set reports it as gc's
// UnsupportedValueError.
func jsonHostArgSeen(v any, seen map[unsafe.Pointer]bool) any {
	if orig, ok := projectedOrig(v); ok {
		return orig
	}
	switch c := v.(type) {
	case []any:
		out := make([]any, len(c))
		if len(c) > 0 {
			p := reflect.ValueOf(c).UnsafePointer()
			if seen[p] {
				return c
			}
			seen[p] = true
			defer delete(seen, p)
		}
		for i, e := range c {
			out[i] = jsonHostArgSeen(e, seen)
		}
		return out
	case map[any]any:
		out := make(map[any]any, len(c))
		if len(c) > 0 {
			p := reflect.ValueOf(c).UnsafePointer()
			if seen[p] {
				return c
			}
			seen[p] = true
			defer delete(seen, p)
		}
		for k, e := range c {
			out[k] = jsonHostArgSeen(e, seen)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(c))
		if len(c) > 0 {
			p := reflect.ValueOf(c).UnsafePointer()
			if seen[p] {
				return c
			}
			seen[p] = true
			defer delete(seen, p)
		}
		for k, e := range c {
			out[k] = jsonHostArgSeen(e, seen)
		}
		return out
	}
	return v
}

// projectedOrig reports the original script value a vm host `any`
// projection stands for — the *struct{...} structDataHost mints,
// recorded in vm's structOrigs.
func projectedOrig(v any) (runtime.Value, bool) {
	return hostbridge.StructOrig(v)
}

// jsonMarshalTextErr is the MarshalText half of a script marshaler
// failure: Error renders gc's text while Unwrap hands the real
// *json.MarshalerError to errors.As.
type jsonMarshalTextErr struct {
	me *json.MarshalerError
}

// scriptReflectType is a reflect.Type stand-in carrying a script type's
// display spelling — a *json.MarshalerError needs a Type, and its Error
// text is the only reader (it calls String). Any other method panics on
// the embedded nil.
type scriptReflectType struct {
	reflect.Type
	name string
}

// jsonMarshalResult extracts the ([]byte, error) a script MarshalJSON
// returns: ok=false when the call result is not the Marshaler shape
// (a same-named method with a different signature does not implement
// the interface, so the value falls back to by-shape encoding).
func jsonMarshalResult(r runtime.Value) (b []byte, e runtime.Value, ok bool) {
	t, isTuple := r.(*runtime.Tuple)
	if !isTuple || len(t.Elems) != 2 {
		return nil, nil, false
	}
	switch t.Elems[1].(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
	default:
		e = t.Elems[1]
	}
	b, ok = stdlib.ByteSlice(hostbridge.GoNative(t.Elems[0])), true
	return b, e, ok
}

// jsonCallMethod invokes a script unmarshaler member (UnmarshalJSON
// or UnmarshalText) with the literal bytes it should decode,
// recording a call panic as fatal — gc lets it propagate — and a
// non-nil error return as the competing callErr at the current path.
// ok=false means the call itself blew up and decoding must stop.
func jsonCallMethod(c runtime.ScriptCaller, m runtime.Value, lit []byte, ectx *jsonErrCtx) bool {
	r, cerr := c.Call(m, []runtime.Value{stdlib.ScriptVal(lit)})
	if cerr != nil {
		if ectx.fatal == nil {
			ectx.fatal = cerr
		}
		return false
	}
	if ev := jsonErrResult(r); ev != runtime.NIL {
		ectx.callErrs = append(ectx.callErrs, jsonCallErr{ev: ev, path: append([]string{}, ectx.path...), pos: ectx.posTop()})
	}
	return true
}

// jsonCallErr is one script unmarshaler's error return plus the path
// of the member it fired for — the input-order competition resolves
// it against the by-shape candidates in errs.
type jsonCallErr struct {
	ev   runtime.Value
	path []string
	pos  int
}

// jsonUnmarshalShape reports whether m spells func([]byte) error —
// the loose arity check applied before calling a script UnmarshalJSON:
// a same-named method with a different signature does not implement
// json.Unmarshaler, so its value decodes by shape. Host-callable
// methods carry no Decl and are assumed conforming.
func jsonUnmarshalShape(m runtime.Value) bool {
	b, ok := m.(*runtime.BoundMethod)
	if !ok {
		return true
	}
	if b.Fn == nil || b.Fn.Decl == nil || b.Fn.Decl.Type == nil {
		return false
	}
	ft := b.Fn.Decl.Type
	n := 0
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			if len(f.Names) == 0 {
				n++
			} else {
				n += len(f.Names)
			}
		}
	}
	return n == 1 && ft.Results != nil && len(ft.Results.List) == 1
}

// jsonAllocPointee builds the pointee gc's indirect() allocates for a
// nil pointer of type ptd: a cell holding the element's zero —
// recursively a fresh cell when the element is itself a pointer, so
// **T allocates a *T pointing at a fresh T.
func jsonAllocPointee(c runtime.ElemZeroTyper, ptd *runtime.TypeDef) runtime.Value {
	if ptd == nil || ptd.Kind != runtime.KindPointer {
		return nil
	}
	z := c.ElemZero(ptd)
	if z == nil {
		return nil
	}
	if et := c.TypeOf(z); et != nil && et.Kind == runtime.KindPointer {
		inner := jsonAllocPointee(c, et)
		if inner == nil {
			return nil
		}
		return &runtime.Cell{Elem: inner}
	}
	return &runtime.Cell{Elem: z}
}

// jsonNilish reports a nil pointer/interface payload — a cell or a
// named wrapper keeps the verdict of the value it carries so a nil
// *T inside one still reads as nil.
func jsonNilish(v runtime.Value) bool {
	_, ok := stdlib.PeelNil(v)
	return ok
}

// jsonErrResult unwraps the single `error` a script UnmarshalJSON
// returns: a nil-ish payload becomes NIL, anything else is the script
// error value itself.
func jsonErrResult(r runtime.Value) runtime.Value {
	if t, ok := r.(*runtime.Tuple); ok {
		if len(t.Elems) == 1 {
			r = t.Elems[0]
		}
	}
	switch r.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return runtime.NIL
	}
	return r
}

// jsonErrAt pins a recorded decode error to the field path it fired
// at — the reported error is the earliest in INPUT order like gc's
// UnmarshalTypeError, and a map[string]any decode no longer carries
// that order.
type jsonErrAt struct {
	err  error
	path []string
	pos  int
}

// posTop answers the stream ordinal of the innermost pair or element
// currently decoding; -1 outside any member (a root-level failure has
// no input position, so it competes only through record order).
func (x *jsonErrCtx) posTop() int {
	if len(x.poss) == 0 {
		return -1
	}
	return x.poss[len(x.poss)-1]
}

// jsonInputErr picks the error whose path is earliest in the raw
// input, falling back to the first recorded when a path never matched
// a member position (e.g. a root-level failure).
func jsonInputErr(errs []jsonErrAt) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0].err
	}
	best, pick := -1, errs[0]
	for _, e := range errs {
		if e.pos >= 0 && (best == -1 || e.pos < best) {
			best, pick = e.pos, e
		}
	}
	return pick.err
}

// jsonUnmarshalTypeError renders encoding/json's UnmarshalTypeError text.
// The bound decoder cannot build a reflect.Type, so the message is the
// contract — including go1.27's heuristic that elides "Go struct field"
// when the path's last element is a numeric index.
type jsonUnmarshalTypeError struct {
	value string // "bool", "number 1.5", "string", "array", "object"
	strct string // root struct's bare name, "" otherwise
	field string // dotted path from the root
	typ   string // display spelling of the failing typedef
	cause error  // underlying error (e.g. illegal base64 data)
}

// jsonValueName is the Value half of UnmarshalTypeError — the JSON kind
// that failed to decode ("bool", "array", ...).
func jsonValueName(dec any) string {
	switch dec.(type) {
	case bool:
		return "bool"
	case string:
		return "string"
	case []any, []*jsonDom:
		return "array"
	case map[string]any, []jsonMember:
		return "object"
	default:
		return "number"
	}
}

// jsonNumLit spells a decoded float64 like the literal text encoding/json
// reports in "cannot unmarshal number <lit>": 1.5 stays 1.5, 1e30 keeps
// the lowercase-e form (FormatFloat's "1e+30" loses the plus gc omits).
func jsonNumLit(f float64) string {
	return strings.Replace(strconv.FormatFloat(f, 'g', -1, 64), "e+", "e", 1)
}

// jsonBareName is the bare declared name used for errorContext.Struct —
// "BoolSchema" for main.BoolSchema, "" for anonymous typedefs.
func jsonBareName(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if i := strings.LastIndex(td.Name, "."); i >= 0 {
		return td.Name[i+1:]
	}
	return td.Name
}

// priorOr keeps the previous value on a decode failure, like gc leaving
// the destination untouched at a type mismatch; without a prior the
// typedef's zero is the fallback.
//
// unmarshalRecv picks the receiver cell for a script unmarshaler
// method: a Cell prior is already the addressable decode target, a nil
// prior decodes into a fresh zero slot, and a bare value decodes through
// a cell around it — a whole-value `*u = ...` assignment in the method
// reaches the field through the returned cell.
func unmarshalRecv(prior runtime.Value, c runtime.Zeroer, td *runtime.TypeDef) (runtime.Value, *runtime.Cell) {
	switch p := prior.(type) {
	case *runtime.Cell:
		// a typaless slot stores `*t = underlying` uncoerced — the decode
		// target's declared type is td, so fill it in.
		if p.Typ == nil {
			p.Typ = td
		}
		return p, nil
	case nil:
		cell := &runtime.Cell{Elem: c.Zero(td), Typ: td}
		return cell, cell
	default:
		// the decode slot is always addressable — a bare struct prior
		// still sees the pointer method set, like gc handing the method
		// the field's own address. Typ makes `*t = ...` coerce to the
		// declared type (e.g. []string -> a named slice type).
		cell := &runtime.Cell{Elem: prior, Typ: td}
		return cell, cell
	}
}

func priorOr(prior runtime.Value, c runtime.Zeroer, td *runtime.TypeDef) runtime.Value {
	if prior != nil {
		return prior
	}
	return c.Zero(td)
}

// jsonFieldEntry is one JSON-visible struct field: a declared field or
// a field promoted through anonymous embeds, plus the index path that
// reaches it through the struct's nested Fields slices
// (Fields[path[0]].Fields[path[1]]...).
type jsonFieldEntry struct {
	key    string // effective JSON key (tag name or field name)
	path   []int  // index chain into nested Fields
	tagged bool   // the key came from an explicit `json:"name"`
	depth  int    // embed hops — 0 for declared fields
	omit   bool   // `,omitempty`
	asStr  bool   // `,string`
}

// jsonStructFields flattens a struct typedef into its JSON-visible
// field entries — declared fields plus fields promoted through
// anonymous embeds, in declaration order with each embed expanded in
// place. A `json:"name"`-tagged embed is a NAMED member rather than
// promoted, and `,omitempty`/`,string` options on an anonymous embed
// are ignored (the promoted fields' own tags govern). A non-struct
// embed (e.g. a named int) behaves like a named field.
func jsonStructFields(c runtime.SeqTyper, td *runtime.TypeDef) []jsonFieldEntry {
	var out []jsonFieldEntry
	// Cycle guard on type IDENTITY, not the typedef pointer: embed
	// resolution (jsonResolveTypName) can rebuild a fresh typedef per
	// hop, so `type Node struct{ *Node }` would walk forever keyed on
	// *TypeDef. The declaring TypeSpec is shared across rebuilds and
	// pins the type.
	seenTd := map[*runtime.TypeDef]bool{}
	seenTs := map[*ast.TypeSpec]bool{}
	var walk func(st *runtime.TypeDef, prefix []int, depth int)
	walk = func(st *runtime.TypeDef, prefix []int, depth int) {
		if st == nil {
			return
		}
		if st.Spec != nil {
			if seenTs[st.Spec] {
				return
			}
			seenTs[st.Spec] = true
			defer delete(seenTs, st.Spec)
		} else {
			if seenTd[st] {
				return
			}
			seenTd[st] = true
			defer delete(seenTd, st)
		}
		for i, name := range st.Fields {
			key, named, omit, asStr, skip := jsonFieldKey(st, name)
			if skip {
				continue
			}
			// encoding/json ignores unexported fields; an unexported
			// embed survives only when it is a struct (or a pointer to
			// one), whose exported fields still promote.
			if !token.IsExported(name) {
				if !jsonEmbedded(st, i) {
					continue
				}
				et := jsonPeelPtr(c, jsonEmbedTyp(c, st, i))
				if et == nil {
					continue
				}
				if _, ok := et.TypeExpr().(*ast.StructType); !ok {
					continue
				}
			}
			p := append(append(make([]int, 0, len(prefix)+1), prefix...), i)
			if jsonEmbedded(st, i) && !named {
				if et := jsonPeelPtr(c, jsonEmbedTyp(c, st, i)); et != nil {
					if _, ok := et.TypeExpr().(*ast.StructType); ok {
						walk(et, p, depth+1)
						continue
					}
				}
			}
			out = append(out, jsonFieldEntry{
				key: key, path: p, tagged: named,
				depth: depth, omit: omit, asStr: asStr,
			})
		}
	}
	walk(td, nil, 0)
	return out
}

// jsonEmbedded reports whether Fields[i] is an embedded (anonymous)
// field — embedded specs are indexed through EmbedIdx.
func jsonEmbedded(st *runtime.TypeDef, i int) bool {
	for _, x := range st.EmbedIdx {
		if x == i {
			return true
		}
	}
	return false
}

// jsonEmbedTyp resolves the typedef of the i'th embedded field — the
// lazily-populated Embeds table first, then the spec's type AST
// through the caller's ResolveType hook (package index, LocalTypes,
// generic binds, import selectors), falling back to the package-level
// elemTyp approximation.
func jsonEmbedTyp(c runtime.ExprResolver, st *runtime.TypeDef, i int) *runtime.TypeDef {
	for k, ei := range st.EmbedIdx {
		if ei != i {
			continue
		}
		if k < len(st.Embeds) && st.Embeds[k] != nil {
			return st.Embeds[k]
		}
		if k < len(st.EmbedSpecs) {
			return jsonResolveTypName(c, st, st.EmbedSpecs[k])
		}
	}
	return nil
}

// jsonResolveTypName resolves a type expression to a typedef detailed
// enough to walk fields over, via the caller's ResolveType hook —
// generic binds, function-local decls, package-level decls and import
// selectors all resolve the way embedded-field specs do elsewhere in
// the VM. An unresolvable expression degrades to the elemTyp
// approximation rather than dropping the field.
func jsonResolveTypName(c runtime.ExprResolver, st *runtime.TypeDef, e ast.Expr) *runtime.TypeDef {
	// A type embedding itself names the decl's own name — the decl-time
	// LocalTypes snapshot predates it and the package index only holds
	// package-level types — so peel stars/parens and compare against
	// the enclosing typedef before the hook runs.
	x := e
	for {
		if sx, ok := x.(*ast.StarExpr); ok {
			x = sx.X
			continue
		}
		if px, ok := x.(*ast.ParenExpr); ok {
			x = px.X
			continue
		}
		break
	}
	if id, ok := x.(*ast.Ident); ok && st != nil && st.Spec != nil && st.Name == id.Name {
		return st
	}
	if c != nil && st != nil {
		if td, err := c.ResolveType(st, e); err == nil && td != nil {
			return td
		}
	}
	var pkg *runtime.Package
	if st != nil {
		pkg = st.Pkg
	}
	return stdlib.ElemTyp(e, pkg)
}

// jsonPeelPtr removes pointer levels from a typedef through the
// caller's ElemOf hook — the runtime Elem link, the declared *T AST
// and named pointer types all peel the way the engine's member walks
// do. When the hook can't resolve (nil caller, missing import) the
// declared AST's immediate pointee keeps the walk terminating.
func jsonPeelPtr(c runtime.ElemTyper, td *runtime.TypeDef) *runtime.TypeDef {
	for td != nil {
		if td.Kind != runtime.KindPointer {
			if _, ok := td.TypeExpr().(*ast.StarExpr); !ok {
				return td
			}
		}
		var et *runtime.TypeDef
		if c != nil {
			if r, err := c.ElemOf(td); err == nil {
				et = r
			}
		}
		if et == nil || et == td {
			if td.Elem != nil {
				et = td.Elem
			} else if st, ok := td.TypeExpr().(*ast.StarExpr); ok {
				et = stdlib.ElemTyp(st.X, td.Pkg)
			} else {
				return td
			}
		}
		td = et
	}
	return nil
}

// jsonEmbedStruct steps into the embedded field at index idx of s —
// following a live embedded struct, or allocating a nil pointer
// embed's pointee the way gc allocates on decode. ok=false when the
// field holds no struct value.
func jsonEmbedStruct(c runtime.ZeroElemTyper, s *runtime.Struct, idx int, ftd *runtime.TypeDef) (*runtime.Struct, bool) {
	f := s.Fields[idx]
	if st := runtime.StructOf(f); st != nil {
		return st, true
	}
	et := jsonPeelPtr(c, ftd)
	if et == nil {
		return nil, false
	}
	nv, ok := c.Zero(et).(*runtime.Struct)
	if !ok {
		return nil, false
	}
	if pc, ok := f.(*runtime.Cell); ok {
		pc.Elem = nv
		return nv, true
	}
	s.Fields[idx] = &runtime.Cell{Elem: nv}
	return nv, true
}

// jsonPathIsIface reports whether the leaf field reached by path is
// declared as an interface type — needed for `,string` marshal, which
// gc applies by declared kind.
func jsonPathIsIface(c runtime.ShapeTyper, td *runtime.TypeDef, path []int) bool {
	cur := td
	for h := 0; h < len(path)-1; h++ {
		cur = jsonPeelPtr(c, jsonEmbedTyp(c, cur, path[h]))
	}
	return jsonFieldIsIface(c, cur, path[len(path)-1])
}

// jsonDominant applies gc's dominantField reduction per JSON key:
// within the group sharing one exact key, the shallowest depth wins,
// a tagged field beats an untagged one at that depth, and a tie on
// both kills EVERY candidate in the group — the key then decodes
// nothing and marshals nothing.
func jsonDominant(ents []jsonFieldEntry) []bool {
	live := make([]bool, len(ents))
	byKey := map[string][]int{}
	for i := range ents {
		byKey[ents[i].key] = append(byKey[ents[i].key], i)
	}
	for _, cand := range byKey {
		minD := ents[cand[0]].depth
		for _, i := range cand[1:] {
			if ents[i].depth < minD {
				minD = ents[i].depth
			}
		}
		var keep []int
		for _, i := range cand {
			if ents[i].depth == minD {
				keep = append(keep, i)
			}
		}
		var tg []int
		for _, i := range keep {
			if ents[i].tagged {
				tg = append(tg, i)
			}
		}
		if len(tg) > 0 {
			keep = tg
		}
		if len(keep) == 1 {
			live[keep[0]] = true
		}
	}
	return live
}

// jsonMarshalLive keeps only the entries surviving jsonDominant — gc's
// ambiguous-key drop applies to marshaling too.
func jsonMarshalLive(ents []jsonFieldEntry) []jsonFieldEntry {
	live := jsonDominant(ents)
	out := make([]jsonFieldEntry, 0, len(ents))
	for i := range ents {
		if live[i] {
			out = append(out, ents[i])
		}
	}
	return out
}

// jsonShape converts a decoded JSON tree (map[string]any / []any /
// scalars from encoding/json) into the runtime shape a declared typedef
// expects: structs get their declared fields by json tag, numeric fields
// land as int64/float64 per the declared scalar, and slices/maps keep
// their typedef tags. A decoded value whose kind cannot go into the
// declared type records an UnmarshalTypeError-shaped failure on ectx and
// keeps prior (gc decodes past errors and preserves what was there);
// null nils pointer/slice/map/interface targets and is a no-op elsewhere.
func jsonShape(c runtime.VMCaller, node *jsonDom, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	// `type TBis T` delegates structure to T's typedef: shape by the
	// structural typedef, then wrap the produced value in the declared
	// tag — the same shape `var b TBis` zero takes (Named{TBis, T's
	// struct}). The declared type keeps its own method set: T's methods
	// do not promote, and a method on TBis stays callable. prior and
	// typed nils already carry the declared tag.
	dec := domVal(node)
	// gc calls UnmarshalJSON on the addressable decode target and hands
	// it the ORIGINAL literal — the node's own byte span — including the
	// "null" literal. UnmarshalText fires on string literals only.
	if td != nil && node != nil && td.Name != "" {
		recv, cell := unmarshalRecv(prior, c, td)
		if m, ok := runtime.IfaceMember(c, recv, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			if !jsonCallMethod(c, m, node.raw, ectx) {
				return prior
			}
			if cell != nil {
				return cell.Elem
			}
			return prior
		} else if m, ok := runtime.IfaceMember(c, recv, "UnmarshalText"); ok && dec != nil && jsonUnmarshalShape(m) {
			// UnmarshalJSON wins when both exist; UnmarshalText decodes
			// string literals only — gc hands the method the unquoted
			// text, and a non-string literal reports the value failing
			// for the type with gc's must-be-string cause.
			s, isStr := dec.(string)
			if !isStr {
				ectx.fail(jsonValueName(dec), td, errors.New("JSON value must be string type"))
				return priorOr(prior, c, td)
			}
			if !jsonCallMethod(c, m, []byte(s), ectx) {
				return prior
			}
			if cell != nil {
				return cell.Elem
			}
			return prior
		}
	}
	var wrap *runtime.TypeDef
	if td != nil && td.Name != "" && (td.Kind == runtime.KindNamedBasic || td.Kind == runtime.KindAlias) {
		if ut := jsonStructChase(c, td); ut != nil && ut != td {
			if ut.Kind == runtime.KindInterface || ut.IfaceBase {
				// interface-underlying names (any, `type A any`) store
				// the payload — the dynamic type decides, no tag.
				res := jsonShapeWalk(c, node, ut, prior, ectx)
				return res
			}
			cp := *ut
			cp.Name = td.Name
			cp.Pkg = td.Pkg
			if cp.TypeExpr() == nil {
				if bn := runtime.BasicNameOf(ut); bn != "" {
					// keep the chased basic name resolvable: the
					// renamed copy's TypeExpr is empty, so a
					// `type F32 float32` target would otherwise un
					// to "F32" and skip the basic conversions.
					cp.Anon = &ast.Ident{Name: bn}
				}
			}
			wrap, td = td, &cp
		}
	}
	res := jsonShapeWalk(c, node, td, prior, ectx)
	if wrap != nil {
		if n, ok := res.(*runtime.Named); ok && n.Typ == wrap {
			return res
		}
		if tn, ok := res.(*runtime.TypedNil); ok && tn.Typ == wrap {
			return res
		}
		return runtime.Tag(wrap, res)
	}
	return res
}

func jsonShapeWalk(c runtime.VMCaller, node *jsonDom, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	dec := domVal(node)
	if td == nil {
		return stdlib.ScriptVal(jsonDomDeep(node, ectx))
	}
	// an `any`-declared field's typedef is not KindInterface (it
	// resolves to the named `any`/`interface{}` decl) — read its
	// underlying name so the deep tree still lands, with the named
	// method probe already run above.
	isIface := td.Kind == runtime.KindInterface
	if u := jsonUnderlyingName(td); u == "any" || u == "interface{}" {
		isIface = true
	}
	if dec == nil {
		if isIface {
			return c.Zero(td)
		}
		switch td.Kind {
		case runtime.KindPointer, runtime.KindSlice, runtime.KindMap, runtime.KindInterface:
			if td.Kind == runtime.KindSlice && stdlib.IsArrayTyp(td) {
				// gc ignores null for fixed arrays: the value stays.
				return priorOr(prior, c, td)
			}
			return c.Zero(td)
		}
		return priorOr(prior, c, td)
	}
	switch td.Kind {
	case runtime.KindStruct:
		ms, ok := dec.([]jsonMember)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		z, ok := c.Zero(td).(*runtime.Struct)
		if !ok {
			ectx.fail("object", td, nil)
			return priorOr(prior, c, td)
		}
		var pf []runtime.Value
		if ps, ok := runtime.Unwrap(prior).(*runtime.Struct); ok && ps.Def == td {
			pf = ps.Fields
		}
		ents := jsonStructFields(c, td)
		// gc reduces each exact-name group by dominantField (dead
		// groups serve neither exact nor folded lookup), then decodes
		// members in input order: an exact-key hit wins, else the FIRST
		// surviving entry in declaration order whose name case-folds to
		// the input key. Duplicate keys each decode in turn — the later
		// occurrence writes over the earlier field, and under
		// DisallowUnknownFields every unmatched occurrence errors.
		live := jsonDominant(ents)
		exact := map[string]int{}
		for i := range ents {
			if live[i] {
				exact[ents[i].key] = i
			}
		}
		seen := map[int]bool{}
		for _, mb := range ms {
			i, kok := exact[mb.key]
			if !kok {
				i = -1
				for j := range ents {
					if live[j] && strings.EqualFold(ents[j].key, mb.key) {
						i = j
						break
					}
				}
			}
			if i < 0 {
				if ectx.strict {
					ectx.errs = append(ectx.errs, jsonErrAt{
						err:  fmt.Errorf("json: unknown field %q", mb.key),
						path: append(append([]string{}, ectx.path...), mb.key),
						pos:  mb.node.pos,
					})
				}
				continue
			}
			e := &ents[i]
			// errors path on the input's member spelling like gc's
			// UnmarshalTypeError (.a, not the Go field name A).
			ectx.path = append(ectx.path, mb.key)
			ectx.poss = append(ectx.poss, mb.node.pos)
			cur, curTd, curPf := z, td, pf
			reach := true
			for h := 0; h < len(e.path)-1 && reach; h++ {
				// walk into the embedded field at path[h], allocating a
				// nil pointer embed the way gc allocates on decode.
				idx := e.path[h]
				etd := jsonEmbedTyp(c, curTd, idx)
				if etd == nil {
					etd = stdlib.FieldTypOf(curTd, idx)
				}
				var pf2 runtime.Value
				if curPf != nil && idx < len(curPf) {
					pf2 = curPf[idx]
				}
				next, ok := jsonEmbedStruct(c, cur, idx, etd)
				if !ok {
					reach = false
					break
				}
				cur, curTd = next, jsonPeelPtr(c, etd)
				if ps2, ok := runtime.Unwrap(pf2).(*runtime.Struct); ok {
					curPf = ps2.Fields
				} else if pc, ok := runtime.Unwrap(pf2).(*runtime.Cell); ok {
					if ps3, ok3 := runtime.Unwrap(pc.Elem).(*runtime.Struct); ok3 {
						curPf = ps3.Fields
					} else {
						curPf = nil
					}
				} else {
					curPf = nil
				}
			}
			if reach {
				leaf := e.path[len(e.path)-1]
				var lpf runtime.Value
				if seen[i] {
					// a repeated key decodes into the value the earlier
					// occurrence produced, like gc's second pass.
					lpf = cur.Fields[leaf]
				} else if curPf != nil && leaf < len(curPf) {
					lpf = curPf[leaf]
				}
				// the DECLARED field typedef: an `any` field must keep
				// decoding through interface{} on a repeated key —
				// the stored value's own typedef is the earlier tree.
				var ltd *runtime.TypeDef
				if fts, err := c.FieldTypes(curTd); err == nil && leaf < len(fts) {
					ltd = fts[leaf]
				}
				if ltd == nil {
					ltd = c.TypeOf(cur.Fields[leaf])
				}
				if e.asStr {
					cur.Fields[leaf] = jsonStringOpt(c, mb.node, ltd, lpf, ectx)
				} else {
					cur.Fields[leaf] = jsonShape(c, mb.node, ltd, lpf, ectx)
				}
				seen[i] = true
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
			ectx.poss = ectx.poss[:len(ectx.poss)-1]
		}
		return z
	case runtime.KindSlice:
		// A string decodes as base64 only for a byte SLICE — a [N]byte
		// array is a number list like any other array (gc has no
		// base64 special case for arrays).
		if s, ok := dec.(string); ok && stdlib.ByteSliceTyp(td) && !stdlib.IsArrayTyp(td) {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				ectx.fail("string", td, err)
				return priorOr(prior, c, td)
			}
			el := make([]runtime.Value, len(b))
			for i, by := range b {
				el[i] = runtime.ByteValue(by)
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
		arr, ok := dec.([]*jsonDom)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		et := c.TypeOf(c.ElemZero(td))
		if stdlib.IsArrayTyp(td) {
			// gc's fixed-array semantics: JSON positions decode into
			// the element in place (a failed position keeps the prior
			// value), positions past the JSON array reset to zero, and
			// extra JSON elements are skipped without error.
			n, ok := c.ArrayLenOf(td)
			if !ok {
				var pe []runtime.Value
				if ps, ok := prior.(*runtime.Slice); ok {
					pe = ps.Elems
				}
				if len(pe) > len(arr) {
					n = int64(len(pe))
				} else {
					n = int64(len(arr))
				}
			}
			el := make([]runtime.Value, n)
			var pe []runtime.Value
			if ps, ok := prior.(*runtime.Slice); ok {
				pe = ps.Elems
			}
			for i := range el {
				if i < len(arr) {
					ectx.path = append(ectx.path, strconv.Itoa(i))
					ectx.poss = append(ectx.poss, arr[i].pos)
					el[i] = jsonShape(c, arr[i], et, stdlib.ElemAt(pe, i), ectx)
					ectx.path = ectx.path[:len(ectx.path)-1]
					ectx.poss = ectx.poss[:len(ectx.poss)-1]
				} else {
					el[i] = c.ElemZero(td)
				}
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
		el := make([]runtime.Value, len(arr))
		var pe []runtime.Value
		if ps, ok := prior.(*runtime.Slice); ok {
			pe = ps.Elems
		}
		for i := range arr {
			ectx.path = append(ectx.path, strconv.Itoa(i))
			ectx.poss = append(ectx.poss, arr[i].pos)
			el[i] = jsonShape(c, arr[i], et, stdlib.ElemAt(pe, i), ectx)
			ectx.path = ectx.path[:len(ectx.path)-1]
			ectx.poss = ectx.poss[:len(ectx.poss)-1]
		}
		return &runtime.Slice{Elems: el, Typ: td}
	case runtime.KindMap:
		ms, ok := dec.([]jsonMember)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		et := c.TypeOf(c.ElemZero(td))
		rm := runtime.NewMap(td, false)
		if pm, ok := prior.(*runtime.Map); ok {
			// gc decodes into the existing map: prior pairs stay unless a
			// decoded key overwrites them.
			for i := 0; i < pm.Len(); i++ {
				rm.Insert(pm.At(i))
			}
		}
		kt := jsonMapKeyTyp(c, td)
		for _, mb := range ms {
			// gc parses the key first: a bad key drops the whole pair and
			// wins the error slot, while a bad element still inserts the
			// fresh zero it decoded into (gc's mapElem), so the element
			// shape runs only after the key is known to fit. Members go
			// in input order — a duplicate key's later value wins.
			ectx.path = append(ectx.path, mb.key)
			ectx.poss = append(ectx.poss, mb.node.pos)
			if kv, kok := jsonMapKey(c, mb.key, kt, ectx); kok {
				rm.Insert(kv, jsonShape(c, mb.node, et, nil, ectx))
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
			ectx.poss = ectx.poss[:len(ectx.poss)-1]
		}
		return rm
	case runtime.KindPointer:
		var ep runtime.Value
		var pc *runtime.Cell
		if c2, ok := prior.(*runtime.Cell); ok {
			pc, ep = c2, c2.Elem
		}
		// *T implementing json.Unmarshaler decodes itself — gc
		// allocates a nil pointer first, then calls the method on it.
		if pc == nil {
			pc = &runtime.Cell{Elem: c.ElemZero(td)}
		}
		if m, ok := runtime.IfaceMember(c, pc, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			if !jsonCallMethod(c, m, node.raw, ectx) {
				return pc
			}
			return pc
		} else if m, ok := runtime.IfaceMember(c, pc, "UnmarshalText"); ok && jsonUnmarshalShape(m) {
			s, isStr := dec.(string)
			if !isStr {
				// the pointee is already allocated, so the fresh cell
				// stays like gc's — and gc names T, not *T, in the
				// must-be-string report.
				et := c.TypeOf(pc.Elem)
				if et == nil {
					et = td
				}
				ectx.fail(jsonValueName(dec), et, errors.New("JSON value must be string type"))
				return pc
			}
			if !jsonCallMethod(c, m, []byte(s), ectx) {
				return pc
			}
			return pc
		}
		return &runtime.Cell{Elem: jsonShape(c, node, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	case runtime.KindInterface:
		return stdlib.ScriptVal(jsonDomDeep(node, ectx))
	}
	if isIface {
		return stdlib.ScriptVal(jsonDomDeep(node, ectx))
	}
	return jsonScalar(c, dec, td, prior, ectx)
}

// jsonScalar coerces a decoded JSON scalar into a declared scalar type.
// JSON numbers always arrive as float64, so int-typed targets convert
// (integrals only, like encoding/json's ParseInt of the literal). A
// kind mismatch is an UnmarshalTypeError, not a silent zero.
func derefTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td != nil && td.Kind == runtime.KindPointer {
		if td.Elem == nil {
			// `&v` where the pointee's typedef could not be recovered
			// (e.g. `var v any` carries an untyped iface nil) — decode
			// as `any` rather than wrapping the value in a cell.
			return nil
		}
		return td.Elem
	}
	return td
}

// IsEncoder reports whether v is the package's script-facing
// *json.Encoder box — the %T speller names it before the %T fallback.
func IsEncoder(v any) bool {
	_, ok := v.(*jsonEncoder)
	return ok
}

func (j jsonCycle) MarshalJSON() ([]byte, error) {
	return nil, &json.UnsupportedValueError{Str: jsonCyclePrefix + j.via}
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range o.keys {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

func (u jsonUnsupported) MarshalJSON() ([]byte, error) { return nil, u.e }

func (e *unsupportedFunc) Error() string { return "json: unsupported type: " + e.name }

func (e *jsonScriptErr) Error() string { return "json: script marshaler error" }

func (e *jsonScriptErr) MarshalJSON() ([]byte, error) {
	return nil, e
}

// ScriptMethod routes Encode around the reflect call (runtime.ScriptMethoder).
func (e *jsonEncoder) ScriptMethod(name string) (func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), bool) {
	if name != "Encode" {
		return nil, false
	}
	return func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("json.Encoder.Encode needs 1 arg, got %d", len(args))
		}
		return stdlib.ErrVal(e.encode(v, args[0])), nil
	}, true
}

func (e *jsonEncoder) encode(c runtime.MemberShapeTyper, v runtime.Value) error {
	err := e.enc.Encode(goJSON(c, v))
	err = jsonUnsupportedOf(err)
	var se *jsonScriptErr
	if errors.As(err, &se) {
		return jsonMarshalerErr(c, se)
	}
	return err
}

// Encode serves host callers holding the boxed encoder (a bound host
// func taking `interface{ Encode(any) error }`); script calls take
// ScriptMethod's raw-arg path. A script value reaches here already
// projected to its host shape, so jsonHostArg recovers the original
// before goJSON walks it.
func (e *jsonEncoder) Encode(v any) error {
	return e.encode(e.c, jsonHostArg(v))
}

func (e *jsonEncoder) SetIndent(prefix, indent string) {
	e.enc.SetIndent(prefix, indent)
}

func (e *jsonEncoder) SetEscapeHTML(on bool) {
	e.enc.SetEscapeHTML(on)
}

func (e *jsonMarshalTextErr) Error() string {
	return "json: error calling MarshalText for type " + e.me.Type.String() + ": " + e.me.Err.Error()
}

func (e *jsonMarshalTextErr) Unwrap() error { return e.me }

func (t scriptReflectType) String() string { return t.name }

// fail records the first type-mismatch error the way saveError does:
// later failures are ignored while decoding continues.
func (x *jsonErrCtx) fail(value string, td *runtime.TypeDef, cause error) {
	err := &jsonUnmarshalTypeError{
		value: value,
		strct: x.strct,
		field: strings.Join(x.path, "."),
		typ:   runtime.DisplayName(td),
		cause: cause,
	}
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...), pos: x.posTop()})
	if x.err == nil {
		x.err = err
	}
}

// failErr records a ready-made error verbatim — for failures whose
// text is not an UnmarshalTypeError (e.g. the inner-literal syntax
// error of a `,string` decode into an interface field).
func (x *jsonErrCtx) failErr(err error) {
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...), pos: x.posTop()})
	if x.err == nil {
		x.err = err
	}
}

func (e *jsonUnmarshalTypeError) Error() string {
	var s string
	// gc names a struct field only when the failure sits INSIDE one —
	// a root-level mismatch reports "into Go value" even when the
	// target is a struct.
	if e.field != "" {
		intoWhat := "Go struct field "
		i := strings.LastIndexByte(e.field, '.') + 1
		if len(e.field[i:]) > 0 && strings.TrimRight(e.field[i:], "0123456789") == "" {
			intoWhat = "" // likely a Go slice or array
		}
		s = "json: cannot unmarshal " + e.value + " into " + intoWhat + e.strct + "." + e.field + " of type " + e.typ
	} else {
		s = "json: cannot unmarshal " + e.value + " into Go value of type " + e.typ
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// derefTyp peels the pointer level off a value's typedef — json.Unmarshal
// decodes into the pointee, so `&cfg` (*Config) shapes as Config.

// jsonStructChase follows a named typedef's TypeExpr chain to the
// shape-giving typedef, returning it only when the chain lands on a
// structural kind — `type TBis T` where T is a struct answers T's
// typedef. Scalar and unresolvable chains return nil so the caller
// keeps the declared td (and its name).
func jsonStructChase(c runtime.ExprResolver, td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && i < 16; i++ {
		switch td.Kind {
		case runtime.KindStruct, runtime.KindSlice, runtime.KindMap, runtime.KindPointer, runtime.KindInterface:
			return td
		}
		var rt *runtime.TypeDef
		var err error
		switch e := td.TypeExpr().(type) {
		case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
			rt, err = c.ResolveType(td, e)
		case nil:
			// a name-only typedef (a bound field spelling like
			// `type F32 float32`) resolves through its own name
			if td.Name != "" {
				rt, err = c.ResolveType(td, &ast.Ident{Name: td.Name})
			} else {
				return nil
			}
		default:
			return nil
		}
		if err != nil || rt == nil {
			return nil
		}
		if rt == td {
			// resolution settled on a typedef that names itself —
			// that IS the chased target (the builtin basic at the
			// end of `type F32 float32`).
			return td
		}
		td = rt
	}
	return nil
}
