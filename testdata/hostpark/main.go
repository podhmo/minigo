package main

import (
	"parkprobe"
	"sync"
)

// DetachedWait: a goroutine parked inside a host call (WaitGroup.Wait —
// no select, no done arm) is not released when the process dies — this
// leaks the host goroutine (unlike channel parking, which procExit
// frees). parkprobe.Parked is bound by TestHostParkLeak: it reports
// that the goroutine is about to park, so the check needs no goroutine
// counting.
func DetachedWait() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var inner sync.WaitGroup
		inner.Add(1)
		parkprobe.Parked()
		inner.Wait() // parks inside a host call — survives proc kill
	}()
	return 1
}
