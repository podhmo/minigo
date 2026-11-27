package mtesting

import (
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRunCleanupsDrainsLateRegistrationAndPanic(t *testing.T) {
	tr := NewT(&Suite{}, "TestX", "x_test.go:1")
	var order []string
	tr.AddCleanup(func() {
		order = append(order, "first-registered")
		panic("boom")
	})
	tr.AddCleanup(func() {
		order = append(order, "last-registered")
		tr.AddCleanup(func() { order = append(order, "late") })
	})

	err := runCleanups(nil, tr)
	if err == nil {
		t.Fatal("runCleanups returned nil after a panicking cleanup")
	}
	if diff := cmp.Diff([]string{"last-registered", "late", "first-registered"}, order); diff != "" {
		t.Errorf("cleanup order mismatch (-want +got):\n%s", diff)
	}
	if got := tr.CleanupCount(); got != 0 {
		t.Errorf("cleanupCount = %d, want 0", got)
	}
}

func TestCleanupRegistrationConcurrent(t *testing.T) {
	tr := NewT(&Suite{}, "TestX", "x_test.go:1")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				tr.AddCleanup(func() {})
			}
		}()
	}
	wg.Wait()
	if got := tr.CleanupCount(); got != 800 {
		t.Errorf("cleanupCount = %d, want 800", got)
	}
}

func TestContextCanceledBeforeCleanup(t *testing.T) {
	tr := NewT(&Suite{}, "TestX", "x_test.go:1")
	canceled := false
	tr.AddCleanup(func() {
		select {
		case <-tr.Context().Done():
			canceled = true
		default:
		}
	})
	tr.CancelContext()
	if err := runCleanups(nil, tr); err != nil {
		t.Fatalf("runCleanups: %v", err)
	}
	if !canceled {
		t.Error("test context was not canceled before cleanup")
	}
}

func TestStopOfUnwrapsSentinel(t *testing.T) {
	tr := NewT(&Suite{}, "TestX", "x_test.go:1")
	err := fmt.Errorf("outer: %w", &stopError{t: tr})
	if got, ok := StopOf(err); !ok || got != tr {
		t.Errorf("StopOf = (%v, %v), want (%v, true)", got, ok, tr)
	}
}
