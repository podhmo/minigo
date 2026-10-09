package tmpllex

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestLexMatchesGOROOT keeps lex.go a verbatim copy of the toolchain's
// text/template/parse/lex.go: re-copy it when this fails after a Go
// upgrade (only the package clause differs).
func TestLexMatchesGOROOT(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(build.Default.GOROOT, "src", "text", "template", "parse", "lex.go"))
	if err != nil {
		t.Skipf("GOROOT lex.go: %v", err)
	}
	if !SameSource(src) {
		want := strings.Replace(string(src), "\npackage parse\n", "\npackage tmpllex\n", 1)
		t.Errorf("lex.go drifted from GOROOT (-want +got):\n%s", cmp.Diff(want, source))
	}
}

func TestNextItem(t *testing.T) {
	x := New("t", "a{{.X | f}}b{{/* c */}}", "", "")
	x.SetOptions(Options{EmitComment: true})
	var got []string
	for {
		it := x.NextItem()
		got = append(got, it.Val)
		if it.Typ == ItemEOF || it.Typ == ItemError {
			break
		}
	}
	want := []string{"a", "{{", ".X", " ", "|", " ", "f", "}}", "b", "/* c */", ""}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
}
