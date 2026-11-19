//go:build minigo_spike

package spike

import (
	"sync"
	"sync/atomic"
)

var (
	compileDepth        atomic.Int64
	initDuringCompile   atomic.Int64
	buildsDuringCompile atomic.Int64
	chunksScanned       atomic.Int64
	chunksEscaped       atomic.Int64
	chunksClassified    atomic.Int64
	chunksBoxedAny      atomic.Int64
	slotsLocal          atomic.Int64
	slotsBoxed          atomic.Int64
	boxReasons          sync.Map // why -> *atomic.Int64
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

// NoteChunkClassified counts one compiled chunk's slot classification:
// chunks with at least one boxed slot, and the boxed/local slot totals.
func NoteChunkClassified(boxed []bool) {
	chunksClassified.Add(1)
	slotsLocal.Add(int64(len(boxed)))
	any := false
	boxedN := int64(0)
	for _, b := range boxed {
		if b {
			any = true
			boxedN++
		}
	}
	if any {
		chunksBoxedAny.Add(1)
	}
	slotsBoxed.Add(boxedN)
}

// NoteBoxReason attributes one slot-box mark to why — the compile-time
// classification source ("ref", "fieldref", "select", "capture",
// "special"). The distribution tells P4-2c where the escape gap lives.
func NoteBoxReason(why string) {
	v, _ := boxReasons.LoadOrStore(why, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

// BoxReasons returns a snapshot of the per-reason box-mark counts.
func BoxReasons() map[string]int64 {
	out := map[string]int64{}
	boxReasons.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

func CompileBuilds() int64     { return buildsDuringCompile.Load() }
func InitDuringCompile() int64 { return initDuringCompile.Load() }
func ChunksScanned() int64     { return chunksScanned.Load() }
func ChunksEscaped() int64     { return chunksEscaped.Load() }
func ChunksClassified() int64  { return chunksClassified.Load() }
func ChunksBoxedAny() int64    { return chunksBoxedAny.Load() }
func SlotsLocal() int64        { return slotsLocal.Load() }
func SlotsBoxed() int64        { return slotsBoxed.Load() }

func Reset() {
	compileDepth.Store(0)
	initDuringCompile.Store(0)
	buildsDuringCompile.Store(0)
	chunksScanned.Store(0)
	chunksEscaped.Store(0)
	chunksClassified.Store(0)
	chunksBoxedAny.Store(0)
	slotsLocal.Store(0)
	slotsBoxed.Store(0)
	boxReasons.Range(func(k, _ any) bool {
		boxReasons.Delete(k)
		return true
	})
}
