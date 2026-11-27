package hostbridge

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func TestSyncProjectedInterfaceKeysKeepIntegerTypes(t *testing.T) {
	sm := runtime.NewMap(nil, true)
	sm.Insert(int64(1), "one")
	sm.Insert(runtime.Tag(runtime.BasicTypedef("int8"), int64(1)), "eight")
	hm := map[any]string{}
	e := &projEntry{
		sv:   sm,
		rv:   reflect.ValueOf(hm),
		snap: map[string]runtime.Value{},
		host: map[string]bool{},
	}

	m := &Marshal{}
	m.syncProjected(e, nil)

	want := map[any]string{int(1): "one", int8(1): "eight"}
	if diff := cmp.Diff(want, hm); diff != "" {
		t.Fatalf("host map mismatch (-want +got):\n%s", diff)
	}
}

func TestPullProjectedKeepsHostInt64Key(t *testing.T) {
	sm := runtime.NewMap(nil, true)
	hm := map[any]string{int64(1): "host"}
	e := &projEntry{
		sv:   sm,
		rv:   reflect.ValueOf(hm),
		snap: map[string]runtime.Value{},
		host: map[string]bool{},
	}

	m := &Marshal{}
	m.pullProjected(e, nil, map[runtime.Value]bool{})

	typed := runtime.Tag(runtime.BasicTypedef("int64"), int64(1))
	if got, ok := sm.Get(typed); !ok || got != "host" {
		t.Fatalf("script int64 entry = %v, %v; want host, true", got, ok)
	}
	if _, ok := sm.Get(int64(1)); ok {
		t.Fatal("host int64 key collapsed to script int")
	}
	m.syncProjected(e, nil)
	if diff := cmp.Diff(map[any]string{int64(1): "host"}, hm); diff != "" {
		t.Fatalf("host map mismatch (-want +got):\n%s", diff)
	}
}

func TestProjectionSnapshotsKeepIntegerKeyTypes(t *testing.T) {
	sm := runtime.NewMap(nil, true)
	sm.Insert(int64(1), "one")
	sm.Insert(runtime.Tag(runtime.BasicTypedef("int8"), int64(1)), "eight")
	if diff := cmp.Diff(2, len(scriptSnap(sm))); diff != "" {
		t.Fatalf("script snapshot length mismatch (-want +got):\n%s", diff)
	}
	keys := map[string]bool{
		hostKey(reflect.ValueOf(int(1))):   true,
		hostKey(reflect.ValueOf(int8(1))):  true,
		hostKey(reflect.ValueOf(int64(1))): true,
	}
	if diff := cmp.Diff(3, len(keys)); diff != "" {
		t.Fatalf("host key length mismatch (-want +got):\n%s", diff)
	}
}
