package hostbridge

import (
	"reflect"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

func TestSyncProjectedCanonicalizesHostIntKeyAlias(t *testing.T) {
	for _, tc := range []struct {
		name string
		host map[any]string
	}{
		{name: "alias only", host: map[any]string{int(1): "host"}},
		{name: "canonical and alias", host: map[any]string{int(1): "host", int64(1): "old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := runtime.NewMap(nil, false)
			sm.Insert(int64(1), "host")
			e := &projEntry{
				sv:   sm,
				rv:   reflect.ValueOf(tc.host),
				snap: map[string]runtime.Value{},
				host: map[string]bool{},
			}

			m := &Marshal{}
			m.syncProjected(e, nil)

			if len(tc.host) != 1 {
				t.Fatalf("len(host map) = %d, want 1: %#v", len(tc.host), tc.host)
			}
			if got := tc.host[int64(1)]; got != "host" {
				t.Fatalf("canonical key value = %q, want host", got)
			}
			if _, ok := tc.host[int(1)]; ok {
				t.Fatalf("host alias key survived: %#v", tc.host)
			}
		})
	}
}
