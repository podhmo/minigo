package hostbridge

import (
	"bytes"
	"reflect"
	"testing"
	"time"

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

func TestPullProjectedKeepsHostKeyTypes(t *testing.T) {
	typedInt64 := runtime.Tag(runtime.BasicTypedef("int64"), int64(1))
	tests := []struct {
		name       string
		hostKey    any
		scriptKey  runtime.Value
		rejectBare bool
	}{
		{name: "int64", hostKey: int64(1), scriptKey: typedInt64, rejectBare: true},
		{name: "named int64", hostKey: time.Second, scriptKey: time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := runtime.NewMap(nil, true)
			hm := map[any]string{tc.hostKey: "host"}
			e := &projEntry{
				sv:   sm,
				rv:   reflect.ValueOf(hm),
				snap: map[string]runtime.Value{},
				host: map[string]bool{},
			}

			m := &Marshal{}
			m.pullProjected(e, nil, map[runtime.Value]bool{})

			if got, ok := sm.Get(tc.scriptKey); !ok {
				t.Fatal("script key entry missing")
			} else if diff := cmp.Diff("host", got); diff != "" {
				t.Fatalf("script value mismatch (-want +got):\n%s", diff)
			}
			if tc.rejectBare {
				if _, ok := sm.Get(int64(1)); ok {
					t.Fatal("host int64 key collapsed to script int")
				}
			}
			m.syncProjected(e, nil)
			if diff := cmp.Diff(map[any]string{tc.hostKey: "host"}, hm); diff != "" {
				t.Fatalf("host map mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProjectionSnapshotKeysUsePointerIdentity(t *testing.T) {
	b1 := bytes.NewBufferString("same")
	b2 := bytes.NewBufferString("same")
	scriptBefore := projKey(b1)
	hostBefore := hostKey(reflect.ValueOf(b1))
	b1.WriteString(" changed")
	if diff := cmp.Diff(projKey(b1), projKey(b2)); diff == "" {
		t.Fatal("distinct same-content buffer keys collapsed")
	}
	if diff := cmp.Diff(scriptBefore, projKey(b1)); diff != "" {
		t.Fatalf("same buffer key changed across mutation (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(hostBefore, hostKey(reflect.ValueOf(b1))); diff != "" {
		t.Fatalf("same host buffer key changed across mutation (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(hostKey(reflect.ValueOf(b1)), hostKey(reflect.ValueOf(b2))); diff == "" {
		t.Fatal("distinct same-content host buffer keys collapsed")
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
