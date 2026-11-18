package execution

import (
	"errors"
	"testing"
)

// TestInitRunOnce pins the at-most-once contract, including that a
// failed run is not retried.
func TestInitRunOnce(t *testing.T) {
	var i Init
	calls := 0
	errBoom := errors.New("boom")
	failures := 0
	err := i.Run(func() error {
		calls++
		return errBoom
	}, func(error) { failures++ })
	if err != errBoom || calls != 1 || failures != 1 {
		t.Fatalf("run: err=%v calls=%d failures=%d", err, calls, failures)
	}
	if err := i.Run(func() error {
		calls++
		return nil
	}, nil); err != errBoom || calls != 1 {
		t.Fatalf("second run: err=%v calls=%d", err, calls)
	}
}

// TestInitRunPanicRecordsAndRepanicks covers the cold-load failure the
// once semantics would otherwise hide: the panic is cached as the
// error, the failure callback fires, and the panic propagates.
func TestInitRunPanicRecordsAndRepanicks(t *testing.T) {
	var i Init
	var failed error
	defer func() {
		if v := recover(); v != "kaboom" {
			t.Fatalf("recover = %v", v)
		}
		if failed == nil {
			t.Fatal("panic did not reach the failure callback")
		}
	}()
	i.Run(func() error { panic("kaboom") }, func(err error) { failed = err })
}

// TestInitTrySwallowsPanic records the panic as the error without
// propagating — the const-only initializer's fallback contract.
func TestInitTrySwallowsPanic(t *testing.T) {
	var i Init
	err := i.Try(func() error { panic("nope") })
	if err == nil || err.Error() != "panic: nope" {
		t.Fatalf("err = %v", err)
	}
	calls := 0
	if err := i.Try(func() error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("try re-ran: err=%v calls=%d", err, calls)
	}
}
