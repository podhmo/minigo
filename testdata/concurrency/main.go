package main

import (
	"runtime"
	"sort"
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

// OnceDo: sync.Once runs its func exactly once — the method takes a
// func-typed argument adapted to a host func.
func OnceDo() int {
	var o sync.Once
	n := 0
	f := func() { n++ }
	o.Do(f)
	o.Do(f)
	return n // 1
}

// ChanPointerIdentity: sending a pointer through a script channel keeps
// pointer identity — *p writes back into the sender's variable.
func ChanPointerIdentity() int {
	ch := make(chan *int, 1)
	n := 1
	ch <- &n
	p := <-ch
	*p = 9
	return n // 9
}

// ChanSliceSend / ChanMapSend: containers cross script channels verbatim.
func ChanSliceSend() int {
	ch := make(chan []int, 1)
	ch <- []int{1, 2, 3}
	return (<-ch)[1] // 2
}

func ChanMapSend() int {
	ch := make(chan map[string]int, 1)
	m := map[string]int{"k": 7}
	ch <- m
	got := <-ch
	got["k"] = 8
	return m["k"] // 8 (same underlying map)
}

// SelectEmptyDefault: a `default:` clause with an empty body still
// registers the default — the select must not block.
func SelectEmptyDefault() int {
	ch := make(chan int)
	select {
	case <-ch:
		return -1
	default:
	}
	return 7 // 7
}

// DurationArithmetic: bound time.* constants behave as their int64
// underlying — arithmetic and comparisons work.
func DurationArithmetic() int {
	if 2*time.Second != 2000000000 {
		return -1
	}
	if !(time.Hour > time.Minute) {
		return -2
	}
	if time.Millisecond+time.Second != 1001000000 {
		return -3
	}
	return 1 // 1
}

// LoopVarPerIteration: a 3-clause for's iteration variable is fresh each
// iteration (Go 1.22) — closures spawned inside capture distinct cells.
func LoopVarPerIteration() int {
	ch := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func() { ch <- i }()
	}
	return <-ch + <-ch + <-ch // 0+1+2 = 3
}

// RangeChanTwoVars: `for k, v := range ch` is a compile error in Go —
// minigo traps it at iteration time.
func RangeChanTwoVars() int {
	ch := make(chan int, 1)
	ch <- 1
	for _, v := range ch {
		return v
	}
	return 0
}

// SortInGoroutine: host callbacks like sort.Slice's less run on the
// calling VM — inside a goroutine they must not touch the root VM.
func SortInGoroutine() int {
	ch := make(chan int, 1)
	go func() {
		s := []int{3, 1, 2}
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		ch <- s[0]
	}()
	return <-ch // 1
}

// DetachedWait: a goroutine parked inside a host call (WaitGroup.Wait —
// no select, no done arm) is not released when the process dies — this
// leaks the host goroutine (unlike channel parking, which procExit frees).
func DetachedWait() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var inner sync.WaitGroup
		inner.Add(1)
		inner.Wait() // parks inside a host call — survives proc kill
	}()
	time.Sleep(1 * time.Millisecond) // let the goroutine reach Wait
	return 1
}

// ---- sync package surface ----

// SyncMapBasic: Store/Load/Range carry script values through the host
// sync.Map untouched.
func SyncMapBasic() int {
	var m sync.Map
	m.Store("a", 1)
	m.Store("b", 2)
	if v, ok := m.Load("a"); !ok || v.(int) != 1 {
		return -1
	}
	sum := 0
	m.Range(func(k, v any) bool {
		sum += v.(int)
		return true
	})
	return sum // 3
}

// SyncMapOps: LoadOrStore / Swap / CompareAndSwap / LoadAndDelete /
// CompareAndDelete / Clear behave like the host methods.
func SyncMapOps() int {
	var m sync.Map
	if v, loaded := m.LoadOrStore("k", 1); loaded || v.(int) != 1 {
		return -1
	}
	if v, loaded := m.LoadOrStore("k", 2); !loaded || v.(int) != 1 {
		return -2
	}
	m.Store("k", 3)
	if v, _ := m.Swap("k", 4); v.(int) != 3 {
		return -3
	}
	if !m.CompareAndSwap("k", 4, 5) {
		return -4
	}
	if m.CompareAndSwap("k", 4, 9) {
		return -5
	}
	if v, ok := m.LoadAndDelete("k"); !ok || v.(int) != 5 {
		return -6
	}
	m.Store("x", 1)
	m.CompareAndDelete("x", 1)
	m.Store("y", 1)
	m.Clear()
	n := 0
	m.Range(func(k, v any) bool { n++; return true })
	return n // 0
}

// SyncMapConcurrent: stores from many goroutines land intact — the host
// map gives real atomicity, not a script-side simulation.
func SyncMapConcurrent() int {
	var m sync.Map
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.Store(i, i*i)
		}(i)
	}
	wg.Wait()
	sum := 0
	m.Range(func(k, v any) bool {
		sum += v.(int)
		return true
	})
	return sum // 140
}

// SyncPoolNew: `&sync.Pool{New: f}` initializes the exported New field
// of a host literal with a script func; Get on an empty pool invokes
// it. Put/Get identity is deliberately not asserted — sync.Pool makes
// no retention guarantee (P migration, GC).
func SyncPoolNew() int {
	n := 0
	p := &sync.Pool{New: func() any {
		n++
		x := n
		return &x
	}}
	a := p.Get().(*int)
	b := p.Get().(*int)
	if a == b || *a != 1 || *b != 2 || n != 2 {
		return -1
	}
	return 1
}

// SyncPoolDecl: `var p sync.Pool` gives a usable zero Pool; assigning
// the exported New field writes through reflection onto the host struct
// and the stored func then calls back through the VM.
func SyncPoolDecl() int {
	var p sync.Pool
	p.New = func() any { return 7 }
	return p.New().(int) // 7
}

// SyncCondSignal: NewCond(&mu) waits and wakes — a real notify, not a
// fake-clock resolution.
func SyncCondSignal() int {
	var mu sync.Mutex
	c := sync.NewCond(&mu)
	done := false
	go func() {
		mu.Lock()
		done = true
		c.Signal()
		mu.Unlock()
	}()
	mu.Lock()
	for !done {
		c.Wait()
	}
	mu.Unlock()
	return 1
}

// SyncCondBroadcast: Broadcast releases every waiter parked on the Cond.
func SyncCondBroadcast() int {
	var mu sync.Mutex
	c := sync.NewCond(&mu)
	waiting := 0
	fired := false
	released := 0
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			waiting++
			for !fired {
				c.Wait()
			}
			released++
			mu.Unlock()
		}()
	}
	for {
		mu.Lock()
		if waiting == 3 {
			fired = true
			c.Broadcast()
			mu.Unlock()
			break
		}
		mu.Unlock()
		runtime.Gosched()
	}
	wg.Wait()
	return released // 3
}

// SyncScriptLocker: NewCond accepts a script-defined Locker too — its
// Lock/Unlock methods dispatch back through the VM. The spy delegates
// mutual exclusion to a real Mutex (script fields are not atomic) and
// counts under a second one so the dispatch is observable race-free.
func SyncScriptLocker() int {
	l := &spyLocker{}
	c := sync.NewCond(l)
	done := false
	go func() {
		l.Lock()
		done = true
		c.Signal()
		l.Unlock()
	}()
	l.Lock()
	for !done {
		c.Wait()
	}
	l.Unlock()
	l.guard.Lock()
	n := l.locks
	m := l.unlocks
	l.guard.Unlock()
	if n == 0 || n != m {
		return -1
	}
	return 1
}

type spyLocker struct {
	mu      sync.Mutex
	guard   sync.Mutex
	locks   int
	unlocks int
}

func (l *spyLocker) Lock() {
	l.mu.Lock()
	l.guard.Lock()
	l.locks++
	l.guard.Unlock()
}

func (l *spyLocker) Unlock() {
	l.guard.Lock()
	l.unlocks++
	l.guard.Unlock()
	l.mu.Unlock()
}

// SyncRWMutex: readers and a writer serialize through the host RWMutex —
// the count is exact.
func SyncRWMutex() int {
	var rw sync.RWMutex
	n := 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			rw.RLock()
			_ = n
			rw.RUnlock()
		}()
		go func() {
			defer wg.Done()
			rw.Lock()
			n++
			rw.Unlock()
		}()
	}
	wg.Wait()
	return n // 4
}

// SyncLockerDecl: `var l sync.Locker` coerces a host Mutex through the
// interface method set (Lock/Unlock dispatch on the boxed value).
func SyncLockerDecl() int {
	var l sync.Locker = &sync.Mutex{}
	l.Lock()
	l.Unlock()
	return 1
}

// SyncTryLock: TryLock reports acquisition like the host method.
func SyncTryLock() int {
	var mu sync.Mutex
	if !mu.TryLock() {
		return -1
	}
	mu.Unlock()
	mu.Lock()
	if mu.TryLock() {
		return -2 // held lock is not tryable
	}
	mu.Unlock()
	return 1
}

// SyncOnceFunc: the returned func runs its inner func exactly once.
func SyncOnceFunc() int {
	calls := 0
	f := sync.OnceFunc(func() { calls++ })
	f()
	f()
	return calls // 1
}

// SyncOnceValue: OnceValue memoizes the first result.
func SyncOnceValue() int {
	calls := 0
	f := sync.OnceValue(func() int {
		calls++
		return calls * 10
	})
	a := f()
	b := f()
	if a != 10 || b != 10 || calls != 1 {
		return -1
	}
	return a / 10 // 1
}

// SyncOnceValues: OnceValues memoizes the pair.
func SyncOnceValues() int {
	calls := 0
	f := sync.OnceValues(func() (int, int) {
		calls++
		return 3, 4
	})
	a, b := f()
	c, d := f()
	if calls != 1 {
		return -1
	}
	return a + b + c + d // 14
}

// SyncPoolConcurrent: Pool.New is a script func invoked from whichever
// goroutine calls Get — foreign-goroutine calls reroute to spawned
// child VMs (the Call goroutine-id check), so this must not race.
func SyncPoolConcurrent() int {
	n := 0
	var mu sync.Mutex
	p := &sync.Pool{New: func() any {
		mu.Lock()
		n++
		x := n
		mu.Unlock()
		return &x
	}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Get().(*int)
		}()
	}
	wg.Wait()
	if n < 1 || n > 4 {
		return -1
	}
	return 1
}

// SyncWaitGroupGo: wg.Go (Go 1.25) runs the func on a new goroutine and
// counts the WaitGroup for you.
func SyncWaitGroupGo() int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			mu.Lock()
			n++
			mu.Unlock()
		})
	}
	wg.Wait()
	return n // 4
}

// SyncMethodMutex: a sync.Mutex embedded in a script struct serializes
// its methods like Go.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) Inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func SyncMethodMutex() int {
	c := &counter{}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	return c.n // 5
}

// SyncEmbedMutex: a host mutex embedded in a script struct promotes its
// methods — `struct{ sync.Mutex }` is Go's standard lockable struct.
type gated struct {
	sync.Mutex
	n int
}

func (g *gated) Inc() {
	g.Lock()
	g.n++
	g.Unlock()
}

func SyncEmbedMutex() int {
	g := &gated{}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Inc()
		}()
	}
	wg.Wait()
	return g.n // 5
}

// SyncEmbedPoolField: a promoted host field works too — g.New reads the
// embedded Pool's exported field through the same host-select path.
type poolBox struct {
	sync.Pool
}

func SyncEmbedPoolField() int {
	g := &poolBox{}
	g.New = func() any { return 9 }
	return g.New().(int) // 9
}

// SyncAssertHostPtr: c.L is a host pointer box — type-asserting it to
// *sync.Mutex matches by the boxed native type, not by Deref.
func SyncAssertHostPtr() int {
	var mu sync.Mutex
	c := sync.NewCond(&mu)
	if _, ok := c.L.(*sync.Mutex); !ok {
		return -1
	}
	return 1
}
