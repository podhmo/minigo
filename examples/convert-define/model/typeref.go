package model

import (
	"fmt"
	"go/token"
	"reflect"
	"strings"

	xinspect "github.com/podhmo/minigo/inspect"
)

// DeclKey renders a decl's canonical "import/path.Name" identity —
// the lookup key for ParsedInfo.Structs so same-named types in
// different packages do not collide.
func DeclKey(d *xinspect.Decl) string {
	if d == nil {
		return ""
	}
	if d.Package != nil && d.Package.Path != "" {
		return d.Package.Path + "." + d.Name
	}
	return d.Name
}

// ResolveNamed resolves a TypeExpr to the declaration it names: a
// pointer expr resolves to its element's decl, a builtin reports nil.
// Composite expressions without a SymbolID report nil — callers decide
// whether that is an error.
func ResolveNamed(res xinspect.Resolver, te *xinspect.TypeExpr) (*xinspect.Decl, error) {
	if te == nil {
		return nil, nil
	}
	u := te.Unref()
	sid, ok := u.SymbolID()
	if !ok || sid.PackagePath == xinspect.BuiltinPackagePath {
		return nil, nil
	}
	return res(sid)
}

// IsStructDecl reports whether the declaration is a named struct type.
// Interface decls, alias/func decls and host pseudo-decls all fail.
func IsStructDecl(d *xinspect.Decl) bool {
	if d == nil || d.Kind != "type" {
		return false
	}
	def, err := xinspect.DefOf(d)
	return err == nil && def.Kind == "StructType"
}

// StructElemOf peels pointer, slice, array and map layers off a TypeExpr
// and resolves the remaining named type — the type a nested conversion
// would target. Reports nil when the core is not a struct decl.
//
// A named type whose spec is itself a composite (type IDs []SrcInner)
// is unwrapped one declared layer at a time so the inner element decl is
// still discovered; a decl over a non-struct (type Celsius int) ends nil.
func StructElemOf(res xinspect.Resolver, te *xinspect.TypeExpr) *xinspect.Decl {
	for te != nil {
		switch te.Kind {
		case "StarExpr", "ArrayType", "Ellipsis":
			cs := te.Children()
			if len(cs) == 0 {
				return nil
			}
			te = cs[len(cs)-1]
		case "MapType":
			cs := te.Children()
			if len(cs) != 2 {
				return nil
			}
			te = cs[1]
		default:
			if d, err := ResolveNamed(res, te); err == nil && IsStructDecl(d) {
				return d
			}
			u := te.Unwrap(res)
			if u == te {
				return nil
			}
			te = u
		}
	}
	return nil
}

// StructInfoFromDecl materializes a StructInfo for a struct decl,
// copying the inspect field views (names + TypeExpr) and the `json`
// tag name used by priority-2 field matching. Unexported fields are
// skipped: generated code always lives outside the declaring package
// and cannot name them.
func StructInfoFromDecl(d *xinspect.Decl) (*StructInfo, error) {
	if d == nil || !IsStructDecl(d) {
		return nil, fmt.Errorf("%s is not a struct decl", DeclKey(d))
	}
	fields, err := xinspect.FieldsOf(d)
	if err != nil {
		return nil, err
	}

	structInfo := &StructInfo{
		Name: d.Name,
		Type: d,
	}
	for _, f := range fields {
		names := f.Names
		if len(names) == 0 {
			names = []string{embeddedFieldName(f.Type)}
		}
		jsonTag := ""
		if f.Tag != "" {
			jsonTag = strings.Split(reflect.StructTag(f.Tag).Get("json"), ",")[0]
		}
		for _, name := range names {
			if !token.IsExported(name) {
				continue
			}
			fieldInfo := FieldInfo{
				Name:         name,
				OriginalName: name,
				JSONTag:      jsonTag,
				FieldType:    f.Type,
				ParentStruct: structInfo,
			}
			structInfo.Fields = append(structInfo.Fields, fieldInfo)
		}
	}
	return structInfo, nil
}

// embeddedFieldName derives the field name of an embedded member from
// its type's leaf name (e.g. `pkg.Base` -> `Base`, `*pkg.Base` -> `Base`).
func embeddedFieldName(te *xinspect.TypeExpr) string {
	if sid, ok := te.Unref().SymbolID(); ok {
		return sid.Name
	}
	return te.Text
}

// ResolveFieldPath walks a dotted field path ("Inner.ID") through struct
// declarations: each segment must name a field of the current struct,
// and every intermediate field's type must resolve to a struct decl.
// Pointer layers on an intermediate are peeled (`*Inner` intermediates
// resolve — the generator emits the nil-init/guard); slices, maps and
// other composites are not selectable and fail.
//
// The returned chain has one FieldInfo per path segment, so callers can
// read each intermediate's declared type. StructInfos for the
// intermediate decls are materialized into info.Structs, the same cache
// the generator's structInfoFor populates.
func ResolveFieldPath(info *ParsedInfo, res xinspect.Resolver, si *StructInfo, path string) ([]FieldInfo, error) {
	if si == nil {
		return nil, fmt.Errorf("field path %q: no struct to resolve against", path)
	}
	parts := strings.Split(path, ".")
	chain := make([]FieldInfo, 0, len(parts))
	cur := si
	for i, p := range parts {
		var f *FieldInfo
		for j := range cur.Fields {
			if cur.Fields[j].Name == p {
				f = &cur.Fields[j]
				break
			}
		}
		if f == nil {
			return nil, fmt.Errorf("field path %q: %s has no field %q", path, cur.Name, p)
		}
		chain = append(chain, *f)
		if i == len(parts)-1 {
			break
		}
		d, err := ResolveNamed(res, f.FieldType)
		if err != nil {
			return nil, fmt.Errorf("field path %q: resolving %s.%s: %w", path, cur.Name, p, err)
		}
		if !IsStructDecl(d) {
			return nil, fmt.Errorf("field path %q: %s.%s is %s, not a selectable struct", path, cur.Name, p, f.FieldType.Text)
		}
		key := DeclKey(d)
		next, ok := info.Structs[key]
		if !ok {
			next, err = StructInfoFromDecl(d)
			if err != nil {
				return nil, err
			}
			if info.Structs == nil {
				info.Structs = map[string]*StructInfo{}
			}
			info.Structs[key] = next
		}
		cur = next
	}
	return chain, nil
}
