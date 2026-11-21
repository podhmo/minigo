package minigo

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// typeIDOf computes the intern key for a typedef (M4-3a): the identity
// every synthesis path agrees on so a session interns one canonical
// TypeDef per type. See runtime.TypeID for the key shape; the rules
// here decide which typedefs collide:
//
//   - aliases are transparent — `type A = B` IS B, so the key peels to
//     the aliased declaration's.
//   - a source-declared type keys on its package path, the parse's
//     *syntax.File (re-parsing the same file makes a NEW type — the
//     source version), its declaration position, and any resolved type
//     arguments. Two instantiations of one generic declaration differ
//     only in their arguments.
//   - a host-represented type keys on the type it represents, not the
//     carrier box: HostIdent when set, else the proxy-mapped HostType
//     (timerChanBox and afterFuncTimer both key as *time.Timer), else
//     the carrier itself when it names a real type, else the bound
//     typedef's declared name — atomic.Value and atomic.Pointer share
//     the *atomicBox carrier but keep distinct declared names.
//   - an anonymous composite keys on its kind plus its element's key;
//     a composite the walk cannot decompose (func types, anonymous
//     structs and interfaces) keys on the canonical spelling instead.
func (e *Engine) typeIDOf(td *runtime.TypeDef) runtime.TypeID {
	td = e.peelAliasTd(td)
	if td == nil {
		return "?"
	}
	var args string
	if len(td.TParams) > 0 && td.Binds != nil {
		for _, tp := range td.TParams {
			args += ";" + string(e.typeIDOf(boundTypedefOf(td.Binds[tp])))
		}
	}
	if td.Spec != nil {
		// source-declared: the file pointer is the source version — a
		// re-parse of the same file makes a new version of the type.
		pkg := ""
		if td.Pkg != nil {
			pkg = td.Pkg.Path
		}
		return runtime.TypeID(fmt.Sprintf("src:%s:%p:%d%s", pkg, td.File, td.Spec.Pos(), args))
	}
	if td.HostIdent != nil {
		return hostTypeID(td.HostIdent, args)
	}
	var rt0 reflect.Type
	if td.HostScalar != nil {
		rt0 = reflect.TypeOf(td.HostScalar)
	} else if td.HostNew != nil {
		rt0 = td.HostType()
	}
	if rt0 != nil {
		if rt0.Name() != "" {
			return hostTypeID(rt0, args)
		}
		// the carrier is unnamed (a *box pointer): the bound typedef's
		// declared name is the represented type's identity.
		if pkg, name, ok := splitHostName(td.Name); ok {
			return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", pkg, name, args))
		}
		return hostTypeID(rt0, args)
	}
	// a *box typedef carries the whole value's carrier on its element:
	// a named host type's box IS its pointer, so the element's HostType
	// is this typedef's carrier. Proxy-map it here — *timerChanBox and
	// *afterFuncTimer both key as *time.Timer — rather than at the
	// element level, which would wrap the pointer twice (**time.Timer)
	// and collide with a real **time.Timer typedef.
	if td.Kind == runtime.KindPointer && td.Elem != nil && td.Elem.HostNew != nil {
		if ert := td.Elem.HostType(); ert != nil {
			if rt := proxyHostType(ert); rt != ert {
				return hostTypeID(rt, args)
			}
		}
	}
	// anonymous and named-basic composites: recurse through Elem when
	// the typedef carries one, else fall back to the canonical
	// underlying spelling (func signatures, anonymous structs).
	if td.Elem != nil {
		return runtime.TypeID(fmt.Sprintf("anon:%d:%s%s", td.Kind, e.typeIDOf(td.Elem), args))
	}
	return runtime.TypeID(fmt.Sprintf("anon:%d:%s%s", td.Kind, runtime.TypUnderlyingSpelling(td), args))
}

// boundTypedefOf unwraps a Binds value to its typedef (type arguments
// bind typedefs; anything else means the parameter is unbound).
func boundTypedefOf(v runtime.Value) *runtime.TypeDef {
	if td, ok := v.(*runtime.TypeDef); ok {
		return td
	}
	return nil
}

// hostTypeID keys a reflect.Type the represented-type way: named types
// by (PkgPath, Name), composites structurally (the kind numbers are
// runtime.TypeKind's).
func hostTypeID(rt reflect.Type, args string) runtime.TypeID {
	if rt == nil {
		return "?"
	}
	if rt.Name() != "" {
		return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", rt.PkgPath(), rt.Name(), args))
	}
	switch rt.Kind() {
	case reflect.Pointer:
		return runtime.TypeID(fmt.Sprintf("anon:8:%s%s", hostTypeID(rt.Elem(), ""), args))
	case reflect.Slice, reflect.Array:
		return runtime.TypeID(fmt.Sprintf("anon:2:%s%s", hostTypeID(rt.Elem(), ""), args))
	case reflect.Map:
		return runtime.TypeID(fmt.Sprintf("anon:3:%s,%s%s", hostTypeID(rt.Key(), ""), hostTypeID(rt.Elem(), ""), args))
	case reflect.Chan:
		return runtime.TypeID(fmt.Sprintf("anon:7:%s%s", hostTypeID(rt.Elem(), ""), args))
	}
	return runtime.TypeID(fmt.Sprintf("anon:-1:%s%s", rt.String(), args))
}

// splitHostName splits a bound typedef's declared "pkg/path.Name" (or a
// synthesized "pkg.Name") into package path and type name.
func splitHostName(s string) (string, string, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[:i], s[i+1:], i+1 < len(s)
		}
		if s[i] == '[' {
			return "", "", false // generic spelling — args live in Binds
		}
	}
	return "", "", false
}
