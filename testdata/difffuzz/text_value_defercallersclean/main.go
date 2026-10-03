package main

// After a deferred call recovers the panic, the consumed unwind's
// frames must not linger in runtime.Callers: the owner frame is
// re-pushed live and lists exactly once — not once live plus once as
// a stale unwound entry.
import (
	"fmt"
	"runtime"
)

func f() (ok bool) {
	defer func() {
		callers := make([]uintptr, 64)
		n := runtime.Callers(0, callers)
		frames := runtime.CallersFrames(callers[:n])
		count := 0
		for {
			fr, next := frames.Next()
			if fr.Function == "f" || fr.Function == "main.f" {
				count++
			}
			if !next {
				break
			}
		}
		ok = count == 1
	}()
	defer func() { recover() }()
	panic("P")
}

func main() {
	fmt.Println(f())
}
