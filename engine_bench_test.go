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

// BenchmarkNewSession measures forking a session off a base engine,
// which re-runs installStdlib on the session's own package cache.
func BenchmarkNewSession(b *testing.B) {
	base := NewEngine(".")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = base.NewSession()
	}
}
