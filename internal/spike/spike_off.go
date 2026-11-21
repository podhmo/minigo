//go:build !minigo_spike

// Package spike carries the rearchitecture spike's instrumentation
// (docs/next/ja/references/spike-instrumentation.md): counters for
// builds triggered from inside the compiler, C1 violations (package
// initialization running while compilation is in progress), and the
// chunkCellsEscape rate. Without the minigo_spike build tag every hook
// is an inlined no-op; with it, the counters in spike_on.go record.
//
// The "in compile" flag is process-global, not goroutine-local: this
// instrumentation is test-only by design.
package spike

// EnterCompile/LeaveCompile bracket a compiler entry point
// (compile.Func, compile.Expr, compile.ExprScoped).
func EnterCompile() {}
func LeaveCompile() {}

// NoteInitDuringCompile records a package bootstrap starting while
// compilation is in progress (a C1 violation).
func NoteInitDuringCompile() {}

// NoteBuildDuringCompile records an Engine.buildPackage call made while
// compilation is in progress.
func NoteBuildDuringCompile() {}

// NoteChunkEscape records one freshly classified chunk's escape verdict.
func NoteChunkEscape(escapes bool) {}

// NoteChunkClassified records one chunk's compile-time slot
// classification (boxed flags).
func NoteChunkClassified(boxed []bool) {}

// CompileBuilds reports how many buildPackage calls happened during
// compilation.
func CompileBuilds() int64 { return 0 }

// InitDuringCompile reports C1 violations.
func InitDuringCompile() int64 { return 0 }

// ChunksScanned reports how many chunks were classified.
func ChunksScanned() int64 { return 0 }

// ChunksEscaped reports how many classified chunks escape their cells.
func ChunksEscaped() int64 { return 0 }

// ChunksClassified/ChunksBoxedAny/SlotsLocal/SlotsBoxed report the
// compile-time slot classification counters.
func ChunksClassified() int64 { return 0 }
func ChunksBoxedAny() int64   { return 0 }
func SlotsLocal() int64       { return 0 }
func SlotsBoxed() int64       { return 0 }

// Reset zeroes every counter.
func Reset() {}
