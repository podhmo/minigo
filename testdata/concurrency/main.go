package main

import (
	"runtime"
	"sync"
	"time"
)

// UnbufferedSendRecv: an unbuffered send genuinely blocks until a
// receiver arrives — the goroutine hands off, the receiver proceeds.
func UnbufferedSendRecv() int {
	ch := make(chan int)
	go func() { ch <- 42 }()
	return <-ch
}

// BufferedSendNonblocking: a send into a buffered channel with room
// completes without a receiver.
func BufferedSendNonblocking() int {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	return len(ch) // 2
}

// BufferedCap: a buffered channel at capacity makes the next send wait;
// the receiver frees a slot and the send lands.
func BufferedCap() int {
	ch := make(chan int, 1)
	done := make(chan int)
	go func() {
		ch <- 1
		ch <- 2 // blocks until the first is received
		done <- 0
	}()
	v := <-ch
	<-done
	return v + <-ch // 1 + 2
}

// FanIn: many goroutines send into one channel; order is free but the
// sum is deterministic.
func FanIn() int {
	ch := make(chan int)
	for i := 0; i < 10; i++ {
		n := i
		go func() { ch <- n }()
	}
	sum := 0
	for i := 0; i < 10; i++ {
		sum += <-ch
	}
	return sum // 45
}

// RangeUntilClose: a producer goroutine fills a channel; range blocks
// per element and ends when the producer closes it.
func RangeUntilClose() int {
	ch := make(chan int)
	go func() {
		for i := 1; i <= 5; i++ {
			ch <- i
		}
		close(ch)
	}()
	sum := 0
	for v := range ch {
		sum += v
	}
	return sum // 15
}

// SelectBlocked: with no ready case, select parks — a later goroutine
// send unblocks it.
func SelectBlocked() int {
	ch := make(chan int)
	go func() { ch <- 7 }()
	r := 0
	select {
	case v := <-ch:
		r = v
	case v := <-make(chan int): // never ready: nil-ish dead end
		r = v
	}
	return r // 7
}

// SelectSendBlocked: a send case that cannot proceed blocks until the
// peer receives.
func SelectSendBlocked() int {
	ch := make(chan int)
	recv := make(chan int)
	go func() {
		v := <-ch
		recv <- v
	}()
	select {
	case ch <- 33:
	}
	return <-recv // 33
}

// SelectTwoReadyRandom: two ready cases — either may win (approximated
// here as "the select completes", the randomness exercised by
// TestSelectRandomPick looping it).
func SelectTwoReady() int {
	a := make(chan int, 1)
	b := make(chan int, 1)
	a <- 1
	b <- 2
	select {
	case v := <-a:
		return v
	case v := <-b:
		return v
	}
	return 0
}

// SelectDefaultEmpty: default fires when nothing is ready.
func SelectDefaultEmpty() int {
	ch := make(chan int)
	select {
	case <-ch:
		return 0
	default:
		return 9
	}
}

// SelectDefaultOnSend: a blocked send case also yields to default.
func SelectDefaultOnSend() int {
	ch := make(chan int)
	select {
	case ch <- 1:
		return 0
	default:
		return 8
	}
}

// EmptySelectBlocks: `select {}` with no cases blocks; a goroutine that
// fails the process unwinds it — see TestSelectEmptyProcExit for the
// error side. This helper parks and then gets killed by the panic.
func EmptySelectThenPanic() int {
	go func() { panic("fail") }()
	select {}
}

// WaitGroupFanout: a WaitGroup coordinates a fan-out; the counter ends
// at the worker count.
func WaitGroupFanout() int {
	var wg sync.WaitGroup
	ch := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch <- 1
		}()
	}
	wg.Wait()
	return <-ch + <-ch + <-ch + <-ch // 4
}

// MutexCounter: a Mutex serializes shared-counter increments across
// goroutines — the final count is exact, not racy.
func MutexCounter() int {
	var mu sync.Mutex
	count := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			count++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return count // 8
}

// TimeAfterSelect: time.After drives a timeout — under synctest the
// fake clock fires it instantly.
func TimeAfterSelect() int {
	select {
	case <-time.After(time.Hour):
		return 5
	}
}

// SleepSelect: the fake clock also resolves a goroutine sleeping on a
// channel-bound wakeup.
func SleepSelect() int {
	ch := make(chan int, 1)
	go func() {
		time.Sleep(time.Hour)
		ch <- 3
	}()
	return <-ch
}

// GoroutineCountRises: a spawned goroutine bumps the host goroutine
// count while it lives.
func GoroutineCountRises() int {
	gate := make(chan int)
	n0 := runtime.NumGoroutine()
	go func() { <-gate }()
	n1 := runtime.NumGoroutine()
	gate <- 0
	if n1 > n0 {
		return 1
	}
	return 0
}

// PanicInGoroutine: a panic inside a spawned goroutine fails the whole
// process — the caller's Run returns the panic, like Go's crash.
func PanicInGoroutine() int {
	go func() { panic("child panic") }()
	select {}
}

// NilChanBlocksForever: send on a nil channel parks — a sibling panic
// releases it (process exit), so the call still resolves to the panic.
func NilChanBlocksThenPanic() int {
	var ch chan int
	go func() { panic("fail") }()
	ch <- 1
	return 0
}

// LazyInitFromGoroutine: the first member touch inside a spawned
// goroutine runs package init on that goroutine's VM.
func LazyInitFromGoroutine() int {
	ch := make(chan int)
	go func() {
		// runtime.NumGoroutine forces member resolution inside the goroutine
		ch <- runtime.NumGoroutine()
	}()
	n := <-ch
	if n >= 1 {
		return 1
	}
	return 0
}

// RecvClosedZero: a receive on a closed channel yields the element
// type's zero value.
func RecvClosedZero() int {
	ch := make(chan int, 1)
	ch <- 9
	close(ch)
	a := <-ch
	b, ok := <-ch
	if ok {
		return -1
	}
	return a + b // 9 + 0
}

// DeferRunsInGoroutine: a deferred call inside a spawned goroutine runs
// as that goroutine returns — observed over a channel.
func DeferRunsInGoroutine() int {
	ch := make(chan int, 1)
	go func() {
		defer func() { ch <- 6 }()
	}()
	return <-ch
}

// DetachedLeak: a goroutine parked when the caller returns dies with the
// process — Run must not leak it (the synctest bubble proves it exits).
func DetachedLeak() int {
	go func() { <-make(chan int) }()
	return 1
}

func main() {}
