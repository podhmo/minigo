package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMain(m *testing.M) {
	strictImports = true
	os.Exit(m.Run())
}

// TestFormatCodeReportsProvenance pins the agent-facing failure report:
// broken generated syntax names the responsible side (the generator),
// the converter and field that emitted it, and a numbered excerpt —
// instead of writing unformatted code that fails later at compile time.
func TestFormatCodeReportsProvenance(t *testing.T) {
	src := `package p

func convertSrcToDst(ctx context.Context, ec *model.ErrorCollector, src *Src) *Dst {
	dst := &Dst{}
	ec.Enter("Items")
	dst.Items = )
	ec.Leave()
	return dst
}
`
	_, err := formatCode(context.Background(), "generated.go", []byte(src))
	var fe *syntaxError
	if !errors.As(err, &fe) {
		t.Fatalf("want *syntaxError, got %T: %v", err, err)
	}
	want := `generated code does not parse (2 errors). This is a convert-define generator bug, not a problem in the define file.
Rerun with -log-level debug to dump the full unformatted source.

generated.go:6:14: expected operand, found ')'
  emitted by: converter convertSrcToDst, field Items
    4 | 	dst := &Dst{}
    5 | 	ec.Enter("Items")
  > 6 | 	dst.Items = )
    7 | 	ec.Leave()
    8 | 	return dst

generated.go:8:2: expected ';', found 'return'
  emitted by: converter convertSrcToDst, field Items
    6 | 	dst.Items = )
    7 | 	ec.Leave()
  > 8 | 	return dst
    9 | }
`
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
}

func TestAddedImports(t *testing.T) {
	src := []byte("package p\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint(strings.ToUpper(\"\"))\n")
	formatted := []byte("package p\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n")
	if diff := cmp.Diff([]string{"strings"}, addedImports(src, formatted)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildConstraintHeader(t *testing.T) {
	tests := []struct {
		tags    string
		want    string
		wantErr string
	}{
		{tags: "", want: ""},
		{tags: "e2e", want: "\n//go:build e2e\n\n"},
		{tags: "linux&&!cgo", want: "\n//go:build linux && !cgo\n\n"},
		{tags: "foo &&", wantErr: `invalid -tags "foo &&": unexpected end of expression. Fix the command-line arguments: -tags takes a build constraint expression, e.g. -tags e2e or -tags 'linux && !cgo'`},
	}
	for _, tt := range tests {
		t.Run(tt.tags, func(t *testing.T) {
			got, err := buildConstraintHeader(tt.tags)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if diff := cmp.Diff(tt.wantErr, gotErr); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("header mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRunReportsDefineSyntaxError pins the input-side failure: a define
// file that does not parse is reported as the user's to fix, with every
// error and a numbered excerpt, and nothing is written.
func TestRunReportsDefineSyntaxError(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"go.mod": "module example.com/bad\ngo 1.22\n",
		"define.go": `//go:build codegen

package main

func main( {
}
`,
	})
	defineFile := filepath.Join(dir, "define.go")
	outputFile := filepath.Join(dir, "generated.go")

	err := run(context.Background(), defineFile, outputFile, false, "")
	var se *syntaxError
	if !errors.As(err, &se) {
		t.Fatalf("want *syntaxError, got %T: %v", err, err)
	}
	got := strings.ReplaceAll(err.Error(), dir, "DIR")
	want := `define file DIR/define.go does not parse (2 errors). Fix the define file; no code was generated.

DIR/define.go:5:12: expected ')', found '{'
    3 | package main
    4 | 
  > 5 | func main( {
    6 | }

DIR/define.go:6:1: missing ',' in parameter list
    4 | 
    5 | func main( {
  > 6 | }
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Errorf("output must not be written, stat err = %v", err)
	}
}
