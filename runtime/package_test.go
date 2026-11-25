package runtime

import (
	"strings"
	"testing"
	"time"
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
	if p.State() != Failed {
		t.Fatalf("state = %v, want Failed", p.State())
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

	memberDone := make(chan error, 1)
	go func() {
		v, err := p.Member("X", nil)
		if err == nil && v != int64(7) {
			t.Errorf("Member(X) = %#v, want 7", v)
		}
		memberDone <- err
	}()

	select {
	case err := <-memberDone:
		t.Fatalf("concurrent Member returned before init completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-initDone; err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if err := <-memberDone; err != nil {
		t.Fatalf("concurrent Member: %v", err)
	}
}
