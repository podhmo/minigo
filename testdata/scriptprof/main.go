package main

import "time"

type T struct{ n int }

// spin busy-loops for d: the profile must charge it to spin's loop.
func (t *T) spin(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		t.n++
	}
}

// parked blocks on ch the whole run: parked time must not be sampled.
func parked(ch chan int, done chan bool) {
	<-ch
	done <- true
}

func Main() int {
	ch, done := make(chan int), make(chan bool)
	go parked(ch, done)
	t := &T{}
	t.spin(300 * time.Millisecond)
	close(ch)
	<-done
	return 1
}

// Closure runs a closure long enough to be sampled; it is named like Go's
// `main.Closure.func1`.
func Closure() int {
	n := 0
	f := func() {
		end := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(end) {
			n++
		}
	}
	f()
	return 1
}

// methodClosure runs a closure inside a pointer-receiver method; it is
// named like Go's `main.(*T).methodClosure.func1`.
func (t *T) methodClosure() int {
	n := 0
	f := func() {
		end := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(end) {
			n++
		}
	}
	f()
	return 1
}

func MethodClosure() int { return (&T{}).methodClosure() }

// slowDefer's deferred host call must be charged to slowDefer. The
// sleep is long enough that the sampler keeps well over the test's
// 100ms attribution floor even when a CI box under load drops ticks.
func slowDefer() {
	defer time.Sleep(400 * time.Millisecond)
}

// recvLoop receives n times on a closed channel: every receive parks,
// but none blocks, so the whole loop is run time.
func recvLoop(n int) {
	ch := make(chan int)
	close(ch)
	for range n {
		<-ch
	}
}

// Ops returns recvLoop's wall time in ms.
func Ops() int {
	slowDefer()
	start := time.Now()
	recvLoop(300000)
	return int(time.Since(start).Milliseconds())
}
