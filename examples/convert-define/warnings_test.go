package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
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

	dir := writeFiles(t, files)

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

	if err := run(ctx, defineFile, outputFile, false /* dryRun */, ""); err != nil {
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
