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
