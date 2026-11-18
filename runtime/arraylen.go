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
// A fold belongs to the session that evaluated it: callers carrying a
// *Package record into the package's PackageInstance table, which dies
// with the session — a session forked after a source edit folds the same
// shared AST under its own answers without touching a sibling's. The
// remaining callers hold synthesized nodes no instance can own; they
// share one process-wide table ([sharedArrayFolds]) that must not keep
// a program alive, so its keys are weak pointers and a cleanup attached
// to the node deletes its entry once the AST is unreachable. A weak
// pointer, unlike a raw address, stays distinct from every later
// allocation, so a node that reuses a freed one's memory can never see
// its stale fold.
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

// sharedArrayFolds maps weak.Pointer[ast.ArrayType] -> *arrayFold for
// callers without a package: synthesized type nodes that no session's
// PackageInstance can own. An entry lives as long as its node: a
// cleanup deletes it when the node is collected (see [FoldArrayLen]).
// Nodes reachable through a session's package use the instance's own
// table instead — a fold belongs to the session that evaluated it.
var sharedArrayFolds sync.Map

// forgetArrayFold is the cleanup of a node's fold: it runs after the node
// is unreachable and drops the entry its weak key stood for.
func forgetArrayFold(key weak.Pointer[ast.ArrayType]) { sharedArrayFolds.Delete(key) }

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

// foldMap selects the table a fold belongs to: the owning package's
// PackageInstance — the per-session unit — or the process-wide fallback
// for nodes no package can claim (synthesized ASTs, nil-Pkg typedefs).
func foldMap(p *Package) *sync.Map {
	if p == nil {
		return &sharedArrayFolds
	}
	return &p.PackageInstance.arrayFolds
}

// FoldedArrayLen reports the constant length of an array type when it is
// already known: a canonical literal, or a length published by
// [FoldArrayLen] under p's session. ok is false for a slice type and for
// a length that has not been evaluated yet.
func FoldedArrayLen(p *Package, at *ast.ArrayType) (int64, bool) {
	if at.Len == nil {
		return 0, false
	}
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if n, ok := canonicalLen(l); ok {
			return n, true
		}
	}
	if f, ok := foldMap(p).Load(weak.Make(at)); ok {
		return f.(*arrayFold).n, true
	}
	return 0, false
}

// FoldArrayLen publishes n as the evaluated length of at under p's
// session and returns it. It never writes to the AST, so it is safe to
// call concurrently with any reader. The first fold of a node wins — a
// length is one constant per site per session — and later folds are
// no-ops.
func FoldArrayLen(p *Package, at *ast.ArrayType, n int64) int64 {
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if m, ok := canonicalLen(l); ok && m == n {
			return n // already spelled in its folded form
		}
	}
	m := foldMap(p)
	key := weak.Make(at)
	if _, ok := m.Load(key); ok {
		return n
	}
	f := &arrayFold{n: n, lit: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)}}
	if _, loaded := m.LoadOrStore(key, f); !loaded && p == nil {
		// the shared table must not keep a node alive: exactly one
		// storer registers the cleanup; key does not keep at alive, so
		// the entry goes away with the node. An instance's own table
		// dies with the session and needs no cleanup.
		goruntime.AddCleanup(at, forgetArrayFold, key)
	}
	return n
}

// ArrayLenExpr returns the length expression of an array type as type
// identity sees it: the folded literal once p's session knows the
// length, else the expression as written (nil for a slice type). Readers
// that spell or compare lengths use this instead of at.Len.
func ArrayLenExpr(p *Package, at *ast.ArrayType) ast.Expr {
	if at.Len == nil {
		return nil
	}
	if l, ok := at.Len.(*ast.BasicLit); ok {
		if _, ok := canonicalLen(l); ok {
			return l
		}
	}
	if f, ok := foldMap(p).Load(weak.Make(at)); ok {
		return f.(*arrayFold).lit
	}
	return at.Len
}
