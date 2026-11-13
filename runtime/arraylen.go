package runtime

import (
	"go/ast"
	"go/token"
	"strconv"
	"sync"
)

// Array-length folding.
//
// An array type's length is spelled in the source as a literal (`[3]T`),
// a named or computed constant (`[N]T`, `[N*2]T`, `[len(a)]T`) or `...`
// (`[...]T{...}`). Type identity wants the concrete count — `[N]T` and
// `[3]T` are one type — so the interpreter folds the evaluated length
// into the type once it is known.
//
// The type AST is shared by every goroutine that runs the code, so the
// fold must not rewrite it: a goroutine that is spelling or comparing the
// type would race with the rewrite. Instead the folded length is
// published in a side table keyed by the *ast.ArrayType, and every reader
// of a length goes through [ArrayLenExpr] / [FoldedArrayLen]. The AST
// itself stays immutable after parsing.
//
// A canonical decimal literal (`[3]T`, by far the common spelling) is
// already its own folded form: it is never recorded and its readers never
// touch the table, so the hot path is a pure read.

// arrayFold is one published fold: the evaluated count and the canonical
// literal spelling it. Immutable once stored.
type arrayFold struct {
	n   int64
	lit *ast.BasicLit
}

// arrayFolds maps *ast.ArrayType -> *arrayFold. Entries are bounded by the
// number of non-canonical length sites in the loaded programs.
var arrayFolds sync.Map

// canonicalLen reports the count of a plain decimal integer literal — the
// form that needs no folding: ASCII digits only, no sign, base prefix,
// digit separator or exponent, and no leading zero (`010` is octal).
func canonicalLen(l *ast.BasicLit) (int64, bool) {
	if l.Kind != token.INT {
		return 0, false
	}
	s := l.Value
	if s == "" || len(s) > 18 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// FoldedArrayLen reports the constant length of an array type when it is
// already known: a canonical literal, or a length published by
// [FoldArrayLen]. ok is false for a slice type and for a length that has
// not been evaluated yet.
func FoldedArrayLen(at *ast.ArrayType) (int64, bool) {
	if at.Len == nil {
		return 0, false
	}
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if n, ok := canonicalLen(l); ok {
			return n, true
		}
	}
	if f, ok := arrayFolds.Load(at); ok {
		return f.(*arrayFold).n, true
	}
	return 0, false
}

// FoldArrayLen publishes n as the evaluated length of at and returns it.
// It never writes to the AST, so it is safe to call concurrently with any
// reader. A repeated fold of the same count is a no-op.
func FoldArrayLen(at *ast.ArrayType, n int64) int64 {
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if m, ok := canonicalLen(l); ok && m == n {
			return n // already spelled in its folded form
		}
	}
	if f, ok := arrayFolds.Load(at); ok && f.(*arrayFold).n == n {
		return n
	}
	arrayFolds.Store(at, &arrayFold{n: n, lit: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)}})
	return n
}

// ArrayLenExpr returns the length expression of an array type as type
// identity sees it: the folded literal once the length is known, else
// the expression as written (nil for a slice type). Readers that spell or
// compare lengths use this instead of at.Len.
func ArrayLenExpr(at *ast.ArrayType) ast.Expr {
	if at.Len == nil {
		return nil
	}
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if _, ok := canonicalLen(l); ok {
			return l
		}
	}
	if f, ok := arrayFolds.Load(at); ok {
		return f.(*arrayFold).lit
	}
	return at.Len
}
