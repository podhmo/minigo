package minigo_test

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// Concurrency tests for the real goroutine model: channels block, select
// parks, and goroutines genuinely interleave — verified inside a
// synctest bubble so blocking and timing are deterministic (the bubble
// detects a script that deadlocks and fails the test instead of hanging,
// and its fake clock resolves time.Sleep/time.After instantly).

func TestConcurrencyBlocking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		cases := []struct {
			fn   string
			want any
		}{
			{"UnbufferedSendRecv", int64(42)},
			{"BufferedSendNonblocking", int64(2)},
			{"BufferedCap", int64(3)},
			{"FanIn", int64(45)},
			{"RangeUntilClose", int64(15)},
			{"SelectBlocked", int64(7)},
			{"SelectSendBlocked", int64(33)},
			{"SelectDefaultEmpty", int64(9)},
			{"SelectDefaultOnSend", int64(8)},
			{"WaitGroupFanout", int64(4)},
			{"MutexCounter", int64(8)},
			{"TimeAfterSelect", int64(5)},
			{"SleepSelect", int64(3)},
			{"GoroutineCountRises", int64(1)},
			{"LazyInitFromGoroutine", int64(1)},
			{"RecvClosedZero", int64(9)},
			{"DeferRunsInGoroutine", int64(6)},
			{"OnceDo", int64(1)},
			{"ChanPointerIdentity", int64(9)},
			{"ChanSliceSend", int64(2)},
			{"ChanMapSend", int64(8)},
			{"SelectEmptyDefault", int64(7)},
			{"DurationArithmetic", int64(1)},
			{"LoopVarPerIteration", int64(3)},
			{"SortInGoroutine", int64(1)},
			{"SyncMapBasic", int64(3)},
			{"SyncMapOps", int64(0)},
			{"SyncMapConcurrent", int64(140)},
			{"SyncPoolNew", int64(1)},
			{"SyncPoolDecl", int64(7)},
			{"SyncPoolConcurrent", int64(1)},
			{"SyncCondSignal", int64(1)},
			{"SyncCondBroadcast", int64(3)},
			{"SyncScriptLocker", int64(1)},
			{"SyncRWMutex", int64(4)},
			{"SyncLockerDecl", int64(1)},
			{"SyncTryLock", int64(1)},
			{"SyncOnceFunc", int64(1)},
			{"SyncOnceValue", int64(1)},
			{"SyncOnceValues", int64(14)},
			{"SyncWaitGroupGo", int64(4)},
			{"SyncMethodMutex", int64(5)},
			{"SyncEmbedMutex", int64(5)},
			{"SyncEmbedPoolField", int64(9)},
			{"SyncAssertHostPtr", int64(1)},
		}
		for _, c := range cases {
			got := run(t, e, "./testdata/concurrency", c.fn)
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
			}
		}
	})
}

// TestSelectRandomPick: two ready cases — repeated runs must eventually
// pick each side, proving a real random choice rather than source order.
func TestSelectRandomPick(t *testing.T) {
	seen := map[int64]int{}
	for i := 0; i < 50 && len(seen) < 2; i++ {
		e := newEngine(t)
		got := run(t, e, "./testdata/concurrency", "SelectTwoReady")
		n, ok := got.(int64)
		if !ok {
			t.Fatalf("SelectTwoReady returned %T", got)
		}
		seen[n]++
	}
	if len(seen) < 2 {
		t.Fatalf("select never picked the second ready case: %v", seen)
	}
}

// TestGoroutinePanic: a panic in a spawned goroutine fails the whole
// process — Run reports it like Go's crash.
func TestGoroutinePanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "PanicInGoroutine")
		if err == nil || !strings.Contains(err.Error(), "child panic") {
			t.Fatalf("expected goroutine panic to fail the run, got %v", err)
		}
	})
}

// TestBlockedSiblingReleased: a panic in one goroutine releases siblings
// parked forever (empty select, nil-channel send) — the run resolves to
// the panic rather than hanging.
func TestBlockedSiblingReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		for _, fn := range []string{"EmptySelectThenPanic", "NilChanBlocksThenPanic"} {
			_, err := runErr(e, "./testdata/concurrency", fn)
			if err == nil || !strings.Contains(err.Error(), "fail") {
				t.Fatalf("%s: expected sibling panic, got %v", fn, err)
			}
		}
	})
}

// TestRangeChanTwoVars: `for k, v := range ch` traps — Go rejects it at
// compile time; minigo reports it when the iteration starts.
func TestRangeChanTwoVars(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "RangeChanTwoVars")
		if err == nil || !strings.Contains(err.Error(), "at most one iteration variable") {
			t.Fatalf("expected range-over-channel arity trap, got %v", err)
		}
	})
}

// TestDetachedLeak: a goroutine still parked when main returns dies with
// the process — the synctest bubble proves it is gone (it would deadlock
// the test otherwise).
func TestDetachedLeak(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		got := run(t, e, "./testdata/concurrency", "DetachedLeak")
		if diff := cmp.Diff(int64(1), got); diff != "" {
			t.Errorf("DetachedLeak mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestHostParkLeak documents a known limitation: a script goroutine parked
// inside a host call (WaitGroup.Wait, Mutex.Lock, time.Sleep) outlives its
// process — only select-based blocking watches proc.done, so the host
// goroutine leaks. A synctest bubble reports it as "blocked goroutines
// remain", so this assertion runs on the real clock.
func TestHostParkLeak(t *testing.T) {
	before := goruntime.NumGoroutine()
	e := newEngine(t)
	run(t, e, "./testdata/concurrency", "DetachedWait")
	deadline := time.Now().Add(2 * time.Second)
	for goruntime.NumGoroutine() <= before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if goruntime.NumGoroutine() <= before {
		t.Fatal("expected the Wait-parked goroutine to leak")
	}
}

// runErr is run() for tests that expect an error.
func runErr(e *minigo.Engine, dir, fn string, args ...runtime.Value) (runtime.Value, error) {
	return e.Run(context.Background(), dir, fn, args...)
}
