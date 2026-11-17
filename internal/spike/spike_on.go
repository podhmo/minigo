//go:build minigo_spike

package spike

import "sync/atomic"

var (
	compileDepth        atomic.Int64
	initDuringCompile   atomic.Int64
	buildsDuringCompile atomic.Int64
	chunksScanned       atomic.Int64
	chunksEscaped       atomic.Int64
)

func EnterCompile() { compileDepth.Add(1) }
func LeaveCompile() { compileDepth.Add(-1) }

func NoteInitDuringCompile() {
	if compileDepth.Load() > 0 {
		initDuringCompile.Add(1)
	}
}

func NoteBuildDuringCompile() {
	if compileDepth.Load() > 0 {
		buildsDuringCompile.Add(1)
	}
}

func NoteChunkEscape(escapes bool) {
	chunksScanned.Add(1)
	if escapes {
		chunksEscaped.Add(1)
	}
}

func CompileBuilds() int64     { return buildsDuringCompile.Load() }
func InitDuringCompile() int64 { return initDuringCompile.Load() }
func ChunksScanned() int64     { return chunksScanned.Load() }
func ChunksEscaped() int64     { return chunksEscaped.Load() }

func Reset() {
	compileDepth.Store(0)
	initDuringCompile.Store(0)
	buildsDuringCompile.Store(0)
	chunksScanned.Store(0)
	chunksEscaped.Store(0)
}
