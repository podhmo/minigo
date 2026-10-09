package minigo

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestTemplateHostLexer: a source-interpreted text/template/parse lexes
// on the host, and the parse trees it builds still behave like Go's.
func TestTemplateHostLexer(t *testing.T) {
	var buf bytes.Buffer
	e := NewEngine(".", WithOutput(&buf), WithPackageModes(map[string]PackageMode{
		"text/template": ModeSource, "text/template/parse": ModeSource,
	}))
	before := hostLexerStarts.Load()
	if _, err := e.Run(context.Background(), "./testdata/difffuzz/template_src_hostparse", ""); err != nil {
		t.Fatal(err)
	}
	if hostLexerStarts.Load() == before {
		t.Errorf("text/template/parse lexed by the interpreted lexer")
	}
	if diff := cmp.Diff("Items: [nut 1.25 m3,steel], [bolt 0.50]", strings.SplitN(buf.String(), "\n", 2)[0]); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}
