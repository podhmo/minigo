package main

import (
	"parkprobe"
)

// DetachedWait: a goroutine parked inside a host call is not released
// when the process dies — it leaks the host goroutine (unlike channel
// parking, which procExit frees). parkprobe.Parked is bound by
// TestHostParkLeak: it reports from inside the host call, then blocks
// there forever, so by the time main's IsParked spin exits the goroutine
// is provably parked inside the builtin — that is the leak.
func DetachedWait() int {
	go func() {
		parkprobe.Parked() // reports inside the host call, then parks there
	}()
	for !parkprobe.IsParked() {
	}
	return 1
}
