package minigo

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

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingJson(h *hostHelpers) {
	e.Bind("encoding/json", map[string]runtime.Value{
		"Number": &runtime.TypeDef{Name: "encoding/json.Number", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("string"), HostNew: func() any { return json.Number("") }},
		// the Marshaler/Unmarshaler interface types — gc dispatches
		// through an interface-holding target (`var i json.Unmarshaler
		// = new(U); json.Unmarshal(data, &i)`), which reaches the same
		// UnmarshalJSON member probe as a concrete pointer.
		"Marshaler": &runtime.TypeDef{Name: "encoding/json.Marshaler", Kind: runtime.KindInterface, MReqs: []string{"MarshalJSON"}},
		// *json.SyntaxError: the bound decoder returns the host error for
		// malformed input, so `err.(*json.SyntaxError)` asserts on it.
		"SyntaxError": hostType("encoding/json.SyntaxError", func() any { return &json.SyntaxError{} }),
		// *json.UnsupportedTypeError: Marshal's unsupported-type failure
		// surfaces the host error, so `err.(*json.UnsupportedTypeError)`
		// and errors.As assert on it.
		"UnsupportedTypeError": hostType("encoding/json.UnsupportedTypeError", func() any { return &json.UnsupportedTypeError{} }),
		// *json.MarshalerError: a script MarshalJSON/MarshalText failure
		// surfaces the host error (its Type is a scriptReflectType
		// spelling — a script type has no reflect.Type), so the same
		// assert and errors.As apply.
		"MarshalerError": hostType("encoding/json.MarshalerError", func() any { return &json.MarshalerError{} }),
		"Unmarshaler":    &runtime.TypeDef{Name: "encoding/json.Unmarshaler", Kind: runtime.KindInterface, MReqs: []string{"UnmarshalJSON"}},
		"Encoder":        hostType("encoding/json.Encoder", func() any { return &jsonEncoder{enc: json.NewEncoder(io.Discard)} }),
		// NewEncoder boxes a jsonEncoder — the wrapper, not the host
		// *json.Encoder — so Encode takes its raw script arg through
		// goJSON like Marshal (see jsonEncoder.ScriptMethod).
		"NewEncoder": &runtime.BuiltinFunc{Name: "json.NewEncoder", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.NewEncoder needs 1 arg, got %d", len(args))
			}
			w, err := asWriterVM(v, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: &jsonEncoder{c: v, enc: json.NewEncoder(w)}}, nil
		}},
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
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal([]byte{}), errVal(jsonMarshalerErr(v, se))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(err)}}, nil
		}},
		"MarshalIndent": &runtime.BuiltinFunc{Name: "json.MarshalIndent", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("json.MarshalIndent needs 3 args, got %d", len(args))
			}
			b, err := json.MarshalIndent(goJSON(v, args[0]), str(hostbridge.GoNative(args[1])), str(hostbridge.GoNative(args[2])))
			err = jsonUnsupportedOf(err)
			var se *jsonScriptErr
			if errors.As(err, &se) {
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal([]byte{}), errVal(jsonMarshalerErr(v, se))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(err)}}, nil
		}},
		// Two shapes: Unmarshal(data) decodes into the runtime value tree
		// (maps/slices/scalars) and returns it; Unmarshal(data, &v) decodes
		// into the pointer target like the stdlib and returns just error.
		"Unmarshal": &runtime.BuiltinFunc{Name: "json.Unmarshal", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 1 {
				var dec any
				err := json.Unmarshal(byteSlice(hostbridge.GoNative(args[0])), &dec)
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal(jsonDeep(dec)), errVal(err)}}, nil
			}
			if len(args) != 2 {
				return nil, fmt.Errorf("json.Unmarshal needs 1 or 2 args, got %d", len(args))
			}
			// gc: Unmarshal(nil) / Unmarshal(non-pointer T) is an
			// InvalidUnmarshalError before any decoding happens.
			if _, ok := runtime.Deref(args[1]); !ok {
				atd := v.TypeOf(args[1])
				if atd == nil {
					return errVal(fmt.Errorf("json: Unmarshal(nil)")), nil
				}
				return errVal(fmt.Errorf("json: Unmarshal(non-pointer %s)", runtime.DisplayName(atd))), nil
			}
			data := byteSlice(hostbridge.GoNative(args[0]))
			// gc validates the input up front (checkValid): a syntax
			// error reports before any UnmarshalJSON dispatch.
			var dec any
			if err := json.Unmarshal(data, &dec); err != nil {
				return errVal(err), nil
			}
			// a target whose method set offers UnmarshalJSON decodes
			// itself — gc's indirect() hands it the raw literal instead
			// of walking by shape. The pointer's method set is checked,
			// so both value- and pointer-receiver methods qualify.
			// gc's indirect() allocates a nil pointer before the
			// call — without it the method derefs nil. The member
			// probe runs after the realloc so the bound receiver
			// sees the fresh pointee.
			if prior0, _ := runtime.Deref(args[1]); jsonNilish(prior0) {
				if ptd := derefTyp(v.TypeOf(args[1])); ptd != nil && ptd.Kind == runtime.KindPointer {
					if z := jsonAllocPointee(v, ptd); z != nil {
						runtime.SetRef(args[1], z)
					}
				}
			}
			if m, ok := runtime.IfaceMember(v, args[1], "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
				r, cerr := v.Call(m, []runtime.Value{scriptVal(data)})
				if cerr != nil {
					return nil, cerr
				}
				return jsonErrResult(r), nil
			}
			prior, _ := runtime.Deref(args[1])
			ectx := &jsonErrCtx{}
			if std := derefTyp(v.TypeOf(args[1])); std != nil && std.Kind == runtime.KindStruct && std.Name != "" {
				ectx.strct = jsonBareName(std)
			}
			sv := jsonShape(v, dec, derefTyp(v.TypeOf(args[1])), prior, ectx)
			if !runtime.SetRef(args[1], sv) {
				return nil, fmt.Errorf("json.Unmarshal: cannot assign to %T", args[1])
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
				pos := jsonErrOrder(data)
				best := -1
				var pickErr error
				var pickCall runtime.Value
				for _, e := range ectx.errs {
					if pp, ok := jsonErrPos(pos, e.path); ok && (best == -1 || pp < best) {
						best, pickErr, pickCall = pp, e.err, nil
					}
				}
				for _, ce := range ectx.callErrs {
					if pp, ok := jsonErrPos(pos, ce.path); ok && (best == -1 || pp < best) {
						best, pickErr, pickCall = pp, nil, ce.ev
					}
				}
				if pickCall != nil {
					return pickCall, nil
				}
				if pickErr != nil {
					return errVal(pickErr), nil
				}
				// no path resolved — the first-recorded call error
				// stands, matching decode order.
				return ectx.callErrs[0].ev, nil
			}
			return errVal(jsonInputErr(data, ectx.errs)), nil
		}},
		"Valid": h.fn("json.Valid", func(a []any) (any, error) { return json.Valid(byteSlice(a[0])), nil }, json.Valid),
	})
}

// goJSON marshals a script value into the shape encoding/json expects:
// structs become field-name maps, runtime maps/slices recurse, GoValue
// unwraps. c carries the engine's type-resolution hooks — embedded
// fields promote through them.
func goJSON(c runtime.MemberShapeCaller, v any) any {
	return goJSONSeen(c, v, map[any]bool{})
}

// goJSONSeen is goJSON carrying a path set of live container
// pointers: re-encountering one means a reference cycle, which gc
// answers with UnsupportedValueError (a jsonCycle marker carries that
// error out through the host encoder's channel).
func goJSONSeen(c runtime.MemberShapeCaller, v any, seen map[any]bool) any {
	// a value whose method set offers MarshalJSON encodes itself —
	// gc's marshalerEncoder calls it instead of walking the value by
	// shape. A nil pointer/interface answers null below without a call
	// (gc's nil-pointer exception).
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil:
	case nil:
	default:
		if !jsonNilish(v) {
			if m, ok := runtime.IfaceMember(c, v, "MarshalJSON"); ok {
				if r, err := c.Call(m, nil); err == nil {
					if b, e, ok := jsonMarshalResult(r); ok {
						if e != nil {
							return &jsonScriptErr{sv: e, method: "MarshalJSON", operand: v}
						}
						return json.RawMessage(b)
					}
				}
			}
			// MarshalText fires only when MarshalJSON does not; its
			// bytes are UTF-8 text re-emitted as a JSON string, not a
			// raw literal like MarshalJSON's output.
			if m, ok := runtime.IfaceMember(c, v, "MarshalText"); ok {
				if r, err := c.Call(m, nil); err == nil {
					if b, e, ok := jsonMarshalResult(r); ok {
						if e != nil {
							return &jsonScriptErr{sv: e, method: "MarshalText", operand: v}
						}
						return string(b)
					}
				}
			}
		}
	}
	if u, ok := jsonFuncValue(v); ok {
		return u
	}
	switch x := v.(type) {
	case *runtime.Named:
		return goJSONSeen(c, x.V, seen)
	case *runtime.Cell:
		if jsonSeen(seen, x) {
			return jsonCycle{via: scriptTypeString(x)}
		}
		defer delete(seen, x)
		return goJSONSeen(c, x.Elem, seen)
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		// a pointer to a field or element (`&s.F`, `&arr[i]`) encodes
		// its pointee like any other pointer — passing the ref through
		// would reflect on its bookkeeping fields. Guard it like a Cell:
		// `s.V = &s.V` revisits the same ref.
		if jsonSeen(seen, x) {
			return jsonCycle{via: scriptTypeString(x)}
		}
		defer delete(seen, x)
		if el, ok := runtime.Deref(x); ok {
			return goJSONSeen(c, el, seen)
		}
		return nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil pointer/interface field marshals as null — passing the
		// wrapper through would reflect on its bookkeeping fields.
		return nil
	case *runtime.Struct:
		if jsonSeen(seen, x) {
			// a struct only recurs through a pointer to it — gc names
			// that pointer type.
			return jsonCycle{via: "*" + scriptTypeString(x)}
		}
		defer delete(seen, x)
		// structs marshal in DECLARATION order (encoding/json never
		// sorts struct fields) — an ordered map keeps that visible.
		// Anonymous embeds flatten: promoted fields emit in place, a
		// name tag on the embed demotes it to a named member, and a
		// depth/tag tie for one key drops every candidate (gc emits
		// nothing — not even a null — for an ambiguous key).
		var o orderedObject
		ents := jsonMarshalLive(jsonStructFields(c, x.Def))
		for _, e := range ents {
			f, ok := jsonReadPath(x, e.path)
			if !ok {
				// a nil pointer embed's promoted fields are
				// unreachable — gc omits them entirely
				continue
			}
			if e.omit && jsonIsEmpty(f) {
				continue
			}
			fv := goJSONSeen(c, f, seen)
			// `,string` applies to the DECLARED kind: interface
			// fields marshal normally even when the tag asks.
			if e.asStr && !jsonPathIsIface(c, x.Def, e.path) {
				// a MarshalText result is already the member's content
				// — quoting it like a raw string would double-wrap it.
				_, isStr := fv.(string)
				_, hasText := runtime.IfaceMember(c, f, "MarshalText")
				if !(isStr && hasText) {
					if lit, ok := jsonStringLit(f); ok {
						fv = lit
					}
				}
			}
			o.keys = append(o.keys, e.key)
			o.vals = append(o.vals, fv)
		}
		return o
	case *runtime.Slice:
		if jsonSeen(seen, x) {
			return jsonCycle{via: scriptTypeString(x)}
		}
		defer delete(seen, x)
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = goJSONSeen(c, e, seen)
		}
		return out
	case *runtime.Map:
		if jsonSeen(seen, x) {
			return jsonCycle{via: scriptTypeString(x)}
		}
		defer delete(seen, x)
		m := make(map[string]any, x.Len())
		kt := jsonMapKeyTyp(c, x.Typ)
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			kk := jsonMapKeyName(c, k, kt)
			if se, ok := kk.(*jsonScriptErr); ok {
				return se
			}
			m[kk.(string)] = goJSONSeen(c, e, seen)
		}
		return m
	case *runtime.GoValue:
		return x.V
	case map[any]any: // hostbridge.GoNative's *runtime.Map output — keys stringify
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[str(k)] = goJSONSeen(c, e, seen)
		}
		return m
	case map[string]any: // jsonHostArg's restored container copy
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		for k, e := range x {
			x[k] = goJSONSeen(c, e, seen)
		}
		return x
	case []any: // hostbridge.GoNative's *runtime.Slice output
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		for i, e := range x {
			x[i] = goJSONSeen(c, e, seen)
		}
		return x
	default:
		return v
	}
}

// jsonSeen marks a container pointer on the current marshal path —
// gc's ptrSeen equivalent. A repeat visit means a reference cycle.
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

// jsonMapKeyName renders one map key the way gc's marshaler does: a
// key type offering MarshalText names itself through the text
// interface — map keys are not addressable, so only the value method
// set applies. Every other key stringifies by kind. A script method
// error surfaces as a *jsonScriptErr so it can ride out through
// encoding/json's error channel like a top-level failure.
func jsonMapKeyName(c runtime.MemberCaller, k runtime.Value, kt *runtime.TypeDef) any {
	if s, ok := runtime.Unwrap(k).(string); ok && (kt == nil || kt.Kind != runtime.KindInterface) {
		// gc uses a string-kinded key directly, before MarshalText.
		return s
	}
	probe := k
	if kt != nil && kt.Spec != nil {
		// a stored key can lose its declared tag or carry a synthetic
		// map-key approximation without a method set — re-tagging the
		// resolved key typedef lets a declared MarshalText surface.
		// The bare value still reads only the value method set (map
		// keys are not addressable in gc either).
		probe = runtime.Tag(kt, k)
	}
	if m, ok := runtime.IfaceMember(c, probe, "MarshalText"); ok {
		if r, err := c.Call(m, nil); err == nil {
			if b, e, ok := jsonMarshalResult(r); ok {
				if e != nil {
					return &jsonScriptErr{sv: e, method: "MarshalText", operand: k}
				}
				return string(b)
			}
		}
	}
	nk := hostbridge.GoNative(k)
	// an integer key spells its number even when its host type has a
	// String method: map[time.Month]int{3: 1} marshals as {"3":1}.
	switch rv := reflect.ValueOf(nk); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10)
	}
	return str(nk)
}

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
	// callErrs records every non-nil error a script UnmarshalJSON /
	// UnmarshalText returned — script Values (not
	// *jsonUnmarshalTypeError), each tagged with its member path. gc
	// reports the first error in INPUT order across all error kinds,
	// so each call error competes against the by-shape candidates by
	// position — a second failing member must not replace the first.
	callErrs []jsonCallErr
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
// jsonUnsupported stands in for a script func value (an iter.Seq from
// maps.Keys, a closure) in the host tree json.Marshal walks: its
// MarshalJSON fails with an unsupportedFunc that jsonUnsupportedOf turns
// into go's "json: unsupported type: T" — walking the builtin or closure
// struct instead would leak interpreter internals.
type jsonUnsupported struct{ e *unsupportedFunc }

// unsupportedFunc carries the script type of an unencodable func value:
// its host type when the binary has one (iter.Seq[string]), else its
// spelling.
type unsupportedFunc struct {
	rt   reflect.Type
	name string
}

// jsonFuncValue reports the stand-in for a script func value.
func jsonFuncValue(v any) (jsonUnsupported, bool) {
	n, _ := v.(*runtime.Named)
	inner := v
	if n != nil {
		inner = n.V
	}
	switch inner.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
	default:
		return jsonUnsupported{}, false
	}
	e := &unsupportedFunc{name: scriptTypeString(v)}
	if n != nil {
		e.rt = runtime.HostIterSeqType(n.Typ)
	}
	return jsonUnsupported{e}, true
}

// jsonUnsupportedOf rewrites the marshaler error a jsonUnsupported
// stand-in raised into go's own *json.UnsupportedTypeError (or its text,
// when the type has no host equivalent).
func jsonUnsupportedOf(err error) error {
	if uve, ok := errors.AsType[*json.UnsupportedValueError](err); ok && strings.HasPrefix(uve.Str, jsonCyclePrefix) {
		// a jsonCycle marker raised gc's UnsupportedValueError inside a
		// MarshalerError — unwrap it so callers see gc's exact error.
		return uve
	}
	var ue *unsupportedFunc
	if !errors.As(err, &ue) {
		return err
	}
	if ue.rt != nil {
		return &json.UnsupportedTypeError{Type: ue.rt}
	}
	return ue
}

type jsonScriptErr struct {
	sv      runtime.Value
	method  string // "MarshalJSON" or "MarshalText"
	operand runtime.Value
}

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

// cleanupHandle is the runtime.Cleanup the bound AddCleanup returns:
// the interpreter has no GC to fire cleanups, so the handle is an
// inert token — Stop is a no-op.
type cleanupHandle struct{}

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
	b, ok = byteSlice(hostbridge.GoNative(t.Elems[0])), true
	return b, e, ok
}

// jsonCallMethod invokes a script unmarshaler member (UnmarshalJSON
// or UnmarshalText) with the literal bytes it should decode,
// recording a call panic as fatal — gc lets it propagate — and a
// non-nil error return as the competing callErr at the current path.
// ok=false means the call itself blew up and decoding must stop.
func jsonCallMethod(c runtime.ScriptCaller, m runtime.Value, lit []byte, ectx *jsonErrCtx) bool {
	r, cerr := c.Call(m, []runtime.Value{scriptVal(lit)})
	if cerr != nil {
		if ectx.fatal == nil {
			ectx.fatal = cerr
		}
		return false
	}
	if ev := jsonErrResult(r); ev != runtime.NIL {
		ectx.callErrs = append(ectx.callErrs, jsonCallErr{ev: ev, path: append([]string{}, ectx.path...)})
	}
	return true
}

// jsonCallErr is one script unmarshaler's error return plus the path
// of the member it fired for — the input-order competition resolves
// it against the by-shape candidates in errs.
type jsonCallErr struct {
	ev   runtime.Value
	path []string
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

// peelNil unwraps cells, refs and named wrappers down to a nil
// payload and reports its tagged typedef — the one peel-to-nil walk
// json's nil probe (any nilish payload) and fmt's pointer skip
// (pointer-tagged nil only) share.
func peelNil(v runtime.Value) (*runtime.TypeDef, bool) {
	var seen map[runtime.Value]bool
	for {
		if dv, ok := runtime.Deref(v); ok {
			if seen == nil {
				seen = map[runtime.Value]bool{v: true}
			} else if seen[v] {
				// a cyclic pointer (x := any(nil); x = &x) is no nil
				return nil, false
			} else {
				seen[v] = true
			}
			v = dv
			continue
		}
		switch n := v.(type) {
		case *runtime.TypedNil:
			return n.Typ, true
		case *runtime.IfaceNil:
			return n.Typ, true // a TypedNil boxed by an interface slot keeps its tag
		}
		return nil, false
	}
}

// jsonNilish reports a nil pointer/interface payload — a cell or a
// named wrapper keeps the verdict of the value it carries so a nil
// *T inside one still reads as nil.
func jsonNilish(v runtime.Value) bool {
	_, ok := peelNil(v)
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
}

// jsonErrOrder maps each object's member path ("\x00"-joined) to its
// stream position by walking the raw input's tokens once — position 0
// is the first pair gc's streaming decoder would decode.
func jsonErrOrder(data []byte) map[string]int {
	pos := map[string]int{}
	n := 0
	var walk func(d *json.Decoder, path []string)
	walk = func(d *json.Decoder, path []string) {
		t, err := d.Token()
		if err != nil {
			return
		}
		switch t {
		case json.Delim('{'):
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return
				}
				key, _ := kt.(string)
				p := append(append(make([]string, 0, len(path)+1), path...), key)
				pos[strings.Join(p, "\x00")] = n
				n++
				walk(d, p)
			}
			d.Token() // '}'
		case json.Delim('['):
			for i := 0; d.More(); i++ {
				p := append(append(make([]string, 0, len(path)+1), path...), strconv.Itoa(i))
				pos[strings.Join(p, "\x00")] = n
				n++
				walk(d, p)
			}
			d.Token() // ']'
		}
	}
	walk(json.NewDecoder(bytes.NewReader(data)), nil)
	return pos
}

// jsonErrPos looks up a member path's position in the raw input,
// walking up to the nearest recorded ancestor (a nested failure
// inherits its member's slot). Paths carry the input's member
// spelling already (the codec records inKey — the exact key, or the
// case-folded key it matched — not the Go field name), so the lookup
// is exact.
func jsonErrPos(pos map[string]int, path []string) (int, bool) {
	for p := path; ; p = p[:len(p)-1] {
		key := strings.Join(p, "\x00")
		if pp, ok := pos[key]; ok {
			return pp, true
		}
		if len(p) == 0 {
			return 0, false
		}
	}
}

// jsonInputErr picks the error whose path is earliest in the raw
// input, falling back to the first recorded when a path never matched
// a member position (e.g. a root-level failure).
func jsonInputErr(data []byte, errs []jsonErrAt) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0].err
	}
	pos := jsonErrOrder(data)
	best, pick := -1, errs[0]
	for _, e := range errs {
		if pp, ok := jsonErrPos(pos, e.path); ok && (best == -1 || pp < best) {
			best, pick = pp, e
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
	case []any:
		return "array"
	case map[string]any:
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
		return p, nil
	case nil:
		cell := &runtime.Cell{Elem: c.Zero(td)}
		return cell, cell
	default:
		// the decode slot is always addressable — a bare struct prior
		// still sees the pointer method set, like gc handing the method
		// the field's own address.
		cell := &runtime.Cell{Elem: prior}
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
	return elemTyp(e, pkg)
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
				et = elemTyp(st.X, td.Pkg)
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

// jsonReadPath follows an entry's index path for marshal — a nil
// pointer embed (or any non-struct hop) makes the promoted fields
// unreachable, and gc omits them from the output entirely.
func jsonReadPath(s *runtime.Struct, path []int) (runtime.Value, bool) {
	cur := s
	for h := 0; h < len(path)-1; h++ {
		st := runtime.StructOf(cur.Fields[path[h]])
		if st == nil {
			return nil, false
		}
		cur = st
	}
	return cur.Fields[path[len(path)-1]], true
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
func jsonShape(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	if td == nil {
		return scriptVal(jsonDeep(dec))
	}
	if dec == nil {
		switch td.Kind {
		case runtime.KindPointer, runtime.KindSlice, runtime.KindMap, runtime.KindInterface:
			if td.Kind == runtime.KindSlice && isArrayTyp(td) {
				// gc ignores null for fixed arrays: the value stays.
				return priorOr(prior, c, td)
			}
			return c.Zero(td)
		}
		return priorOr(prior, c, td)
	}
	// gc calls UnmarshalJSON on the addressable decode target: a
	// non-nil *T through its own pointer, a named value through &v,
	// and a fresh slot — new field, slice element, map value — through
	// the addressable value it decodes into before storing. The
	// receiver's cell is returned rather than the prior so a
	// whole-value `*u = ...` assignment in the method reaches the
	// field. The literal handed over is the re-encoded subtree — the
	// original byte span is gone by the time decode reaches here. The
	// probe is gated to NAMED types: methods attach to named types
	// only, so an unnamed container's member walk must not borrow the
	// element type's methods.
	if td.Name != "" {
		recv, cell := unmarshalRecv(prior, c, td)
		if m, ok := runtime.IfaceMember(c, recv, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			if lit, lerr := json.Marshal(dec); lerr == nil {
				if !jsonCallMethod(c, m, lit, ectx) {
					return prior
				}
				if cell != nil {
					return cell.Elem
				}
				return prior
			}
		} else if m, ok := runtime.IfaceMember(c, recv, "UnmarshalText"); ok && jsonUnmarshalShape(m) {
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
	switch td.Kind {
	case runtime.KindStruct:
		m, ok := dec.(map[string]any)
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
		if ps, ok := prior.(*runtime.Struct); ok && ps.Def == td {
			pf = ps.Fields
		}
		ents := jsonStructFields(c, td)
		// gc reduces each exact-name group by dominantField (dead
		// groups serve neither exact nor folded lookup), then decodes:
		// an exact-key hit wins, else the FIRST surviving entry in
		// declaration order whose name case-folds to the input key.
		live := jsonDominant(ents)
		exact := map[string]int{}
		for i := range ents {
			if live[i] {
				exact[ents[i].key] = i
			}
		}
		foldWin := map[string]int{}
		for k := range m {
			if _, ok := exact[k]; ok {
				continue
			}
			for i := range ents {
				if live[i] && strings.EqualFold(ents[i].key, k) {
					foldWin[k] = i
					break
				}
			}
		}
		for i := range ents {
			e := &ents[i]
			if !live[i] {
				continue
			}
			// the JSON key this entry decodes: its exact key when the
			// object carries it, else the folded key it won first
			inKey := e.key
			fv, kok := m[e.key]
			if !kok {
				for k, w := range foldWin {
					if w == i {
						inKey, fv, kok = k, m[k], true
						break
					}
				}
			}
			if !kok {
				continue
			}
			// errors path on the input's member spelling like gc's
			// UnmarshalTypeError (.a, not the Go field name A).
			ectx.path = append(ectx.path, inKey)
			cur, curTd, curPf := z, td, pf
			reach := true
			for h := 0; h < len(e.path)-1 && reach; h++ {
				// walk into the embedded field at path[h], allocating a
				// nil pointer embed the way gc allocates on decode.
				idx := e.path[h]
				etd := jsonEmbedTyp(c, curTd, idx)
				if etd == nil {
					etd = fieldTypOf(curTd, idx)
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
				if curPf != nil && leaf < len(curPf) {
					lpf = curPf[leaf]
				}
				ltd := c.TypeOf(cur.Fields[leaf])
				if e.asStr {
					cur.Fields[leaf] = jsonStringOpt(c, fv, ltd, lpf, ectx)
				} else {
					cur.Fields[leaf] = jsonShape(c, fv, ltd, lpf, ectx)
				}
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
		}
		return z
	case runtime.KindSlice:
		// A string decodes as base64 only for a byte SLICE — a [N]byte
		// array is a number list like any other array (gc has no
		// base64 special case for arrays).
		if s, ok := dec.(string); ok && byteSliceTyp(td) && !isArrayTyp(td) {
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
		arr, ok := dec.([]any)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		et := c.TypeOf(c.ElemZero(td))
		if isArrayTyp(td) {
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
					el[i] = jsonShape(c, arr[i], et, elemAt(pe, i), ectx)
					ectx.path = ectx.path[:len(ectx.path)-1]
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
			el[i] = jsonShape(c, arr[i], et, elemAt(pe, i), ectx)
			ectx.path = ectx.path[:len(ectx.path)-1]
		}
		return &runtime.Slice{Elems: el, Typ: td}
	case runtime.KindMap:
		m, ok := dec.(map[string]any)
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
		for k, e := range m {
			// gc parses the key first: a bad key drops the whole pair and
			// wins the error slot, while a bad element still inserts the
			// fresh zero it decoded into (gc's mapElem), so the element
			// shape runs only after the key is known to fit.
			ectx.path = append(ectx.path, k)
			if kv, kok := jsonMapKey(c, k, kt, ectx); kok {
				rm.Insert(kv, jsonShape(c, e, et, nil, ectx))
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
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
			if lit, lerr := json.Marshal(dec); lerr == nil {
				if !jsonCallMethod(c, m, lit, ectx) {
					return pc
				}
				return pc
			}
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
		return &runtime.Cell{Elem: jsonShape(c, dec, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	case runtime.KindInterface:
		return scriptVal(jsonDeep(dec))
	}
	return jsonScalar(c, dec, td, prior, ectx)
}

// jsonMapKeyTyp resolves a map typedef's key typedef — LocalTypes
// first so a function-local `type NK int` key peels to int, then the
// declared name through the enclosing typedef's package context (the
// synthetic approximation in mapElemTyps carries no method set, and
// the key's UnmarshalText probe needs the real typedef).
func jsonMapKeyTyp(c runtime.ExprResolver, td *runtime.TypeDef) *runtime.TypeDef {
	mt, ok := td.TypeExpr().(*ast.MapType)
	if !ok {
		return nil
	}
	if id, ok := mt.Key.(*ast.Ident); ok && td.LocalTypes != nil {
		if lt := td.LocalTypes[id.Name]; lt != nil {
			return lt
		}
	}
	if rt, err := c.ResolveType(td, mt.Key); err == nil && rt != nil {
		return rt
	}
	kt, _ := mapElemTyps(td)
	return kt
}

// jsonMapKey parses one JSON object key text into the map's key type.
// gc accepts string/int/uint/float spellings plus named and pointer
// variants (a `*K` key lands in a fresh cell holding the parsed K);
// every other kind reports `cannot unmarshal string` for the key type,
// and a bad literal reports `number <key>`. Interface keys keep the
// string itself. ok=false drops the pair entirely.
func jsonMapKey(c runtime.VMCaller, k string, kt *runtime.TypeDef, ectx *jsonErrCtx) (runtime.Value, bool) {
	if kt == nil {
		return runtime.Value(k), true
	}
	// a pointer key decodes as the pointee, then addresses
	if pt, ok := kt.TypeExpr().(*ast.StarExpr); ok {
		et := bindTyp(kt, elemTyp(pt.X, kt.Pkg))
		if rt, err := c.ResolveType(kt, pt.X); err == nil && rt != nil {
			et = rt
		}
		v, ok := jsonMapKey(c, k, et, ectx)
		if !ok {
			return nil, false
		}
		return &runtime.Cell{Elem: v}, true
	}
	u := jsonUnderlyingName(kt)
	if kt.Kind == runtime.KindInterface || u == "any" || u == "interface{}" {
		return runtime.Value(k), true
	}
	// gc decodes map keys through UnmarshalText before any by-kind
	// parsing — the key sits in an addressable temporary, so
	// pointer-receiver methods fire. UnmarshalJSON is never probed
	// on keys.
	kr := &runtime.Cell{Elem: c.Zero(kt)}
	if m, ok := runtime.IfaceMember(c, kr, "UnmarshalText"); ok && jsonUnmarshalShape(m) {
		if !jsonCallMethod(c, m, []byte(k), ectx) {
			return nil, false
		}
		return kr.Elem, true
	}
	conv := func(x any) (runtime.Value, bool) {
		v, err := c.Convert(kt, scriptVal(x))
		if err != nil {
			ectx.fail("string", kt, nil)
			return nil, false
		}
		return v, true
	}
	switch {
	case u == "string":
		return conv(k)
	case jsonIntName(u):
		bits, signed := jsonIntWidth(u)
		var err error
		var n int64
		var u64 uint64
		if signed {
			n, err = strconv.ParseInt(k, 10, bits)
		} else {
			u64, err = strconv.ParseUint(k, 10, bits)
		}
		if err != nil {
			ectx.fail("number "+k, kt, nil)
			return nil, false
		}
		if signed {
			return conv(n)
		}
		return conv(int64(u64))
	case u == "float64" || u == "float32":
		f, err := strconv.ParseFloat(k, 64)
		if err != nil {
			ectx.fail("number "+k, kt, nil)
			return nil, false
		}
		return conv(f)
	}
	// bool, struct, and other unsupported key kinds: gc reports the
	// raw string failing for the key type.
	ectx.fail("string", kt, nil)
	return nil, false
}

// jsonScalar coerces a decoded JSON scalar into a declared scalar type.
// JSON numbers always arrive as float64, so int-typed targets convert
// (integrals only, like encoding/json's ParseInt of the literal). A
// kind mismatch is an UnmarshalTypeError, not a silent zero.
func jsonScalar(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	fail := func() runtime.Value {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, scriptVal(x))
		if err != nil {
			return fail()
		}
		return v
	}
	switch td.Name {
	case "string":
		s, ok := dec.(string)
		if !ok {
			return fail()
		}
		return s
	case "bool":
		b, ok := dec.(bool)
		if !ok {
			return fail()
		}
		return b
	case "float64", "float32":
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		return conv(f)
	case "encoding/json.Number":
		// Number is `type Number string` and keeps the literal text —
		// the check is the number grammar, not the float64 range.
		switch x := dec.(type) {
		case float64:
			return conv(jsonNumLit(x))
		case string:
			if !jsonIsValidNumber(x) {
				// gc reports this one without field context.
				ectx.failErr(fmt.Errorf("json: cannot unmarshal string %s into Go value of type json.Number: invalid syntax", strconv.Quote(x)))
				return priorOr(prior, c, td)
			}
			return conv(x)
		}
		return fail()
	}
	if jsonIntName(td.Name) {
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, td.Name)
		if !ok {
			ectx.fail("number "+jsonNumLit(f), td, nil)
			return priorOr(prior, c, td)
		}
		return conv(i)
	}
	// Named basics (`type MBool bool`, `type MyInt int`) coerce by their
	// underlying zero's Go kind — Convert applies the declaration's
	// conversion rules and tags the result.
	switch runtime.Unwrap(c.Zero(td)).(type) {
	case bool:
		b, ok := dec.(bool)
		if !ok {
			return fail()
		}
		return conv(b)
	case string:
		s, ok := dec.(string)
		if !ok {
			return fail()
		}
		return conv(s)
	case float64:
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		return conv(f)
	case int64:
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, jsonUnderlyingName(td))
		if !ok {
			ectx.fail("number "+jsonNumLit(f), td, nil)
			return priorOr(prior, c, td)
		}
		return conv(i)
	}
	return fail()
}

// jsonStringOpt decodes a `,string`-tagged field: the JSON value must
// be a string whose inner text is the literal for the field's kind —
// `"42"` for ints, `"\"x\""` for strings, `"true"` for bools. A
// non-string value is a type error even when it would otherwise fit
// (gc reports the value's JSON kind) and null keeps normal semantics.
// Composite and interface kinds ignore the option entirely — the
// string then decodes as it normally would (into an interface the
// string itself lands, into a struct it fails like any string).
// Pointers apply the option to the pointee.
func jsonStringOpt(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	if dec == nil {
		return jsonShape(c, nil, td, prior, ectx)
	}
	s, sok := dec.(string)
	if td == nil {
		if sok {
			return scriptVal(s)
		}
		return scriptVal(jsonDeep(dec))
	}
	// A quoted "null" is the null literal, not the word: it follows
	// normal null semantics (nils nilable kinds, leaves the rest).
	if sok && s == "null" {
		return jsonShape(c, nil, td, prior, ectx)
	}
	if td.Kind == runtime.KindPointer {
		var ep runtime.Value
		if pc, ok := prior.(*runtime.Cell); ok {
			ep = pc.Elem
		}
		return &runtime.Cell{Elem: jsonStringOpt(c, dec, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, scriptVal(x))
		if err != nil {
			ectx.fail("string", td, nil)
			return priorOr(prior, c, td)
		}
		return v
	}
	numFail := func() runtime.Value {
		ectx.fail("number "+s, td, nil)
		return priorOr(prior, c, td)
	}
	isNumber := td.Name == "encoding/json.Number"
	kind, bits, signed := "", 64, true
	if !isNumber {
		switch u := jsonUnderlyingName(td); {
		case u == "string":
			kind = "string"
		case u == "bool":
			kind = "bool"
		case u == "float64", u == "float32":
			kind = "float"
			if u == "float32" {
				bits = 32
			}
		case jsonIntName(u):
			bits, signed = jsonIntWidth(u)
			if signed {
				kind = "int"
			} else {
				kind = "uint"
			}
		}
	}
	// gc's ,string applies only to bool/int/float/string kinds (and
	// json.Number's literal keep): composite and interface kinds ignore
	// the option entirely and the value decodes as it normally would —
	// an array into a `,string` slice field is not an error.
	if !isNumber && kind == "" {
		return jsonShape(c, dec, td, prior, ectx)
	}
	if !sok {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
	}
	// a named type offering UnmarshalText receives the string's inner
	// text verbatim — ,string hands the method the literal bytes
	// without re-parsing them as JSON.
	if !isNumber && td.Name != "" {
		recv, cell := unmarshalRecv(prior, c, td)
		if m, ok := runtime.IfaceMember(c, recv, "UnmarshalText"); ok && jsonUnmarshalShape(m) {
			if !jsonCallMethod(c, m, []byte(s), ectx) {
				return prior
			}
			if cell != nil {
				return cell.Elem
			}
			return prior
		}
	}
	inner := s
	if isNumber {
		if !jsonIsValidNumber(inner) {
			// gc reports this one without field context.
			ectx.failErr(fmt.Errorf("json: cannot unmarshal string %s into Go value of type json.Number: invalid syntax", strconv.Quote(inner)))
			return priorOr(prior, c, td)
		}
		return conv(inner)
	}
	switch kind {
	case "string":
		var sv string
		if err := json.Unmarshal([]byte(inner), &sv); err != nil {
			// The inner text is re-parsed expecting a quoted string
			// token: a leading non-quote char reports jsontext's
			// object-key error, a bare empty text reports EOF.
			var cause error
			if inner == "" {
				cause = errors.New("unexpected end of JSON input")
			} else if inner[0] != '"' {
				cause = fmt.Errorf("invalid character %q looking for beginning of object key string", rune(inner[0]))
			} else {
				cause = err
			}
			ectx.fail("string", td, cause)
			return priorOr(prior, c, td)
		}
		return conv(sv)
	case "bool":
		switch inner {
		case "true":
			return conv(true)
		case "false":
			return conv(false)
		}
		ectx.fail("string "+strconv.Quote(inner), td, strconv.ErrSyntax)
		return priorOr(prior, c, td)
	case "int":
		n, err := strconv.ParseInt(inner, 10, bits)
		if err != nil {
			return numFail()
		}
		return conv(n)
	case "uint":
		u64, err := strconv.ParseUint(inner, 10, bits)
		if err != nil {
			return numFail()
		}
		return conv(int64(u64))
	case "float":
		f, err := strconv.ParseFloat(inner, bits)
		if err != nil {
			return numFail()
		}
		return conv(f)
	}
	return priorOr(prior, c, td)
}

// jsonIsValidNumber mirrors encoding/json's isValidNumber — the RFC
// 7159 number grammar. json.Number keeps the literal text, so range is
// not a constraint ("1e1000" is valid even though it overflows float64).
func jsonIsValidNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	switch {
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	default:
		return false
	}
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s == ""
}

// jsonStringLit renders a scalar field's `,string` marshal literal —
// the JSON encoding of the value as it should appear inside the
// outer quotes: ints become "42", strings are re-quoted ("\"x\""),
// json.Number keeps its raw literal text. ok=false for kinds gc
// leaves alone (composites, interfaces, nil).
func jsonStringLit(v runtime.Value) (string, bool) {
	// Named must be checked before runtime.Unwrap peels it — json.Number
	// is `type Number string` and only the wrapper carries the typedef.
	if n, ok := v.(*runtime.Named); ok {
		if n.Typ != nil && n.Typ.Name == "encoding/json.Number" {
			if s, ok := runtime.Unwrap(n.V).(string); ok {
				return s, true
			}
			return "", false
		}
		return jsonStringLit(n.V)
	}
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Cell:
		return jsonStringLit(x.Elem)
	case bool:
		return strconv.FormatBool(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// jsonIntName reports whether name is a builtin integer spelling.
func jsonIntName(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"byte", "rune", "uintptr":
		return true
	}
	return false
}

// jsonUnderlyingName peels a named typedef to its underlying spelling's
// bare identifier ("int" for `type MyInt int`) for width checks.
func jsonUnderlyingName(td *runtime.TypeDef) string {
	s := runtime.TypUnderlyingSpelling(td)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// jsonIntOf converts an integral float64 to int64 like encoding/json's
// ParseInt on the literal: non-integral and out-of-range numbers fail.
func jsonIntOf(f float64, name string) (int64, bool) {
	i := int64(f)
	if float64(i) != f {
		return 0, false
	}
	bits, signed := jsonIntWidth(name)
	if bits >= 64 {
		return i, true
	}
	if signed {
		max := int64(1)<<(bits-1) - 1
		if i < -max-1 || i > max {
			return 0, false
		}
	} else if i < 0 || i > int64(1)<<bits-1 {
		return 0, false
	}
	return i, true
}

// jsonIntWidth returns the bit width and signedness of a spelled integer
// type; unknown spellings default to signed 64.
func jsonIntWidth(name string) (bits int, signed bool) {
	switch name {
	case "int8", "uint8", "byte":
		return 8, name == "int8"
	case "int16", "uint16":
		return 16, name == "int16"
	case "int32", "rune", "uint32":
		return 32, name == "int32" || name == "rune"
	}
	return 64, name == "int" || name == "int64"
}

// jsonDeep rewrites the tree encoding/json produces — map[string]any keys —
// into the map[any]any shape scriptVal already handles.
func jsonDeep(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[any]any, len(x))
		for k, e := range x {
			out[k] = jsonDeep(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = jsonDeep(e)
		}
		return x
	default:
		return v
	}
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
		return errVal(e.encode(v, args[0])), nil
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

func (cleanupHandle) Stop() {}

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
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
	if x.err == nil {
		x.err = err
	}
}

// failErr records a ready-made error verbatim — for failures whose
// text is not an UnmarshalTypeError (e.g. the inner-literal syntax
// error of a `,string` decode into an interface field).
func (x *jsonErrCtx) failErr(err error) {
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
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
