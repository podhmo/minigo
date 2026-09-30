package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/go-scan/scantest"
)

var update = flag.Bool("update", false, "update golden files")

func TestIntegration(t *testing.T) {
	// a lot of files are needed to run the test.
	// - define.go (the conversion definition)
	// - go.mod (for the module root)
	// - source and destination struct files
	// - helper packages (convutil, funcs)
	// we can get these from the `examples/convert` directory.
	baseDir := filepath.Join("testdata", "convert")
	sampleSrc, err := os.ReadFile(filepath.Join(baseDir, "sampledata", "source", "source.go"))
	if err != nil {
		t.Fatalf("reading source.go: %v", err)
	}
	sampleDst, err := os.ReadFile(filepath.Join(baseDir, "sampledata", "destination", "destination.go"))
	if err != nil {
		t.Fatalf("reading destination.go: %v", err)
	}
	convutil, err := os.ReadFile(filepath.Join(baseDir, "convutil", "util.go"))
	if err != nil {
		t.Fatalf("reading convutil/util.go: %v", err)
	}
	funcs, err := os.ReadFile(filepath.Join(baseDir, "sampledata", "funcs", "funcs.go"))
	if err != nil {
		t.Fatalf("reading funcs/funcs.go: %v", err)
	}

	files := map[string]string{
		"go.mod": `
module example.com/m
go 1.22
replace github.com/podhmo/minigo/examples/convert-define/define => ../define
`,
		"define.go": `
package main

import (
	"example.com/m/convutil"
	"example.com/m/sampledata/destination"
	"example.com/m/sampledata/funcs"
	"example.com/m/sampledata/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Rule(convutil.TimeToString)
	define.Rule(convutil.PtrTimeToString)
	define.Convert(func(c *define.Config, dst *destination.DstUser, src *source.SrcUser) {
		c.Map(dst.UserID, src.ID)
		c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)
		c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))
	})
}
`,
		"sampledata/source/source.go":           string(sampleSrc),
		"sampledata/destination/destination.go": string(sampleDst),
		"convutil/util.go":                      string(convutil),
		"sampledata/funcs/funcs.go":             string(funcs),
	}

	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	ctx := context.Background()
	defineFile := filepath.Join(dir, "define.go")
	outputFile := filepath.Join(dir, "generated.go")

	// Since the `run` function creates its own scanner, we need to trick it
	// into using the correct module root. We do this by changing the current
	// working directory for the duration of the test.
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

	goldenFile := filepath.Join(cwd, "testdata", "integration.go.golden")
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
