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

// writeModuleFiles is writeFiles inside this module (under testdata/),
// so `go build` in -check resolves the generated code's model import
// without a network fetch. It returns the dir and its import path.
func writeModuleFiles(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("testdata", "check-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs, "github.com/podhmo/minigo/examples/convert-define/testdata/" + filepath.Base(dir)
}

func TestRunCheck(t *testing.T) {
	define := func(pkg, body string) string {
		return `//go:build codegen

package gen

import (
	"` + pkg + `/destination"
	"` + pkg + `/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
		` + body + `
	})
}
`
	}
	cases := []struct {
		name    string
		compute string
		source  string
		dest    string // default: type B struct{ V int }
		want    string // "" means success and the output is written
	}{
		{
			name:    "ok",
			compute: `c.Compute(dst.V, len(src.V))`,
			source:  "package source\n\ntype A struct{ V string }\n",
		},
		{
			// The template's own fmt import used to be emitted twice.
			name:    "fmt in a dst type and in a map loop",
			compute: ``,
			source:  "package source\n\ntype Name string\n\nfunc (n Name) String() string { return string(n) }\n\ntype A struct {\n\tS Name\n\tM map[string]int\n}\n",
			dest:    "package destination\n\nimport \"fmt\"\n\ntype B struct {\n\tS fmt.Stringer\n\tM map[string]int\n}\n",
		},
		{
			// A same-named field written by c.Compute/c.Map used to be
			// auto-matched too, emitting an ill-typed dead assignment.
			name:    "compute and map claim same-named dst fields",
			compute: "c.Compute(dst.V, len(src.V))\n\t\tc.Map(dst.W, src.X)",
			source:  "package source\n\ntype A struct {\n\tV string\n\tW string\n\tX int\n}\n",
			dest:    "package destination\n\ntype B struct {\n\tV int\n\tW int\n}\n",
		},
		{
			name:    "type error traced to the field",
			compute: `c.Compute(dst.V, src.V + "!")`,
			source:  "package source\n\ntype A struct{ V string }\n",
			want: `-check: generated code does not compile (1 errors); no output was written.
Each error names the converter and field that emitted it. Fix the define file first (add a define.Rule for the type pair, c.Convert the field, or fix the c.Compute expression); if the define file is right, this is a convert-define generator bug.

DIR/generated.go:23:10: cannot use src.V + "!" (value of type string) as int value in assignment
  emitted by: converter convertAToB, field V
    21 | 	}
    22 | 	ec.Enter("V")
  > 23 | 	dst.V = src.V + "!"
    24 | 	ec.Leave()
    25 | 	return dst
`,
		},
		{
			name:    "broken input is inconclusive",
			compute: `c.Compute(dst.V, len(src.V))`,
			source:  "package source\n\ntype A struct{ V string }\n\nvar _ int = \"no\"\n",
			want: `-check: inconclusive: the package does not build for reasons outside the generated file; no output was written.
Fix the input packages first (or rerun without -check to regenerate anyway):
  source/source.go:5:13: cannot use "no" (untyped string constant) as int value in variable declaration
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := tc.dest
			if dest == "" {
				dest = "package destination\n\ntype B struct{ V int }\n"
			}
			dir, pkg := writeModuleFiles(t, map[string]string{
				"source/source.go":           tc.source,
				"destination/destination.go": dest,
			})
			if err := os.WriteFile(filepath.Join(dir, "define.go"), []byte(define(pkg, tc.compute)), 0o644); err != nil {
				t.Fatal(err)
			}
			outputFile := filepath.Join(dir, "generated.go")
			err := run(context.Background(), filepath.Join(dir, "define.go"), outputFile, false, "", false /* strict */, true /* check */)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got: %v", err)
				}
				if _, err := os.Stat(outputFile); err != nil {
					t.Errorf("output must be written: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if diff := cmp.Diff(tc.want, strings.ReplaceAll(err.Error(), dir, "DIR")); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if _, err := os.Stat(outputFile); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("output must not be written, stat err = %v", err)
			}
		})
	}
}

func TestSatisfyingTags(t *testing.T) {
	cases := []struct {
		expr string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"e2e", []string{"e2e"}, false},
		{"a && !b", []string{"a"}, false},
		{"a || b", []string{"a"}, false},
		{"!a", nil, false},
		{"a && !a", nil, true},
	}
	for _, tc := range cases {
		got, err := satisfyingTags(tc.expr)
		if (err != nil) != tc.err {
			t.Errorf("%q: err = %v, want error %v", tc.expr, err, tc.err)
			continue
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%q: mismatch (-want +got):\n%s", tc.expr, diff)
		}
	}
}
