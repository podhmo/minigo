package execution

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGoroutineID(t *testing.T) {
	self := GoroutineID()
	if diff := cmp.Diff(int64(0), self); diff == "" {
		t.Fatal("GoroutineID() = 0 on the test goroutine")
	}
	if diff := cmp.Diff(self, GoroutineID()); diff != "" {
		t.Fatalf("same goroutine id mismatch (-want +got):\n%s", diff)
	}
	other := make(chan int64, 1)
	go func() { other <- GoroutineID() }()
	if diff := cmp.Diff(self, <-other); diff == "" {
		t.Fatalf("different goroutines reported the same id %d", self)
	}
}
