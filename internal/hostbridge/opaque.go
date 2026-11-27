// Package hostbridge holds the host<->script marshalling layer
// extracted from the VM (P2-7): argument packing for the opaque
// container `any` slots (sync.Map keys, context values, atomic boxes,
// sync.Pool) and the Named-value boxing that keeps Go's
// (type, payload) interface equality identical to gc's.
package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// Materialize converts an untyped constant to its default-typed value.
// The VM injects its const machinery (materializeDefault) so this
// package stays free of the constant domain's details.
type Materialize func(u *runtime.UConst) (runtime.Value, error)

var anyType = reflect.TypeFor[any]()

// OpaqueKeyPosition reports whether `any` parameter i of opaque
// container method recv.Name hashes or ==-compares its argument — a
// key or CAS comparand, where gc runs the unhashable-type check.
func OpaqueKeyPosition(recv, name string, i int) bool {
	switch recv {
	case "*sync.Map":
		// every method's first `any` arg is the key; the CAS pair
		// compares `old` against the stored value with ==.
		if i == 0 {
			return true
		}
		return i == 1 && (name == "CompareAndSwap" || name == "CompareAndDelete")
	case "*context.valueCtx":
		return name == "Value" && i == 0
	case "*stdatomic.AtomicBox":
		return name == "CompareAndSwap" && i == 0
	}
	return false
}

// OpaqueStoreArg packs a script value for a storage `any` parameter:
// key positions take the canonical form (OpaqueKey panics on
// unhashable payloads like gc's hash check); free value positions
// canonicalize only hashable args — so a later comparand still hits
// equal content — and cross unhashable ones verbatim, which gc stores
// without complaint.
func OpaqueStoreArg(recv, name string, i int, a runtime.Value, mat Materialize) (reflect.Value, error) {
	if OpaqueKeyPosition(recv, name, i) {
		rv, err := opaqueStoreKey(a, mat)
		if err != nil {
			return reflect.Value{}, err
		}
		return rv, nil
	}
	if recv == "*minigo.Engine" {
		// Embedding calls are a pass-through boundary, not a container:
		// canonicalizing hashable values would pin them in the process-wide
		// key restore table, while gc-call semantics still require the
		// nested call to receive a snapshot of script containers. Host
		// objects pass through so marshaling them cannot invoke methods
		// (httptest.ResponseRecorder.Result) before the nested call runs.
		return reflect.ValueOf(passThroughSnapshot(a, mat, map[runtime.Value]runtime.Value{})), nil
	}
	if ck, ok := runtime.TryOpaqueKey(a); ok {
		return reflect.ValueOf(ck), nil
	}
	return opaqueStoreValue(a, mat)
}

// passThroughSnapshot copies script containers the way Engine.Call's
// snapshot contract requires, while leaving host objects and callable
// values untouched. It is recursive unlike runtime.Copy: Go assignment
// shares a slice or map, but an embedding-call argument is a snapshot
// of the caller's data, not an assignment into the callee.
func passThroughSnapshot(v runtime.Value, mat Materialize, memo map[runtime.Value]runtime.Value) runtime.Value {
	if u, ok := v.(*runtime.UConst); ok {
		if mv, err := mat(u); err == nil {
			v = mv
		}
	}
	if cp, ok := memo[v]; ok {
		return cp
	}
	switch x := v.(type) {
	case *runtime.Slice:
		cp := *x
		cp.Host = reflect.Value{}
		memo[v] = &cp
		cp.Elems = make([]runtime.Value, len(x.Elems))
		for i, e := range x.Elems {
			cp.Elems[i] = passThroughSnapshot(e, mat, memo)
		}
		return &cp
	case *runtime.Map:
		cp := runtime.NewMap(x.Typ, x.IfaceKey)
		memo[v] = cp
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			cp.Insert(passThroughSnapshot(k, mat, memo), passThroughSnapshot(e, mat, memo))
		}
		return cp
	case *runtime.Struct:
		cp := &runtime.Struct{Def: x.Def, Fields: make([]runtime.Value, len(x.Fields))}
		memo[v] = cp
		for i, e := range x.Fields {
			cp.Fields[i] = passThroughSnapshot(e, mat, memo)
		}
		return cp
	case *runtime.Named:
		cp := *x
		memo[v] = &cp
		cp.V = passThroughSnapshot(x.V, mat, memo)
		return &cp
	default:
		return v
	}
}

// opaqueStoreKey packs a key-position arg in its canonical form.
func opaqueStoreKey(a runtime.Value, mat Materialize) (reflect.Value, error) {
	if u, ok := a.(*runtime.UConst); ok {
		mv, err := mat(u)
		if err != nil {
			return reflect.Value{}, err
		}
		a = mv
	}
	if a == nil {
		return reflect.Zero(anyType), nil
	}
	return reflect.ValueOf(runtime.OpaqueKey(a)), nil
}

// opaqueStoreValue packs a script value for a free storage `any`
// parameter: the value itself crosses, with untyped constants
// materialized and a GoValue box unwrapped to the host object it holds.
func opaqueStoreValue(a runtime.Value, mat Materialize) (reflect.Value, error) {
	if u, ok := a.(*runtime.UConst); ok {
		mv, err := mat(u)
		if err != nil {
			return reflect.Value{}, err
		}
		a = mv
	}
	if gv, ok := a.(*runtime.GoValue); ok {
		a = gv.V
	}
	if a == nil {
		return reflect.Zero(anyType), nil
	}
	if n, ok := a.(*runtime.Named); ok {
		// Named values cross as the struct VALUE, not the pointer:
		// a storage `any` arg is usually a map key (sync.Map's
		// decompressor registry, context keys) and Go's interface
		// equality compares (type, payload) — *runtime.Named compares
		// by pointer, so two equal `uint16(8)` conversions would never
		// hit. The value form reproduces gc's boxing: equal keys hit,
		// unequal or unhashable payloads behave like gc. goValueOf
		// re-wraps the loaded value back to *runtime.Named.
		return reflect.ValueOf(namedStoreValue(n, mat)), nil
	}
	// gc's boxing snapshots the value — the stored copy owns no alias
	// a later store-or-mutate on the source could observe.
	return reflect.ValueOf(runtime.Copy(a)), nil
}

// namedStoreValue copies n to its comparable value form, converting a
// nested Named payload (Named{MyT, Named{byte, v}}) the same way so
// layered named keys compare by content too.
func namedStoreValue(n *runtime.Named, mat Materialize) runtime.Named {
	v := *n
	if inner, ok := v.V.(*runtime.Named); ok {
		v.V = namedStoreValue(inner, mat)
	}
	if u, ok := v.V.(*runtime.UConst); ok {
		// a typed constant still rides the const domain (Named{T,
		// UConst}): the UConst pointer is unique per evaluation, so
		// the key would never hit — materialize it at its default
		// type first.
		if mv, err := mat(u); err == nil {
			v.V = mv
		}
	}
	// gc's boxing snapshots the payload — the stored box must not
	// alias the script struct the caller can still mutate.
	if nn, ok := v.V.(runtime.Named); ok {
		cp := nn
		v.V = runtime.Copy(&cp)
	} else {
		v.V = runtime.Copy(v.V)
	}
	return v
}

// NamedLoadValue is the goValueOf inverse of namedStoreValue: a
// runtime.Named arriving back through an `any` result re-wraps to the
// pointer form scripts use, restoring nested payloads as pointers too.
func NamedLoadValue(n runtime.Named) *runtime.Named {
	if inner, ok := n.V.(runtime.Named); ok {
		n.V = NamedLoadValue(inner)
	}
	return &n
}
