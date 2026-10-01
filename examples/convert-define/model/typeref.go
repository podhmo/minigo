package model

import (
	xinspect "github.com/podhmo/minigo/inspect"
)

// TypeKey renders a TypeExpr as a canonical type identity: the fully
// qualified "import/path.Name" for named types (the written spelling's
// local alias is replaced by the declaring file's import path, so the
// same type read through two files compares equal), the plain name for
// builtins, and "*" + the element key for pointer types. Composite
// shapes (slices, maps, func types, ...) report "" — they carry no
// package-qualified identity a rule could name.
func TypeKey(te *xinspect.TypeExpr) string {
	if te == nil {
		return ""
	}
	switch te.Kind {
	case "StarExpr":
		cs := te.Children()
		if len(cs) != 1 {
			return ""
		}
		inner := TypeKey(cs[0])
		if inner == "" {
			return ""
		}
		return "*" + inner
	default:
		sid, ok := te.SymbolID()
		if !ok {
			return ""
		}
		if sid.PackagePath == xinspect.BuiltinPackagePath {
			return sid.Name
		}
		return sid.PackagePath + "." + sid.Name
	}
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
