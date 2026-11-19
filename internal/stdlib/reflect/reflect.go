// Package stdreflect is the lazy installer for the bound "reflect"
// package (P2-12): the minireflect facade answers over runtime values;
// DeepEqual stays a VM-side script implementation (deepEql).
package stdreflect

// Package stdreflect is the lazy installer for the bound "reflect"
// package (P2-12): the minireflect facade answers over runtime values;
// DeepEqual stays a VM-side script implementation (deepEql).

import (
	"errors"
	"reflect"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

// reflectHooks returns the engine's type machinery in the facade's
// hook shape — the wiring installReflect builds the reflect bind with,
// also used where intrinsics need declared-type layout (unsafe.Offsetof).
// installReflect binds the reflect facade: interpreting reflect's own
// source is a dead end (reflect.TypeOf reaches internal/abi's
// unsafe.Pointer reinterpretation, which the host-object value model
// cannot execute), so the package is backed by the minireflect facade
// over runtime values instead. DeepEqual stays on the VM-side script
// implementation.
func Install(bind stdlib.Bind, hooks minireflect.Hooks) {
	syms := minireflect.Symbols(hooks)
	syms["DeepEqual"] = &runtime.BuiltinFunc{Name: "reflect.DeepEqual", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, errors.New("reflect.DeepEqual needs 2 args")
		}
		var twin func(a, b *runtime.TypeDef) bool
		if t, ok := vc.(runtime.HostNilTwiner); ok {
			twin = t.HostNilTwin
		}
		return deepEql(args[0], args[1], twin), nil
	}}
	bind("reflect", syms)
}

type devisit struct{ a, b runtime.Value }

func deepEql(a, b runtime.Value, twin func(a, b *runtime.TypeDef) bool) bool {
	return deepEqlSeen(a, b, map[devisit]bool{}, twin)
}

func deepEqlSeen(a, b runtime.Value, seen map[devisit]bool, twin func(a, b *runtime.TypeDef) bool) bool {
	for {
		// a boxed host named basic (a FileMode from os.Stat) meets
		// its source twin's value (io/fs's ModeDir) as that Named;
		// a plain uint32 stays a different type.
		a, b = runtime.TwinOperands(a, b)
		an, aNamed := a.(*runtime.Named)
		bn, bNamed := b.(*runtime.Named)
		if aNamed != bNamed {
			// a raw bound host scalar (time.Second) against a tagged one
			// (time.Duration(1e9)): compare as host values.
			hn, raw := an, b
			if bNamed {
				hn, raw = bn, a
			}
			if hn.Typ != nil && hn.Typ.HostScalar != nil {
				hv, ok := runtime.HostScalarOf(hn.Typ, hn.V)
				return ok && reflect.TypeOf(raw) == reflect.TypeOf(hv) && any(raw) == hv
			}
			return false
		}
		if aNamed {
			if !runtime.TypIdenticalStrict(an.Typ, bn.Typ) {
				return false
			}
			if an.Typ != nil && an.Typ.HostScalar != nil {
				// a bound host scalar's payload is either the host value
				// or still a constant (time.Duration(1e9)): compare as
				// host values.
				ha, aok := runtime.HostScalarOf(an.Typ, an.V)
				hb, bok := runtime.HostScalarOf(bn.Typ, bn.V)
				if aok && bok {
					return ha == hb
				}
			}
			a, b = an.V, bn.V
			continue
		}
		ad, aRef := runtime.Deref(a)
		bd, bRef := runtime.Deref(b)
		if aRef != bRef {
			return false
		}
		if aRef {
			a, b = ad, bd
			continue
		}
		break
	}
	if an, bn := deepNilish(a), deepNilish(b); an || bn {
		if !(an && bn) {
			return false
		}
		// an untyped nil and an empty-interface nil are just nil —
		// equal to each other; a typed nil, bare or boxed in an
		// interface (`var a any = (*T)(nil)`), carries a dynamic type
		// so only another typed nil of the same type compares equal
		// (kind + spelling — anonymous typedefs all spell "" by name —
		// or a nil host *T against its source twin's *T).
		at, bt := deepNilTyp(a), deepNilTyp(b)
		if (at == nil) != (bt == nil) {
			return false
		}
		if at != nil {
			return runtime.TypIdenticalStrict(at, bt) || (twin != nil && twin(at, bt))
		}
		return true
	}
	switch av := a.(type) {
	case *runtime.Slice:
		bs, ok := b.(*runtime.Slice)
		// both arrays and slices are runtime.Slice — the typedef's
		// spelling carries the kind and element type ([]T != [N]T).
		if !ok || len(av.Elems) != len(bs.Elems) || !runtime.TypIdenticalStrict(av.Typ, bs.Typ) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Elems {
			if !deepEqlSeen(av.Elems[i], bs.Elems[i], seen, twin) {
				return false
			}
		}
		return true
	case *runtime.Struct:
		bs, ok := b.(*runtime.Struct)
		// struct defs need full type identity — field names alone let
		// struct{ X int } equal struct{ X any }. Named types match
		// only the same declaration; anonymous ones compare spelling
		// (field types, tags, order).
		if !ok || !runtime.TypIdenticalStrict(av.Def, bs.Def) || len(av.Fields) != len(bs.Fields) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Fields {
			if !deepEqlSeen(av.Fields[i], bs.Fields[i], seen, twin) {
				return false
			}
		}
		return true
	case *runtime.Map:
		bm, ok := b.(*runtime.Map)
		if !ok || len(av.Pairs) != len(bm.Pairs) || !runtime.TypIdenticalStrict(av.Typ, bm.Typ) {
			return false
		}
		if av == bm {
			return true
		}
		v := devisit{av, bm}
		if seen[v] {
			return true
		}
		seen[v] = true
		// keys match by the map's own equality — the canonical key in
		// Pairs — not by deep equality: two distinct pointer keys with
		// equal pointees are different keys in Go. Only the values
		// compare recursively.
		for ak, aval := range av.Pairs {
			bval, found := bm.LookupCanonical(ak)
			if !found {
				return false
			}
			if !deepEqlSeen(aval, bval, seen, twin) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(hostbridge.GoNative(a), hostbridge.GoNative(b))
}

// deepNilTyp is the dynamic type of a nil v: a typed nil's own type or
// the type boxed in an interface nil; nil for the nil interface.
func deepNilTyp(v runtime.Value) *runtime.TypeDef {
	if tn, ok := v.(*runtime.TypedNil); ok {
		return tn.Typ
	}
	return runtime.BoxedNilTyp(v)
}

// deepNilish reports whether v is any nil flavor (typed nil, iface nil,
// or the untyped nil).
func deepNilish(v runtime.Value) bool {
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
		return true
	}
	return v == nil || v == runtime.NIL
}

// ioAdapters bundles the env hooks the moved io package needs
// (P2-12): the declared method set and the fmt argument unwrap.
