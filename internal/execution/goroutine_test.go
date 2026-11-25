package execution

import "testing"

func TestGoroutineID(t *testing.T) {
	self := GoroutineID()
	if self == 0 {
		t.Fatal("GoroutineID() = 0 on the test goroutine")
	}
	if got := GoroutineID(); got != self {
		t.Fatalf("same goroutine id = %d, want %d", got, self)
	}
	other := make(chan int64, 1)
	go func() { other <- GoroutineID() }()
	if got := <-other; got == self {
		t.Fatalf("different goroutine id = %d, want distinct from %d", got, self)
	}
}
