package runtime

import (
	"go/ast"
	"go/token"
	goruntime "runtime"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/google/go-cmp/cmp"
)

func arrayOf(len ast.Expr) *ast.ArrayType {
	return &ast.ArrayType{Len: len, Elt: ast.NewIdent("int")}
}

func intLit(s string) *ast.BasicLit { return &ast.BasicLit{Kind: token.INT, Value: s} }

func TestFoldedArrayLen(t *testing.T) {
	cases := []struct {
		name     string
		at       *ast.ArrayType
		known    bool  // a length is known before any fold
		wantKnow int64 // that length
	}{
		{"canonical", arrayOf(intLit("3")), true, 3},
		{"zero", arrayOf(intLit("0")), true, 0},
		{"slice", &ast.ArrayType{Elt: ast.NewIdent("int")}, false, 0},
		{"named const", arrayOf(ast.NewIdent("N")), false, 0},
		{"ellipsis", arrayOf(&ast.Ellipsis{}), false, 0},
		{"hex", arrayOf(intLit("0x10")), false, 0},
		{"octal", arrayOf(intLit("010")), false, 0},
		{"separator", arrayOf(intLit("1_000")), false, 0},
		{"float", arrayOf(&ast.BasicLit{Kind: token.FLOAT, Value: "1e1"}), false, 0},
		{"char", arrayOf(&ast.BasicLit{Kind: token.CHAR, Value: "'a'"}), false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := FoldedArrayLen(c.at)
			if ok != c.known || n != c.wantKnow {
				t.Fatalf("FoldedArrayLen before fold = (%d, %v), want (%d, %v)", n, ok, c.wantKnow, c.known)
			}
			if c.at.Len == nil {
				if got := ArrayLenExpr(c.at); got != nil {
					t.Fatalf("ArrayLenExpr of a slice = %v, want nil", got)
				}
				return
			}
			if c.known {
				return // a canonical literal is already its folded form
			}
			before := c.at.Len
			if got := FoldArrayLen(c.at, 7); got != 7 {
				t.Fatalf("FoldArrayLen returned %d, want 7", got)
			}
			if c.at.Len != before {
				t.Fatalf("FoldArrayLen rewrote the AST: Len %v -> %v", before, c.at.Len)
			}
			n, ok = FoldedArrayLen(c.at)
			if !ok || n != 7 {
				t.Fatalf("FoldedArrayLen after fold = (%d, %v), want (7, true)", n, ok)
			}
			lit, ok := ArrayLenExpr(c.at).(*ast.BasicLit)
			if !ok {
				t.Fatalf("ArrayLenExpr after fold is %T, want *ast.BasicLit", ArrayLenExpr(c.at))
			}
			if diff := cmp.Diff(&ast.BasicLit{Kind: token.INT, Value: "7"}, lit); diff != "" {
				t.Errorf("folded literal mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// A canonical literal is its own folded form: folding its own count
// records nothing, so its readers never consult the side table.
func TestFoldArrayLenCanonicalIsNotRecorded(t *testing.T) {
	at := arrayOf(intLit("4"))
	FoldArrayLen(at, 4)
	if _, ok := arrayFolds.Load(weak.Make(at)); ok {
		t.Errorf("a canonical literal length was recorded in the fold table")
	}
	if got, want := ArrayLenExpr(at), ast.Expr(at.Len); got != want {
		t.Errorf("ArrayLenExpr = %v, want the written literal %v", got, want)
	}
}

// Spelling follows the fold without the AST changing: `[N]int` spells
// `[3]int` once the length is known, like Go's constant evaluation.
func TestSpellingFollowsFold(t *testing.T) {
	at := arrayOf(ast.NewIdent("N"))
	if got, want := TypSpelling(at, nil), "[N]int"; got != want {
		t.Fatalf("before fold: %q, want %q", got, want)
	}
	FoldArrayLen(at, 3)
	if got, want := TypSpelling(at, nil), "[3]int"; got != want {
		t.Errorf("after fold: %q, want %q", got, want)
	}
	if got, want := anonTag(at), "[3]int"; got != want {
		t.Errorf("anonTag after fold: %q, want %q", got, want)
	}
	if id, ok := at.Len.(*ast.Ident); !ok || id.Name != "N" {
		t.Errorf("the AST length was rewritten: %#v", at.Len)
	}
}

// Folds are published beside the AST, so goroutines spelling, folding
// and reading the same array types never race (run with -race).
func TestFoldArrayLenConcurrent(t *testing.T) {
	nodes := []*ast.ArrayType{
		arrayOf(ast.NewIdent("N")),
		arrayOf(intLit("0x3")),
		arrayOf(&ast.Ellipsis{}),
		arrayOf(intLit("3")),
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				for _, at := range nodes {
					FoldArrayLen(at, 3)
					_ = TypSpelling(at, nil)
					_, _ = FoldedArrayLen(at)
					_ = ArrayLenExpr(at)
					_ = arrayTypedef(&TypeDef{Anon: at})
				}
			}
		}()
	}
	wg.Wait()
	for _, at := range nodes {
		if got, want := TypSpelling(at, nil), "[3]int"; got != want {
			t.Errorf("%T len %v spells %q, want %q", at.Len, at.Len, got, want)
		}
	}
}

// foldFresh folds n fresh non-canonical array types and returns the weak
// keys their entries are stored under. The nodes are unreachable when it
// returns.
//
//go:noinline
func foldFresh(t *testing.T, n int) []weak.Pointer[ast.ArrayType] {
	t.Helper()
	keys := make([]weak.Pointer[ast.ArrayType], 0, n)
	for i := 0; i < n; i++ {
		at := arrayOf(ast.NewIdent("N"))
		FoldArrayLen(at, 3)
		key := weak.Make(at)
		if _, ok := arrayFolds.Load(key); !ok {
			t.Fatalf("fold %d was not stored while its node is live", i)
		}
		keys = append(keys, key)
	}
	return keys
}

// The fold table must not keep a program alive: a host that loads a
// fresh engine per request would otherwise leak one entry per
// non-canonical length site per load. An entry goes away with its node.
func TestFoldTableReleasesEntries(t *testing.T) {
	keys := foldFresh(t, 64)
	live := func() int {
		n := 0
		for _, k := range keys {
			if _, ok := arrayFolds.Load(k); ok {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for live() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d fold entries outlived their nodes", live(), len(keys))
		}
		goruntime.GC()
		time.Sleep(5 * time.Millisecond) // cleanups run on their own goroutine
	}
}
