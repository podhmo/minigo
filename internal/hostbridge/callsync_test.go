package hostbridge

import (
	"reflect"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

func TestSyncProjectedCanonicalizesHostIntKeyAlias(t *testing.T) {
	sm := runtime.NewMap(nil, false)
	sm.Insert(int64(1), "host")
	hm := map[any]string{int(1): "host"}
	e := &projEntry{
		sv:   sm,
		rv:   reflect.ValueOf(hm),
		snap: map[string]runtime.Value{},
		host: map[string]bool{},
	}

	m := &Marshal{}
	m.syncProjected(e, nil)

	if len(hm) != 1 {
		t.Fatalf("len(host map) = %d, want 1: %#v", len(hm), hm)
	}
	if got := hm[int64(1)]; got != "host" {
		t.Fatalf("canonical key value = %q, want host", got)
	}
	if _, ok := hm[int(1)]; ok {
		t.Fatalf("host alias key survived: %#v", hm)
	}
}
