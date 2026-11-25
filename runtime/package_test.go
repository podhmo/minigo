package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestMemberInitCycleOnSameGoroutineFails(t *testing.T) {
	p := &Package{
		Name: "p",
		PackageInstance: PackageInstance{
			Globals: NewEnv(),
		},
	}
	p.SetState(Indexed)
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
	p.SetState(Indexed)
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
