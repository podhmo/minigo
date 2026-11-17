//go:build minigo_spike

package minigo_test

import (
	"context"
	"testing"

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
	if got := spike.InitDuringCompile(); got != 0 {
		t.Errorf("C1 violations (init during compile) = %d, want 0", got)
	}
	t.Logf("builds during compile: %d", spike.CompileBuilds())
	t.Logf("chunkCellsEscape: %d/%d chunks escape", spike.ChunksEscaped(), spike.ChunksScanned())
}
