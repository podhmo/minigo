//go:build minigo_spike

package minigo_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/spike"
)

// TestSpikeSlotClassification measures the P4-2 classification over the
// difffuzz corpus: the share of compiled chunks carrying a boxed slot
// and the share of local slots boxed — the "escape ~10%" target of
// P4-2. Run errors are tolerated: PENDING cases diverge on purpose, and
// their chunks still classified at compile time.
func TestSpikeSlotClassification(t *testing.T) {
	spike.Reset()
	dirs, err := filepath.Glob("testdata/difffuzz/*/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range dirs {
		dir := filepath.Dir(f)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			e := minigo.NewEngine(".")
			_, _ = e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
		})
	}
	classified, anyBoxed := spike.ChunksClassified(), spike.ChunksBoxedAny()
	locals, boxed := spike.SlotsLocal(), spike.SlotsBoxed()
	t.Logf("classified chunks: %d, carrying a boxed slot: %d (%.1f%%)",
		classified, anyBoxed, 100*float64(anyBoxed)/float64(max(classified, 1)))
	t.Logf("local slots: %d, boxed: %d (%.1f%%)",
		locals, boxed, 100*float64(boxed)/float64(max(locals, 1)))
	t.Logf("run-time escape checks: %d", spike.ChunksScanned())
	t.Logf("plain slots promoted to cells at run time: %d", spike.SlotsPromoted())
	for why, n := range spike.BoxReasons() {
		t.Logf("box[%s]: %d", why, n)
	}
}
