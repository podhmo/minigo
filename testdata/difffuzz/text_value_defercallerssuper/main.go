package main

// runtime.Callers during the unwind of a panic that superseded another:
// the frames the NEW panic already unwound list before the old panic's
// leftovers — Go's per-panic traceback order (newest gopanic first).
// A flat append-ordered list buries f.func2 after g/f.
import (
	"fmt"
	"runtime"
)

func g() { panic("P") }

func f() {
	defer func() {
		callers := make([]uintptr, 64)
		n := runtime.Callers(0, callers)
		frames := runtime.CallersFrames(callers[:n])
		var names []string
		for {
			fr, next := frames.Next()
			names = append(names, fr.Function)
			if !next {
				break
			}
		}
		idx := func(short, qualified string) int {
			for i, nm := range names {
				if nm == short || nm == qualified {
					return i
				}
			}
			return -1
		}
		iFunc2 := idx("f.func2", "main.f.func2")
		iG := idx("g", "main.g")
		iF := idx("f", "main.f")
		iMain := idx("main", "main.main")
		fmt.Println(iFunc2 >= 0 && iG >= 0 && iF >= 0 && iMain >= 0 &&
			iFunc2 < iG && iG < iF && iF < iMain)
	}()
	defer func() { panic("E2") }()
	g()
}

func main() {
	defer func() { recover() }()
	f()
}
