package runtime

import (
	"go/ast"
	"reflect"
)

// HostStructTagged reports whether v is a [Named] whose tag marks a
// host struct box that copies on assignment like a struct: a bound
// HostNew struct tag, or a declared-over-host-struct tag (`type MyBuf
// bytes.Buffer`) whose own typedef carries no HostNew. A declared
// pointer tag (`type P *bytes.Buffer`) shares its box like a pointer.
func HostStructTagged(v Value) bool {
	n, ok := v.(*Named)
	if !ok || n.Typ == nil {
		return false
	}
	if n.Typ.Kind == KindStruct && n.Typ.HostNew != nil {
		return true
	}
	if n.Typ.Spec == nil || n.Typ.HostNew != nil {
		return false
	}
	if ptrSpelling(n.Typ) {
		return false
	}
	gv, ok := n.V.(*GoValue)
	if !ok {
		return false
	}
	rv := reflect.ValueOf(gv.V)
	return rv.IsValid() && rv.Kind() == reflect.Ptr && !rv.IsNil() && rv.Elem().Kind() == reflect.Struct
}

// ptrSpelling reports whether the typedef's declared underlying expr
// spells a pointer — `type P *bytes.Buffer` directly, or `type Q P`
// transitively: such a tag's box shares like a pointer, never copies.
// Ident refs resolve through function-local typedefs first, then the
// declaring package's declaration index; a selector (otherpkg.P) or a
// builtin name stops the chase as a non-pointer.
func ptrSpelling(td *TypeDef) bool {
	seen := map[*TypeDef]bool{}
	expr := td.Anon
	for depth := 0; expr != nil && depth < 16; depth++ {
		switch e := expr.(type) {
		case *ast.StarExpr:
			return true
		case *ast.Ident:
			expr = nil
			if ltd := td.LocalTypes[e.Name]; ltd != nil {
				if seen[ltd] {
					return false
				}
				seen[ltd] = true
				if ltd.Kind == KindPointer {
					return true
				}
				td = ltd
				expr = ltd.Anon
				continue
			}
			if td.Pkg != nil && td.Pkg.Index != nil {
				if info := td.Pkg.Index.Types[e.Name]; info != nil && info.Decl != nil {
					if ts, ok := info.Decl.Spec.(*ast.TypeSpec); ok {
						expr = ts.Type
					}
				}
			}
		default:
			return false
		}
	}
	return false
}

// HostStructCopy copies the struct behind a [GoValue] holding a non-nil
// pointer to a struct, returning a new box. Unlike [Copy], which copies a
// host struct only when the tag says so, it copies unconditionally;
// the caller decides that v is a struct value. ok is false for any
// other value, which the caller should store unchanged.
func HostStructCopy(v Value) (Value, bool) {
	gv, ok := v.(*GoValue)
	if !ok {
		return nil, false
	}
	rv := reflect.ValueOf(gv.V)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	cp := reflect.New(rv.Elem().Type())
	cp.Elem().Set(rv.Elem())
	return &GoValue{V: cp.Interface()}, true
}

// HostStructSnapshot copies the host struct v boxes — *T storage or a
// bare T value alike — into a fresh *T box, the shape [HostNew] types
// store. ok is false when v is not a host struct box.
func HostStructSnapshot(v Value) (Value, bool) {
	gv, ok := v.(*GoValue)
	if !ok {
		return nil, false
	}
	rv := reflect.ValueOf(gv.V)
	switch rv.Kind() {
	case reflect.Ptr:
		return HostStructCopy(v)
	case reflect.Struct:
		cp := reflect.New(rv.Type())
		cp.Elem().Set(rv)
		return &GoValue{V: cp.Interface()}, true
	}
	return nil, false
}
