package main

import (
	"context"
	"errors"
	"os"
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
	var fe *formatError
	if !errors.As(err, &fe) {
		t.Fatalf("want *formatError, got %T: %v", err, err)
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
