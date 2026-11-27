package unsupport

import "testing"

func TestFine(t *testing.T) {}

// TestBadShape has the wrong signature: go vet would flag it, minigo
// test reports it malformed and does not run it.
func TestBadShape(t *testing.T, extra int) {}

// BenchmarkV is reported unsupported — it compiles (the B placeholder
// answers the type) but never runs.
func BenchmarkV(b *testing.B) {}

// FuzzV likewise.
func FuzzV(f *testing.F) {}
