package hostbridge

import "go/ast"

// SigHostType builds a host func type for a script signature: each
// predeclared basic and `error` keeps its Go type, everything else is
// `any`. A trailing ...T parameter stays variadic.
func SigHostType(sig *ast.FuncType) Type {
	slot := func(e ast.Expr) Type {
		if id, ok := e.(*ast.Ident); ok {
			if id.Name == "error" {
				return ErrorType
			}
			if rt := HostBasicTypes[id.Name]; rt != nil {
				return rt
			}
		}
		return anyType
	}
	var in, out []Type
	variadic := false
	if sig.Params != nil {
		for _, fl := range sig.Params.List {
			e := fl.Type
			var rt Type
			if el, ok := e.(*ast.Ellipsis); ok {
				variadic = true
				rt = SliceOf(slot(el.Elt))
			} else {
				rt = slot(e)
			}
			for range max(1, len(fl.Names)) {
				in = append(in, rt)
			}
		}
	}
	if sig.Results != nil {
		for _, fl := range sig.Results.List {
			for range max(1, len(fl.Names)) {
				out = append(out, slot(fl.Type))
			}
		}
	}
	return FuncOf(in, out, variadic)
}
