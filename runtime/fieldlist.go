package runtime

import "go/ast"

// FieldListLen counts the names a field list declares — `a, b int`
// counts two, an unnamed field one — the arity a parameter or result
// list contributes to a signature. A nil list counts zero.
func FieldListLen(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}
