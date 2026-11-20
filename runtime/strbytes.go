package runtime

import "go/ast"

// byteSliceTyp is the shared []byte spelling a string->[]byte
// projection carries: anonymous, element byte.
var byteSliceTyp = &TypeDef{Kind: KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("byte")}}

// BytesOf copies s into a script []byte whose elements ride the shared
// byte-tagged values. Conversions (builtin, reflect.Value.Convert,
// unsafe.StringData's backing) produce this one shape so DeepEqual and
// friends see identical values no matter which path built them.
func BytesOf(s string) *Slice {
	el := make([]Value, len(s))
	for i := 0; i < len(s); i++ {
		el[i] = ByteValue(s[i])
	}
	return &Slice{Elems: el, Typ: byteSliceTyp}
}
