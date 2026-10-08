package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/pkg/gentest"
)

// TestIntegration_LeafMismatchWarnings covers the fuzz-round residual:
// a leaf field pair that no rule and no cast covers (int -> string,
// []int -> []string) previously emitted an uncompilable assignment
// with no diagnostics. Generation now keeps the honest output and
// flags the pair in the converter's doc comment. It also proves the
// builtin casts that do exist (string <-> []byte/[]rune) emit them.
func TestIntegration_LeafMismatchWarnings(t *testing.T) {
	files := map[string]string{
		"go.mod": `
module example.com/warn
go 1.22
`,
		"define.go": `
package main

import (
	"example.com/warn/destination"
	"example.com/warn/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
	})
}
`,
		"source/source.go": `package source

type Src struct {
	Age  int
	Nums []int
	Data []byte
	Name string
}
`,
		"destination/destination.go": `package destination

type Dst struct {
	Age  string
	Nums []string
	Data string
	Name []byte
}
`,
	}

	dir := gentest.WriteFiles(t, files)

	ctx := context.Background()
	defineFile := filepath.Join(dir, "define.go")
	outputFile := filepath.Join(dir, "generated.go")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("could not chdir to temp dir: %v", err)
	}
	defer os.Chdir(cwd)

	if err := run(ctx, defineFile, outputFile, false /* dryRun */, "", false /* strict */, false /* check */); err != nil {
		t.Fatalf("run failed: %+v", err)
	}

	got, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("reading generated.go: %v", err)
	}

	goldenFile := filepath.Join(cwd, "testdata", "integration_warnings.go.golden")
	if *update {
		if err := os.WriteFile(goldenFile, got, 0644); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
	}

	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("generated code mismatch (-want +got):\n%s", diff)
	}
}

// TestRunStrictRejectsLeafMismatch pins -strict: the same leaf
// mismatches that only warn by default fail the run, listing each
// converter/field and the input-side fix, and nothing is written.
func TestRunStrictRejectsLeafMismatch(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod": "module example.com/strict\ngo 1.22\n",
		"define.go": `
package main

import (
	"example.com/strict/destination"
	"example.com/strict/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
	})
}
`,
		"source/source.go":           "package source\n\ntype Src struct {\n\tAge  int\n\tName string\n}\n",
		"destination/destination.go": "package destination\n\ntype Dst struct {\n\tAge  string\n\tName string\n}\n",
	})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	outputFile := filepath.Join(dir, "generated.go")
	err = run(context.Background(), filepath.Join(dir, "define.go"), outputFile, false, "", true /* strict */, false /* check */)
	want := "-strict: 1 field pair(s) would not compile; no output was written. Fix the define file or the types: add a define.Rule for the type pair, or c.Convert the field with a converter function.\n" +
		"  - convertSrcToDst: dst.Age: no conversion covers int -> string\n"
	if err == nil {
		t.Fatal("want an error in strict mode")
	}
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Errorf("output must not be written, stat err = %v", err)
	}
}

// TestRunStrictRejectsGenericInstantiation pins the named-composite
// cul-de-sac: `type SrcList List[int]` unwraps to the instantiation
// `List[int]` — the spec beyond is parametric ([]T), so no
// element-wise shape is reachable and the leafCast optimism would emit
// a cast that cannot compile. The pair warns (and -strict fails).
func TestRunStrictRejectsGenericInstantiation(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod": "module example.com/geninst\ngo 1.22\n",
		"define.go": `
package main

import (
	"example.com/geninst/destination"
	"example.com/geninst/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
	})
}
`,
		"source/source.go":           "package source\n\ntype List[T any] []T\n\ntype SrcList List[int]\n\ntype Src struct {\n\tV SrcList\n}\n",
		"destination/destination.go": "package destination\n\ntype List[T any] []T\n\ntype DstList List[int64]\n\ntype Dst struct {\n\tV DstList\n}\n",
	})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	outputFile := filepath.Join(dir, "generated.go")
	err = run(context.Background(), filepath.Join(dir, "define.go"), outputFile, false, "", true /* strict */, false /* check */)
	want := "-strict: 1 field pair(s) would not compile; no output was written. Fix the define file or the types: add a define.Rule for the type pair, or c.Convert the field with a converter function.\n" +
		"  - convertSrcToDst: dst.V: no conversion covers source.SrcList -> destination.DstList (generic instantiation)\n"
	if err == nil {
		t.Fatal("want an error in strict mode")
	}
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestRunStrictChecksComputeTypes(t *testing.T) {
	const prefix = "-strict: 1 field pair(s) would not compile; no output was written. Fix the define file or the types: add a define.Rule for the type pair, or c.Convert the field with a converter function.\n"
	cases := []struct {
		name    string
		compute string
		want    string // "" means generation succeeds
	}{
		{"src field mismatch", "c.Compute(dst.Value, src.N)", "  - convertSrcToDst: dst.Value: c.Compute expression src.N is int, not string\n"},
		{"src field match", "c.Compute(dst.Value, src.S)", ""},
		{"func result mismatch", "c.Compute(dst.Value, funcs.Count(src.S))", "  - convertSrcToDst: dst.Value: c.Compute expression funcs.Count(src.S) is int, not string\n"},
		{"func result match", "c.Compute(dst.Value, funcs.Itoa(src.N))", ""},
		{"generic func is unknown", "c.Compute(dst.Value, funcs.Same(src.N))", ""},
		{"other expressions are unknown", `c.Compute(dst.Value, src.S + "!")`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := gentest.WriteFiles(t, map[string]string{
				"go.mod": "module example.com/compute\ngo 1.22\n",
				"define.go": `
package main

import (
	"example.com/compute/destination"
	"example.com/compute/funcs"
	"example.com/compute/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		` + tc.compute + `
	})
}
`,
				"source/source.go":           "package source\n\ntype Src struct {\n\tN int\n\tS string\n}\n",
				"destination/destination.go": "package destination\n\ntype Dst struct {\n\tValue string\n}\n",
				"funcs/funcs.go":             "package funcs\n\nimport \"strconv\"\n\nfunc Itoa(n int) string { return strconv.Itoa(n) }\n\nfunc Count(s string) int { return len(s) }\n\nfunc Same[T any](v T) T { return v }\n",
			})
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(cwd)

			err = run(context.Background(), filepath.Join(dir, "define.go"), filepath.Join(dir, "generated.go"), true /* dryRun */, "", true /* strict */, false /* check */)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error in strict mode")
			}
			if diff := cmp.Diff(prefix+tc.want, err.Error()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
