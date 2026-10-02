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
  emitted by: converter convertSrcToDst (outside any field)
    6 | 	dst.Items = )
    7 | 	ec.Leave()
  > 8 | 	return dst
    9 | }
`
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
}

// TestFormatCodeRejectsMissingImports pins that an import goimports
// has to add fails the run (it would hide an ImportManager bug, and the
// guessed package can be the wrong one), naming the path and the
// converter/field that first uses it.
func TestFormatCodeRejectsMissingImports(t *testing.T) {
	src := `package p

import (
	"context"

	"github.com/podhmo/minigo/examples/convert-define/model"
)

func convertSrcToDst(ctx context.Context, ec *model.ErrorCollector, src *Src) *Dst {
	dst := &Dst{}
	ec.Enter("Name")
	dst.Name = strings.ToUpper(src.Name)
	ec.Leave()
	return dst
}
`
	_, err := formatCode(context.Background(), "generated.go", []byte(src))
	var me *missingImportsError
	if !errors.As(err, &me) {
		t.Fatalf("want *missingImportsError, got %T: %v", err, err)
	}
	want := `generated code uses 1 package(s) the generator did not import. This is a convert-define generator bug (ImportManager missed a registration), not a problem in the define file; no output was written.

missing import "strings" (referenced as strings.)
  first used by: converter convertSrcToDst, field Name
    11 | 	ec.Enter("Name")
  > 12 | 	dst.Name = strings.ToUpper(src.Name)
    13 | 	ec.Leave()
`
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
}

func TestProvenanceBalancesEnterLeave(t *testing.T) {
	lines := strings.Split(`func convertAToB(ctx context.Context, ec *model.ErrorCollector, src *A) *B {
	ec.Enter("Items")
	for i, item := range src.Items {
		ec.Enter(fmt.Sprintf("[%d]", i))
		s[i] = item
		ec.Leave()
	}
	dst.Items = s
	ec.Leave()
	return dst
}`, "\n")
	tests := []struct {
		line        int
		conv, field string
	}{
		{line: 5, conv: "convertAToB", field: "Items"}, // inside an element segment
		{line: 8, conv: "convertAToB", field: "Items"}, // after the element loop, still in the field
		{line: 10, conv: "convertAToB", field: ""},     // after the field's Leave
	}
	for _, tt := range tests {
		conv, field := provenance(lines, tt.line)
		if diff := cmp.Diff([2]string{tt.conv, tt.field}, [2]string{conv, field}); diff != "" {
			t.Errorf("line %d mismatch (-want +got):\n%s", tt.line, diff)
		}
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

	err := run(context.Background(), defineFile, outputFile, false, "", false)
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

// TestRunReportsDSLMisuse pins the DSL-misuse report: the user's to
// fix, at the offending c.Map call (not the enclosing define.Convert),
// with an excerpt; the DSL call path appears only when a helper was
// involved. Nothing is written.
func TestRunReportsDSLMisuse(t *testing.T) {
	types := map[string]string{
		"go.mod":                     "module example.com/dslerr\ngo 1.22\n",
		"source/source.go":           "package source\n\ntype A struct{ V int }\n",
		"destination/destination.go": "package destination\n\ntype B struct{ V int }\n",
	}
	tests := []struct {
		name   string
		define string
		want   string
	}{
		{
			name: "in main",
			define: `package main

import (
	"example.com/dslerr/destination"
	"example.com/dslerr/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
		c.Map(dst.V, src.W)
	})
}
`,
			want: `define file DIR/define.go is invalid at 11:3: c.Map: source: field path "W": A has no field "W"
Fix the define file at that position; no code was generated.

     9 | func main() {
    10 | 	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
  > 11 | 		c.Map(dst.V, src.W)
    12 | 	})
    13 | }
`,
		},
		{
			name: "via a helper",
			define: `package main

import (
	"example.com/dslerr/destination"
	"example.com/dslerr/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	register()
}

func register() {
	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
		c.Map(dst.W, src.V)
	})
}
`,
			want: `define file DIR/define.go is invalid at 15:3: c.Map: destination: field path "W": B has no field "W"
Fix the define file at that position; no code was generated.

    13 | func register() {
    14 | 	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
  > 15 | 		c.Map(dst.W, src.V)
    16 | 	})
    17 | }

reached via (most recent call first):
  File "DIR/define.go", line 14, in register()
      define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
  File "DIR/define.go", line 10, in main()
      register()
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"define.go": tt.define}
			for k, v := range types {
				files[k] = v
			}
			dir := writeFiles(t, files)
			outputFile := filepath.Join(dir, "generated.go")
			err := run(context.Background(), filepath.Join(dir, "define.go"), outputFile, false, "", false)
			var de *dslError
			if !errors.As(err, &de) {
				t.Fatalf("want *dslError, got %T: %v", err, err)
			}
			got := strings.ReplaceAll(err.Error(), dir, "DIR")
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("report mismatch (-want +got):\n%s", diff)
			}
			if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
				t.Errorf("output must not be written, stat err = %v", err)
			}
		})
	}
}

func TestRunRejectsBadDefineFile(t *testing.T) {
	dir := t.TempDir()
	const fix = "Fix the command-line arguments: -file takes the path of a Go define file (e.g. -file ./define.go)"
	cases := []struct {
		name string
		file string
		want string
	}{
		{"missing", filepath.Join(dir, "nope.go"), "define file DIR/nope.go does not exist. " + fix},
		{"directory", dir, "define file DIR is a directory. " + fix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(context.Background(), tc.file, filepath.Join(dir, "generated.go"), false, "", false)
			if err == nil {
				t.Fatal("want an error")
			}
			if diff := cmp.Diff(tc.want, strings.ReplaceAll(err.Error(), dir, "DIR")); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
