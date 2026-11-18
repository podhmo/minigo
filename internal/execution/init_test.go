package execution

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
	if diff := cmp.Diff(err, errBoom, cmpopts.EquateErrors()); diff != "" {
		t.Fatalf("run err mismatch (-got +want):\n%s", diff)
	}
	if diff := cmp.Diff(calls, 1); diff != "" {
		t.Fatalf("calls mismatch (-got +want):\n%s", diff)
	}
	if diff := cmp.Diff(failures, 1); diff != "" {
		t.Fatalf("failures mismatch (-got +want):\n%s", diff)
	}
	err = i.Run(func() error {
		calls++
		return nil
	}, nil)
	if diff := cmp.Diff(err, errBoom, cmpopts.EquateErrors()); diff != "" {
		t.Fatalf("second run err mismatch (-got +want):\n%s", diff)
	}
	if diff := cmp.Diff(calls, 1); diff != "" {
		t.Fatalf("second-run calls mismatch (-got +want):\n%s", diff)
	}
}

// TestInitRunPanicRecordsAndRepanicks covers the cold-load failure the
// once semantics would otherwise hide: the panic is cached as the
// error, the failure callback fires, and the panic propagates.
func TestInitRunPanicRecordsAndRepanicks(t *testing.T) {
	var i Init
	var failed error
	defer func() {
		if diff := cmp.Diff(recover(), "kaboom"); diff != "" {
			t.Fatalf("recover mismatch (-got +want):\n%s", diff)
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
	if diff := cmp.Diff(err.Error(), "panic: nope"); diff != "" {
		t.Fatalf("err mismatch (-got +want):\n%s", diff)
	}
	calls := 0
	err = i.Try(func() error { calls++; return nil })
	if diff := cmp.Diff(err.Error(), "panic: nope"); diff != "" {
		t.Fatalf("second try err mismatch (-got +want):\n%s", diff)
	}
	if diff := cmp.Diff(calls, 0); diff != "" {
		t.Fatalf("try re-ran (-got +want):\n%s", diff)
	}
}
