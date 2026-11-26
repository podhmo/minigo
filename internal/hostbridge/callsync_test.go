package hostbridge

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
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

			want := map[any]string{int64(1): "host"}
			if diff := cmp.Diff(want, tc.host); diff != "" {
				t.Fatalf("host map mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
