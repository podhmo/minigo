package minigo

import "testing"

// BenchmarkNewEngine measures the cost of constructing an Engine from
// scratch (resolver scan + stdlib install), which is dominated by the
// `go env` subprocesses started by pkg/locator.
func BenchmarkNewEngine(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = NewEngine(".")
	}
}
