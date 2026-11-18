package execution

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestIntern(t *testing.T) {
	tab := NewIntern[string, *int]()

	a, b := new(int), new(int)
	// first registration wins: the loser is dropped.
	if got := tab.Of("k", a); got != a {
		t.Fatal("first Of did not return its descriptor")
	}
	if got := tab.Of("k", b); got != a {
		t.Fatal("second Of did not return the interned descriptor")
	}
	if diff := cmp.Diff(1, tab.Len()); diff != "" {
		t.Fatal(diff)
	}

	// concurrent registration of one key converges on one descriptor.
	const n = 16
	losers := make([]*int, n)
	for i := range losers {
		losers[i] = new(int)
	}
	got := make([]*int, n)
	var wg sync.WaitGroup
	for i := range losers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = tab.Of("race", losers[i])
		}(i)
	}
	wg.Wait()
	for i := range got {
		if got[i] != got[0] {
			t.Fatalf("goroutine %d saw a different descriptor", i)
		}
	}

	// Lookup never stores.
	if _, ok := tab.Lookup("absent"); ok {
		t.Fatal("Lookup hit an absent key")
	}
	if diff := cmp.Diff(2, tab.Len()); diff != "" {
		t.Fatal(diff)
	}
}
