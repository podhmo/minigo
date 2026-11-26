package hostbridge

import (
	"reflect"
	goruntime "runtime"
	"testing"
	"time"
	"weak"

	"github.com/podhmo/minigo/runtime"
)

// storeHostShapeProbe stores one shape for a typedef and drops both the
// typedef and the marshal before returning. The weak key itself does not
// keep the typedef alive.
//
//go:noinline
func storeHostShapeProbe() (weak.Pointer[runtime.TypeDef], bool) {
	td := &runtime.TypeDef{Name: "probeT", Kind: runtime.KindStruct}
	m := &Marshal{
		HasVM:     func(runtime.VMCaller) bool { return true },
		HostTypOf: func(*runtime.TypeDef) reflect.Type { return nil },
	}
	m.cachedHostShape(nil, td)
	key := weak.Make(td)
	_, ok := hostShapes.Load(key)
	return key, ok
}

func TestHostShapeCacheReleasesTypedefs(t *testing.T) {
	key, ok := storeHostShapeProbe()
	if !ok {
		t.Fatal("shape was not cached while its typedef was live")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := hostShapes.Load(key); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("host shape entry outlived its typedef")
		}
		goruntime.GC()
		time.Sleep(5 * time.Millisecond) // cleanups run on their own goroutine
	}
}

// recordStructOrigProbe records one projection and drops both it and the
// script value before returning.
//
//go:noinline
func recordStructOrigProbe() (uintptr, weak.Pointer[runtime.Struct], bool) {
	rt := reflect.StructOf([]reflect.StructField{{Name: "X", Type: reflect.TypeFor[string]()}})
	x := reflect.New(rt).Interface()
	orig := &runtime.Struct{}
	RecordStructOrig(x, orig)
	key, ok := structOrigKey(x)
	if !ok {
		return 0, weak.Pointer[runtime.Struct]{}, false
	}
	_, registered := structOrigs.Load(key)
	wp := weak.Make(orig)
	goruntime.KeepAlive(x)
	return key, wp, registered
}

func TestStructOrigsReleaseProjections(t *testing.T) {
	key, wp, registered := recordStructOrigProbe()
	if !registered {
		t.Fatal("projection was not registered while live")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, entryGone := structOrigs.Load(key)
		if !entryGone && wp.Value() == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection entry or original outlived the projection: entry=%v original=%v", !entryGone, wp.Value() != nil)
		}
		goruntime.GC()
		time.Sleep(5 * time.Millisecond) // cleanups run on their own goroutine
	}
}
