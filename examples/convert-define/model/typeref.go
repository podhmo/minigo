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
			d, err := ResolveNamed(res, te)
			if err != nil || !IsStructDecl(d) {
				return nil
			}
			return d
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
