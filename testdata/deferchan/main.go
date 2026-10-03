package main

// DeferOrder runs defers LIFO on a named result: r becomes 321.
func DeferOrder() (r int) {
	defer func() { r = r*10 + 1 }()
	defer func() { r = r*10 + 2 }()
	defer func() { r = r*10 + 3 }()
	return 0
}

// DeferArgCaptured evaluates defer arguments at defer time, not call time:
// i is 1 when deferred, 99 when it fires — the captured 1 wins.
func DeferArgCaptured() (r int) {
	set := func(v int) { r = v }
	i := 1
	defer set(i)
	i = 99
	return 0
}

// NamedResultDefer mutates a named result in a deferred call.
func NamedResultDefer() (r int) {
	defer func() { r += 10 }()
	return 5
}

// Recovered recovers a panic inside a deferred function.
func Recovered() int {
	defer func() {
		if r := recover(); r != nil {
			// recovered: marker var proves we got here via RecoverAt
		}
	}()
	return 7
}

// RecoverValue captures the panic value via defer+recover.
func RecoverValue() (v any) {
	defer func() { v = recover() }()
	panic("boom")
}

// RecoverOutsideDefer returns nil (recover outside a deferred call).
func RecoverOutsideDefer() int {
	recover()
	return 1
}

// StillPanic re-panics from a defer: caller sees the panic.
func StillPanic() {
	defer func() { recover() }()
	panic("original")
}

// ReraiseReplace panics inside the deferred call itself.
func ReraiseReplace() {
	defer func() { panic("second") }()
	panic("first")
}

// ChanQueue: send then receive, FIFO.
func ChanQueue() int {
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3
	return <-ch + <-ch*10 + <-ch*100
}

// ChanCommaOk: receive with ok flag on a non-empty channel.
func ChanCommaOk() int {
	ch := make(chan int, 1)
	ch <- 7
	v, ok := <-ch
	if ok && v == 7 {
		return 1
	}
	return 0
}

// ChanClosedRecv: receive on closed empty channel yields zero+false.
func ChanClosedRecv() int {
	ch := make(chan int)
	close(ch)
	v, ok := <-ch
	_ = v
	if ok {
		return 1
	}
	return 42
}

// ChanRange drains a buffered channel until close.
func ChanRange() int {
	ch := make(chan int, 3)
	ch <- 10
	ch <- 20
	ch <- 30
	close(ch)
	sum := 0
	for v := range ch {
		sum += v
	}
	return sum
}

// GoSync spawns two goroutines and collects their results over a
// channel: the order is free but the sum is deterministic.
func GoSync() int {
	done := make(chan int, 2)
	add := func(n int) { done <- n }
	go add(5)
	go add(7)
	return <-done + <-done
}

// GoChanRoundtrip runs the classic goroutine+channel pattern for real:
// the unbuffered send hands off directly to the blocked receiver.
func GoChanRoundtrip() int {
	ch := make(chan int)
	go func() { ch <- 42 }()
	return <-ch
}

// SelectRecv receives through a select on a buffered channel.
func SelectRecv() int {
	ch := make(chan int, 1)
	ch <- 5
	r := 0
	select {
	case v := <-ch:
		r = v
	}
	return r
}

// SelectDefault falls to default when no case is ready.
func SelectDefault() int {
	ch := make(chan int)
	r := 0
	select {
	case v := <-ch:
		r = v
	default:
		r = 9
	}
	return r
}

// SelectCommaOk binds (v, ok) from a closed channel.
func SelectCommaOk() int {
	ch := make(chan int)
	close(ch)
	select {
	case _, ok := <-ch:
		if !ok {
			return 3
		}
	}
	return 0
}

// SelectSend: a send case is ready on a buffered channel with room.
func SelectSend() int {
	ch := make(chan int, 1)
	select {
	case ch <- 11:
	default:
		return 0
	}
	return <-ch
}

// DeferBuiltinClose: a deferred builtin (close) runs at teardown —
// deferred callees are not limited to compiled functions.
func DeferBuiltinClose() int {
	ch := make(chan int, 1)
	defer close(ch)
	ch <- 7
	return len(ch) + 1 // closed but still queued: len 1 -> 2
}

// DeferBuiltinRecover: a `defer recover()` inside a deferred function
// runs at that function's epilogue, one frame from gopanic — so it
// catches the panic that invoked it.
func DeferBuiltinRecover() (r int) {
	defer func() {
		defer recover()
	}()
	r = 3
	panic("swallowed")
}

// DeferBuiltinRecoverPanic: `defer recover()` itself cannot recover —
// zero non-wrapper frames sit between gorecover and gopanic, so the
// panic propagates like Go.
func DeferBuiltinRecoverPanic() int {
	defer recover()
	panic("swallowed")
}

// SelectConsumeBare: `case <-ch` consumes the queued value like any
// other receive — a non-binding receive must not leave it queued.
func SelectConsumeBare() int {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	select {
	case <-ch:
	}
	return <-ch // the select consumed 1 -> 2
}

// SelectEvalOrder: every case's channel operand is evaluated once on
// entry, in source order — even operands of cases that do not win.
func SelectEvalOrder() int {
	seen := 0
	mkch := func(n int) chan int {
		seen += n
		c := make(chan int, 1)
		c <- n
		return c
	}
	select {
	case <-mkch(1):
	case <-mkch(10):
	}
	return seen // 11
}

// SelectSendEvalOrder: a send case's channel and value operands are also
// evaluated on entry, before the winning case runs.
func SelectSendEvalOrder() int {
	seen := 0
	val := func() int { seen = 7; return 1 }
	sch := make(chan int)
	rch := make(chan int, 1)
	rch <- 9
	select {
	case <-rch:
	case sch <- val():
	}
	_ = sch
	return seen // 7
}

func main() {}
