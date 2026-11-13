package minigo_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestHostCallbackRacesProcRelease fires a host callback
// (context.AfterFunc) on a foreign goroutine right as the run that armed
// it ends: the callback's Call checks the VM's process while the run's
// ReleaseProc clears it. The window is narrow, so several goroutines
// repeat it; the race detector (go test -race, CI's race-test) is what
// fails when the process pointer is read unsynchronized.
func TestHostCallbackRacesProcRelease(t *testing.T) {
	e := newEngine(t)
	pkg, err := e.Package(context.Background(), "./testdata/procrace")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 300 {
				got, err := e.Call(context.Background(), pkg, "CancelThenReturn")
				if err != nil {
					t.Error(err)
					return
				}
				if diff := cmp.Diff("ok", got); diff != "" {
					t.Errorf("CancelThenReturn mismatch (-want +got):\n%s", diff)
					return
				}
			}
		})
	}
	wg.Wait()
}
