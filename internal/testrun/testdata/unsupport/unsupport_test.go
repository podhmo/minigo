package unsupport

import "testing"

func TestFine(t *testing.T) {}

// TestBadShape has the wrong signature: go vet would flag it, minigo
// test reports it malformed and does not run it.
func TestBadShape(t *testing.T, extra int) {}

// BenchmarkV compiles but, like `go test` without -bench, does not run.
func BenchmarkV(b *testing.B) {}

// FuzzV likewise.
func FuzzV(f *testing.F) {}

// ExampleV is reported unsupported but not fatal: without an
// // Output: comment go test would never run it either.
func ExampleV() {}

// TestTwoParams declares two parameters in one field — malformed.
func TestTwoParams(t, u *testing.T) {}
