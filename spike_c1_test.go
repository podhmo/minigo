//go:build minigo_spike

package minigo_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/spike"
)

// TestSpikeNoInitDuringCompile asserts the C1 invariant: package
// initialization never runs while compilation is in progress
// (docs/next/ja/references/spike-instrumentation.md). It also reports
// the compile-caused build count and the chunkCellsEscape rate so the
// run doubles as a sample for those counters.
func TestSpikeNoInitDuringCompile(t *testing.T) {
	spike.Reset()
	e := minigo.NewEngine(".")
	if _, err := e.Run(context.Background(), "./testdata/spikeinit", "Answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if diff := cmp.Diff(int64(0), spike.InitDuringCompile()); diff != "" {
		t.Errorf("C1 violations (init during compile) mismatch (-want +got):\n%s", diff)
	}
	t.Logf("builds during compile: %d", spike.CompileBuilds())
	t.Logf("chunkCellsEscape: %d/%d chunks escape", spike.ChunksEscaped(), spike.ChunksScanned())
}

// TestSpikeStrictLazyNoInitDuringCompile is the C1 invariant under
// WithStrictLazyCompile (M4-8a): strict lazy must make compile-time
// initialization impossible, not merely absent from this sample.
func TestSpikeStrictLazyNoInitDuringCompile(t *testing.T) {
	spike.Reset()
	e := minigo.NewEngine(".", minigo.WithStrictLazyCompile())
	if _, err := e.Run(context.Background(), "./testdata/spikeinit", "Answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if diff := cmp.Diff(int64(0), spike.InitDuringCompile()); diff != "" {
		t.Errorf("C1 violations under strict lazy (init during compile) mismatch (-want +got):\n%s", diff)
	}
	t.Logf("builds during compile: %d", spike.CompileBuilds())
}
