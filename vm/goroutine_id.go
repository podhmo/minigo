//go:build !arm64 && !amd64

package vm

import (
	"bytes"
	goruntime "runtime"
	"strconv"
)

// goroutineID reports the calling goroutine's id, parsed out of
// runtime.Stack — the portable fallback of goroutine_id_asm.go. Its
// traceback walks the whole (deep) interpreter stack, tens of µs per
// call. 0 means unparseable, which Call treats as foreign when the VM
// is busy — the safe direction.
func goroutineID() int64 {
	var buf [48]byte
	n := goruntime.Stack(buf[:], false)
	s := buf[:n]
	// "goroutine 123 [running]:" — the id sits between the first two
	// spaces of the header line.
	i := bytes.IndexByte(s, ' ')
	if i < 0 {
		return 0
	}
	j := bytes.IndexByte(s[i+1:], ' ')
	if j < 0 {
		return 0
	}
	id, err := strconv.ParseInt(string(s[i+1:i+1+j]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
