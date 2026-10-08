package vm

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGoroutineID(t *testing.T) {
	self := goroutineID()
	if self == 0 {
		t.Fatal("goroutineID() = 0 on the test goroutine")
	}
	if diff := cmp.Diff(self, goroutineID()); diff != "" {
		t.Errorf("same goroutine, different ids (-first +second):\n%s", diff)
	}

	// goroutines alive at the same time never share an id
	const n = 8
	ids := make([]int64, n)
	var ready, done sync.WaitGroup
	ready.Add(n)
	done.Add(1)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ids[i] = goroutineID()
			ready.Done()
			done.Wait()
		})
	}
	ready.Wait()
	seen := map[int64]bool{self: true}
	for i, id := range ids {
		if seen[id] {
			t.Errorf("goroutine %d: id %#x already taken", i, id)
		}
		seen[id] = true
	}
	done.Done()
	wg.Wait()
}
