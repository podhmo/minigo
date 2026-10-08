package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/pkg/gentest"
)

var update = flag.Bool("update", false, "update golden files")

func TestIntegration(t *testing.T) {
	// a lot of files are needed to run the test.
	// - define.go (the conversion definition)
	// - go.mod (for the module root)
	// - source and destination struct files
	// - helper packages (convutil, funcs)
	// we can get these from the library packages in this module.
	sampleSrc, err := os.ReadFile(filepath.Join("sampledata", "source", "source.go"))
	if err != nil {
		t.Fatalf("reading source.go: %v", err)
	}
	sampleDst, err := os.ReadFile(filepath.Join("sampledata", "destination", "destination.go"))
	if err != nil {
		t.Fatalf("reading destination.go: %v", err)
	}
	convutil, err := os.ReadFile(filepath.Join("convutil", "util.go"))
	if err != nil {
		t.Fatalf("reading convutil/util.go: %v", err)
	}
	funcs, err := os.ReadFile(filepath.Join("sampledata", "funcs", "funcs.go"))
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

	dir := gentest.WriteFiles(t, files)

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

	if err := run(ctx, defineFile, outputFile, false /* dryRun */, "", false /* strict */, false /* check */); err != nil {
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

// TestIntegration_GenericAndJSONTag covers two gaps the inspect-based
// rewrite exposed: a field of generic instantiation type ([]box.Box[T])
// needs its base package imported in generated code (Children() does not
// reach IndexExpr.X, so registerImports walks it via TypeExpr.Sub), and
// struct `json:"..."` tags must actually populate FieldInfo.JSONTag for
// the priority-2 shared-json-tag field matching to fire (Src.ID ->
// Dst.UserID via the shared `user_id` tag, without an explicit c.Map).
func TestIntegration_GenericAndJSONTag(t *testing.T) {
	files := map[string]string{
		"go.mod": `
module example.com/m2
go 1.22
`,
		"define.go": `
package main

import (
	"example.com/m2/destination"
	"example.com/m2/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
	})
}
`,
		"box/box.go": `
package box

type Box[T any] struct {
	V T
}
`,
		"source/source.go": `
package source

import "example.com/m2/box"

type Src struct {
	ID   int64            ` + "`json:\"user_id\"`" + `
	Tags []box.Box[string]
}
`,
		"destination/destination.go": `
package destination

import "example.com/m2/box"

type Dst struct {
	UserID int64 ` + "`json:\"user_id\"`" + `
	Tags   []box.Box[string]
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

	goldenFile := filepath.Join(cwd, "testdata", "integration_generic.go.golden")
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

// TestFileWriterCapture exercises the FileWriter seam: the output write
// in run() goes through gentest.WriteFile, so a MemoryFileWriter on the
// context captures generated.go in memory and nothing reaches disk. It
// is the go-scan scantest pattern — host-side generator writes are
// interceptable where interpreted writes (gen-sync's script) are not.
func TestFileWriterCapture(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod": `
module example.com/mw
go 1.22
`,
		"define.go": `
package main

import (
	"example.com/mw/destination"
	"example.com/mw/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Map(dst.UserID, src.ID)
	})
}
`,
		"source/source.go": `
package source

type Src struct {
	ID int64
}
`,
		"destination/destination.go": `
package destination

type Dst struct {
	UserID int64
}
`,
	})

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

	mem := &gentest.MemoryFileWriter{BaseDir: dir}
	ctx = gentest.WithFileWriter(ctx, mem)
	if err := run(ctx, defineFile, outputFile, false /* dryRun */, "", false /* strict */, false /* check */); err != nil {
		t.Fatalf("run failed: %+v", err)
	}

	// the write was captured, not written to disk.
	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Fatalf("a captured write must not reach disk: %v", err)
	}
	got, ok := mem.Outputs["generated.go"]
	if !ok {
		t.Fatalf("nothing captured for generated.go")
	}
	if !strings.Contains(string(got), "src.ID") {
		t.Fatalf("captured output does not contain the mapped assignment:\n%s", got)
	}
}
