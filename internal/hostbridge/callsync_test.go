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
