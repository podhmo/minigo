package runtime

import (
	goruntime "runtime"
	"testing"
	"time"
	"weak"
)

// mintSynthImportProbe registers one import ref and drops it before
// returning. The weak key itself does not keep the ref alive.
//
//go:noinline
func mintSynthImportProbe() (string, weak.Pointer[ImportRef], bool) {
	ref := &ImportRef{Path: "example.com/synthprobe"}
	name := MintSynthImportName(ref)
	registered := SynthImportRef(name) == ref
	return name, weak.Make(ref), registered
}

func TestSynthImportRegistryReleasesRefs(t *testing.T) {
	name, wp, registered := mintSynthImportProbe()
	if !registered {
		t.Fatal("minted import was not registered while its ref was live")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if wp.Value() == nil && SynthImportRef(name) == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("synthetic import outlived its ref: ref=%v entry=%v", wp.Value() != nil, SynthImportRef(name) != nil)
		}
		goruntime.GC()
		time.Sleep(5 * time.Millisecond) // cleanups run on their own goroutine
	}
}
