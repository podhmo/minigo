package execution

import (
	"sync"
	"testing"
	"time"
)

// TestIndexGateUnmarkedIsReady pins the bound-package case: a gate
// nobody marked never blocks.
func TestIndexGateUnmarkedIsReady(t *testing.T) {
	var g IndexGate
	if !g.Ready() {
		t.Fatal("unmarked gate is not ready")
	}
	g.Wait() // must not block
}

// TestIndexGateWaitersReleaseOnFinish exercises the cold-load race:
// readers mark time while the loader finishes indexing.
func TestIndexGateWaitersReleaseOnFinish(t *testing.T) {
	var g IndexGate
	g.Mark()
	if g.Ready() {
		t.Fatal("marked gate reported ready before finish")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Wait()
		}()
	}
	time.Sleep(10 * time.Millisecond)
	g.Finish()
	wg.Wait()
	if !g.Ready() {
		t.Fatal("finished gate is not ready")
	}
}

// TestIndexGateFinishWithoutMark does not panic — finishing a gate that
// was never marked is a no-op.
func TestIndexGateFinishWithoutMark(t *testing.T) {
	var g IndexGate
	g.Finish()
	if !g.Ready() {
		t.Fatal("gate finished without mark is not ready")
	}
}
