package execution

import (
	"fmt"
	goruntime "runtime"
)

// GoroutineID reports the calling goroutine's id, parsed out of
// runtime.Stack. 0 means unparseable, which callers must treat as a
// foreign goroutine — the safe direction for re-entry checks.
func GoroutineID() int64 {
	var buf [48]byte
	n := goruntime.Stack(buf[:], false)
	var id int64
	if _, err := fmt.Sscanf(string(buf[:n]), "goroutine %d", &id); err != nil {
		return 0
	}
	return id
}
