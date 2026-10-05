package minigo_test

import (
	"context"
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
			{"AfterFuncFires", int64(7)},
			{"AfterFuncStop", int64(9)},
			{"AfterFuncNilStop", int64(1)},
			{"ShallowHostWins", int64(4)},
			{"NamedHostFieldEmbed", int64(7)},
			{"NamedScriptFieldEmbed", int64(9)},
			{"HostSubEmbedField", int64(1)},
			{"HostSubEmbedDepth", int64(7)},
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

// TestWaitUnblockThenPanic: a panic kills the process while a sibling is
// released mid-unwind — the dying goroutine's defer runs wg.Done, so the
// sibling resumes between Done and the process exit. Go's crash gives
// the sibling no post-exit progress: its next call dies with the
// process, and the package flag it would have set stays 0.
func TestWaitUnblockThenPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "WaitUnblockThenPanic")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("expected goroutine panic to fail the run, got %v", err)
		}
		got := run(t, e, "./testdata/concurrency", "WaitUnblockRead")
		if diff := cmp.Diff(int64(0), got); diff != "" {
			t.Errorf("WaitUnblockRead mismatch (-want +got):\n%s", diff)
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

// TestAmbiguousSelector: a member promoted through two embedded paths
// at the same depth traps like Go's compile-time rejection — across
// host embeds, and across script+host embeds, for fields and methods.
func TestAmbiguousSelector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		for _, fn := range []string{"AmbigHostMethod", "AmbigHostField", "AmbigMixedField", "AmbigMixedMethodField", "AmbigMixedMethodMethod", "AmbigScriptMethod"} {
			_, err := runErr(e, "./testdata/concurrency", fn)
			if err == nil || !strings.Contains(err.Error(), "ambiguous selector") {
				t.Fatalf("%s: expected ambiguous-selector trap, got %v", fn, err)
			}
		}
	})
}

// TestAfterFuncPanic: a panic inside an AfterFunc callback reaches Run
// through the goroutine-failure path — the timer's host goroutine must
// not die with an unrecovered panic.
func TestAfterFuncPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "AfterFuncPanic")
		if err == nil || !strings.Contains(err.Error(), "timer boom") {
			t.Fatalf("expected timer-callback panic to fail the run, got %v", err)
		}
	})
}

// TestAfterFuncNilFire: a nil callback that does fire fails the
// process with a nil-call panic through the goroutine-failure path —
// like a panic inside any other spawned goroutine.
func TestAfterFuncNilFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "AfterFuncNilFire")
		if err == nil || !strings.Contains(err.Error(), "nil pointer") {
			t.Fatalf("expected nil-call panic to fail the run, got %v", err)
		}
	})
}

// TestDefinedTypeMethodSet: a defined type (`type B A`) carries the
// underlying's fields but not its methods — Go rejects the selector at
// compile time, minigo traps on the access.
func TestDefinedTypeMethodSet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		for _, fn := range []string{"NamedHostMethodEmbed", "NamedScriptMethodEmbed"} {
			_, err := runErr(e, "./testdata/concurrency", fn)
			if err == nil || !strings.Contains(err.Error(), "has no field or method") {
				t.Fatalf("%s: expected no-member trap, got %v", fn, err)
			}
		}
	})
}

// TestAfterFuncDiesWithProc: a timer registered by a finished run never
// fires — Go kills pending timers with the process. Real clock: the
// timer must actually outlive the run.
func TestAfterFuncDiesWithProc(t *testing.T) {
	e := newEngine(t)
	run(t, e, "./testdata/concurrency", "AfterFuncArm")
	time.Sleep(100 * time.Millisecond)
	got := run(t, e, "./testdata/concurrency", "AfterFuncRead")
	if diff := cmp.Diff(int64(0), got); diff != "" {
		t.Errorf("AfterFuncRead mismatch (-want +got):\n%s", diff)
	}
}

type nilHostT struct{ N int }

type selfHostT struct{ *selfHostT }

func (*selfHostT) M() {}

func (*nilHostT) M() int { return 7 } // nil-tolerant pointer receiver
func (nilHostT) V() int  { return 9 } // value receiver — dereferences

// TestNilHostPtrMember: through a nil embedded host pointer, only the
// accesses that must dereference panic — fields and value-receiver
// methods. Pointer-receiver methods take the nil receiver like Go.
func TestNilHostPtrMember(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		e.Bind("probehost", map[string]runtime.Value{
			"T":     &runtime.TypeDef{Name: "probehost.T", Kind: runtime.KindStruct, HostNew: func() any { return &nilHostT{} }},
			"Nil":   &runtime.GoValue{V: (*nilHostT)(nil)},
			"SelfT": &runtime.TypeDef{Name: "probehost.SelfT", Kind: runtime.KindStruct, HostNew: func() any { return &selfHostT{} }},
		})
		got := run(t, e, "./testdata/nilhost", "NilMethod")
		if diff := cmp.Diff(int64(7), got); diff != "" {
			t.Errorf("NilMethod mismatch (-want +got):\n%s", diff)
		}
		for _, fn := range []string{"NilValueMethod", "NilField"} {
			_, err := runErr(e, "./testdata/nilhost", fn)
			if err == nil || !strings.Contains(err.Error(), "nil pointer") {
				t.Fatalf("%s: expected nil-pointer panic, got %v", fn, err)
			}
		}
		// a recursively-embedded host type (struct{ *T }) must not
		// loop the internal method-depth walk.
		if got := run(t, e, "./testdata/nilhost", "SelfEmbedMethod"); got != int64(7) {
			t.Errorf("SelfEmbedMethod = %v", got)
		}
	})
}

// TestNilHostPtrEmbed: a member reachable only through a nil embedded
// host pointer panics on the implicit dereference, like Go.
func TestNilHostPtrEmbed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngine(t)
		_, err := runErr(e, "./testdata/concurrency", "NilHostPtrEmbed")
		if err == nil || !strings.Contains(err.Error(), "nil pointer") {
			t.Fatalf("expected nil-pointer panic, got %v", err)
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
// inside a host call (WaitGroup.Wait, Mutex.Lock, a blocked builtin)
// outlives its process — proc death is checked at the call boundary and
// on channel parking, not inside a running host call, so the host
// goroutine leaks. A synctest bubble reports it as "blocked goroutines
// remain", so the check runs on the real clock. The bound builtin both
// reports that the goroutine entered it and blocks inside it forever:
// main's IsParked spin exits only after the goroutine is provably parked
// in the host call, so no goroutine counting or timing is involved.
func TestHostParkLeak(t *testing.T) {
	parked := make(chan struct{})
	never := make(chan struct{})
	e := newEngine(t)
	e.Bind("parkprobe", map[string]runtime.Value{
		"Parked": &runtime.BuiltinFunc{
			Name: "parkprobe.Parked",
			// runs on the spawned goroutine while the process is still
			// alive (main spins on IsParked), then blocks inside the
			// host call — past every proc-death check, so it stays
			// parked when the run ends: that is the leak.
			Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				close(parked)
				<-never
				return nil, nil
			},
		},
		"IsParked": &runtime.BuiltinFunc{
			Name: "parkprobe.IsParked",
			Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				select {
				case <-parked:
					return int64(1), nil
				default:
					return int64(0), nil
				}
			},
		},
	})
	run(t, e, "./testdata/hostpark", "DetachedWait")
	select {
	case <-parked:
	case <-time.After(30 * time.Second): // anti-hang bound, not a timing check
		t.Fatal("spawned goroutine never reached the host park call")
	}
}

// runErr is run() for tests that expect an error.
func runErr(e *minigo.Engine, dir, fn string, args ...runtime.Value) (runtime.Value, error) {
	return e.Run(context.Background(), dir, fn, args...)
}
