package runtime

import (
	"go/ast"
	"go/token"
	goruntime "runtime"
	"strconv"
	"sync"
	"weak"
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
// The table must not keep a program alive: a host that loads a fresh
// engine per request would otherwise leak one entry (and the node it is
// keyed by) per non-canonical length site per load. Keys are therefore
// weak pointers to the node, and a cleanup attached to the node deletes
// its entry once the AST is unreachable. A weak pointer, unlike a raw
// address, stays distinct from every later allocation, so a node that
// reuses a freed one's memory can never see its stale fold.
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

// arrayFolds maps weak.Pointer[ast.ArrayType] -> *arrayFold. An entry
// lives as long as its node: a cleanup deletes it when the node is
// collected (see [FoldArrayLen]).
var arrayFolds sync.Map

// forgetArrayFold is the cleanup of a node's fold: it runs after the node
// is unreachable and drops the entry its weak key stood for.
func forgetArrayFold(key weak.Pointer[ast.ArrayType]) { arrayFolds.Delete(key) }

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
	if f, ok := arrayFolds.Load(weak.Make(at)); ok {
		return f.(*arrayFold).n, true
	}
	return 0, false
}

// FoldArrayLen publishes n as the evaluated length of at and returns it.
// It never writes to the AST, so it is safe to call concurrently with any
// reader. The first fold of a node wins — a length is one constant per
// site — and later folds are no-ops.
func FoldArrayLen(at *ast.ArrayType, n int64) int64 {
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if m, ok := canonicalLen(l); ok && m == n {
			return n // already spelled in its folded form
		}
	}
	key := weak.Make(at)
	if _, ok := arrayFolds.Load(key); ok {
		return n
	}
	f := &arrayFold{n: n, lit: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)}}
	if _, loaded := arrayFolds.LoadOrStore(key, f); !loaded {
		// exactly one storer registers the cleanup; key does not keep at
		// alive, so the entry goes away with the node.
		goruntime.AddCleanup(at, forgetArrayFold, key)
	}
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
	if f, ok := arrayFolds.Load(weak.Make(at)); ok {
		return f.(*arrayFold).lit
	}
	return at.Len
}
