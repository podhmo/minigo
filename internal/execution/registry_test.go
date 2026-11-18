package execution

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRegistry covers register/lookup/replace — including the
// nil-registry reads a package never given specials relies on.
func TestRegistry(t *testing.T) {
	r := NewRegistry[string, int]()
	var nilReg *Registry[string, int]
	if nilReg.Has("x") || nilReg.Len() != 0 {
		t.Fatal("nil registry is not empty")
	}
	if _, ok := nilReg.Lookup("x"); ok {
		t.Fatal("nil registry hit")
	}
	r.Register("a", 1)
	r.Register("a", 2)
	v, ok := r.Lookup("a")
	if diff := cmp.Diff(v, 2); diff != "" || !ok {
		t.Fatalf("Lookup = %v, %v%s", v, ok, diff)
	}
	if !r.Has("a") || r.Has("b") || r.Len() != 1 {
		t.Fatal("Has/Len wrong")
	}
}
