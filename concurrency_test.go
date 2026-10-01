package minigo_test

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"

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

// runErr is run() for tests that expect an error.
func runErr(e *minigo.Engine, dir, fn string, args ...runtime.Value) (runtime.Value, error) {
	return e.Run(context.Background(), dir, fn, args...)
}
