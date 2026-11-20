package stdjson

import (
	"go/ast"
	"reflect"
	"strconv"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

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
		et := stdlib.BindTyp(kt, stdlib.ElemTyp(pt.X, kt.Pkg))
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
		v, err := c.Convert(kt, stdlib.ScriptVal(x))
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
	kt, _ := stdlib.MapElemTyps(td)
	return kt
}

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
	return stdlib.Str(nk)
}
