package main

import "parkprobe"

// DetachedWait: a goroutine parked inside a host call is not released
// when its process dies — only select/channel blocking watches
// proc.done, so the host goroutine outlives the run that spawned it.
// parkprobe is bound by TestHostParkLeak: its Wait reports the goroutine
// reached a host call, then parks it on a real sync.WaitGroup.
func DetachedWait() int {
	go func() {
		parkprobe.Wait()
	}()
	return 1
}
