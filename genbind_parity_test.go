package minigo

// P2-13: the generated bind halves agree with source interpretation.

import (
	"context"
	"fmt"
	"go/ast"
	"regexp"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// genbindPackages lists every package with a generated bind half (the
// specs in internal/cmd/genbind/table.go, in the same order).
var genbindPackages = []string{
	"encoding/hex",
	"encoding/base64",
	"crypto/sha256",
	"html",
	"path",
	"strconv",
	"unicode/utf8",
	"path/filepath",
	"net/netip",
	"unicode",
	"bytes",
	"context",
}

// exportedOf collects the names a source-interpreted package exports.
func exportedOf(ix *index.Index) []string {
	var out []string
	for name := range ix.Funcs {
		if ast.IsExported(name) {
			out = append(out, name)
		}
	}
	for name := range ix.Vars {
		if ast.IsExported(name) {
			out = append(out, name)
		}
	}
	for name := range ix.Consts {
		if ast.IsExported(name) {
			out = append(out, name)
		}
	}
	for name := range ix.Types {
		if ast.IsExported(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestGeneratedBindCoversSourceExports checks the bound table exposes
// every member the source-interpreted package exports — the generated
// half plus whatever custom adapters stay handwritten.
func TestGeneratedBindCoversSourceExports(t *testing.T) {
	ctx := context.Background()
	for _, path := range genbindPackages {
		se := NewEngine(".", WithPackageModes(map[string]PackageMode{path: ModeSource}))
		sp, err := se.Package(ctx, path)
		if err != nil {
			t.Fatalf("%s: source load: %v", path, err)
		}
		want := exportedOf(sp.Index)

		be := NewEngine(".")
		names, ok := be.BoundSymbols(path)
		if !ok {
			t.Fatalf("%s: not bound", path)
		}
		have := map[string]bool{}
		for _, n := range names {
			have[n] = true
		}
		var missing []string
		for _, n := range want {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s: bound table misses source-exported members: %v", path, missing)
		}
	}
}

// hexPtr normalizes pointer-shaped hex literals so rendered host values
// compare equal across engines.
var hexPtr = regexp.MustCompile(`0x[0-9a-f]+`)

// norm renders a script value through its host view — the two engines
// hold results in different internal shapes for the same value.
func norm(v runtime.Value) string {
	if t, ok := v.(*runtime.Tuple); ok {
		out := make([]any, len(t.Elems))
		for i, e := range t.Elems {
			out[i] = goNative(e)
		}
		return fmt.Sprint(out)
	}
	return hexPtr.ReplaceAllString(fmt.Sprint(goNative(v)), "0xPTR")
}

// TestGeneratedBindCallParity runs the same call against the bound and
// the source-interpreted package and compares rendered results.
func TestGeneratedBindCallParity(t *testing.T) {
	ctx := context.Background()
	calls := []struct {
		path, name string
		args       []any
	}{
		{"encoding/hex", "EncodeToString", []any{[]byte{0xde, 0xad}}},
		{"encoding/hex", "DecodeString", []any{"dead"}},
		{"html", "EscapeString", []any{"<a>"}},
		{"path", "Join", []any{"a", "b"}},
		{"strconv", "Itoa", []any{int64(-42)}},
		{"strconv", "Quote", []any{"a\nb"}},
		{"unicode/utf8", "RuneCountInString", []any{"héllo"}},
		{"unicode/utf8", "ValidString", []any{"ok"}},
		{"path/filepath", "Base", []any{"a/b/c"}},
		{"unicode", "IsDigit", []any{int64('5')}},
		{"unicode", "ToUpper", []any{int64('a')}},
		{"bytes", "Contains", []any{[]byte("haystack"), []byte("stack")}},
	}
	for _, c := range calls {
		be := NewEngine(".")
		bp, err := be.Package(ctx, c.path)
		if err != nil {
			t.Fatalf("%s: bound load: %v", c.path, err)
		}
		se := NewEngine(".", WithPackageModes(map[string]PackageMode{c.path: ModeSource}))
		sp, err := se.Package(ctx, c.path)
		if err != nil {
			t.Fatalf("%s: source load: %v", c.path, err)
		}
		bg, berr := be.Call(ctx, bp, c.name, c.args...)
		sg, serr := se.Call(ctx, sp, c.name, c.args...)
		if (berr == nil) != (serr == nil) {
			t.Errorf("%s.%s: bound err %v, source err %v", c.path, c.name, berr, serr)
			continue
		}
		if berr != nil {
			t.Logf("%s.%s: both engines erred (%v)", c.path, c.name, berr)
			continue
		}
		if diff := cmp.Diff(norm(sg), norm(bg)); diff != "" {
			t.Errorf("%s.%s parity mismatch (-source +bound):\n%s", c.path, c.name, diff)
		}
	}
}
