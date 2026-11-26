package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLifecycleTransitionTable(t *testing.T) {
	assertPanics := func(t *testing.T, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("expected invalid transition panic")
			}
		}()
		fn()
	}

	p := &Package{}
	p.advanceState(Indexed)
	p.advanceState(Initializing)
	p.advanceState(Ready)
	if diff := cmp.Diff(Ready, p.State()); diff != "" {
		t.Fatalf("state mismatch (-want +got):\n%s", diff)
	}
	assertPanics(t, func() { p.advanceState(Failed) })

	loadFailed := &Package{}
	loadFailed.advanceState(Failed)
	loadFailed.advanceState(Initializing)
	if diff := cmp.Diff(Initializing, loadFailed.State()); diff != "" {
		t.Fatalf("load-failed state mismatch (-want +got):\n%s", diff)
	}
}

func TestLifecycleInitFailureIsAbsorbing(t *testing.T) {
	errBoom := errors.New("boom")
	p := &Package{
		Name: "p",
		PackageInstance: PackageInstance{
			Globals: NewEnv(),
		},
	}
	p.advanceState(Indexed)
	p.Bootstrap = func(*Package, func(*Function) error) error { return errBoom }
	p.RunInit = func(*Function) error { return nil }
	if err := p.EnsureReady(); !errors.Is(err, errBoom) {
		t.Fatalf("EnsureReady error = %v, want %v", err, errBoom)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("init-time Failed accepted a new initialization")
		}
	}()
	p.advanceState(Initializing)
}

func TestMemberInitCycleOnSameGoroutineFails(t *testing.T) {
	p := &Package{
		Name: "p",
		PackageInstance: PackageInstance{
			Globals: NewEnv(),
		},
	}
	p.advanceState(Indexed)
	p.Bootstrap = func(p *Package, run func(*Function) error) error {
		_, err := p.Member("X", nil)
		return err
	}
	p.RunInit = func(*Function) error { return nil }

	err := p.EnsureReady()
	if err == nil || !strings.Contains(err.Error(), "initialization cycle: p.X is not bound yet") {
		t.Fatalf("same-goroutine cyclic read = %v, want initialization cycle", err)
	}
	if diff := cmp.Diff(Failed, p.State()); diff != "" {
		t.Fatalf("state mismatch (-want +got):\n%s", diff)
	}
}

func TestMemberConcurrentInitWaitsForOwner(t *testing.T) {
	p := &Package{
		Name: "p",
		PackageInstance: PackageInstance{
			Globals: NewEnv(),
		},
	}
	p.advanceState(Indexed)
	started := make(chan struct{})
	release := make(chan struct{})
	p.Bootstrap = func(p *Package, run func(*Function) error) error {
		close(started)
		<-release
		p.Globals.Set("X", int64(7))
		return nil
	}
	p.RunInit = func(*Function) error { return nil }

	initDone := make(chan error, 1)
	go func() { initDone <- p.EnsureReady() }()
	<-started

	type memberResult struct {
		v   Value
		err error
	}
	memberDone := make(chan memberResult, 1)
	go func() {
		v, err := p.Member("X", nil)
		memberDone <- memberResult{v: v, err: err}
	}()

	select {
	case res := <-memberDone:
		t.Fatalf("concurrent Member returned before init completed: %v", res.err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	res := <-memberDone
	if res.err != nil {
		t.Fatalf("concurrent Member: %v", res.err)
	}
	if diff := cmp.Diff(int64(7), res.v); diff != "" {
		t.Fatalf("Member(X) mismatch (-want +got):\n%s", diff)
	}
}
