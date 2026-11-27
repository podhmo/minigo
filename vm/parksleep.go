package vm

import (
	"time"

	"github.com/podhmo/minigo/runtime"
)

// ParkedSleep sleeps like time.Sleep while participating in process
// cancellation: process death unwinds the wait instead of leaving the
// caller blocked. Like a host sleep, the elapsed time remains profiled run time.
func ParkedSleep(vc runtime.CallerRef, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-procDoneOf(vc):
		panic(procExit{})
	}
}
