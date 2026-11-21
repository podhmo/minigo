package runtime

import (
	"net"
	"net/http"
	"net/textproto"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type hueA struct{ Next *hueA }
type hueB struct{ Next *hueB }

type hueE1 int
type hueE2 int
type hueL1 []hueE1
type hueL2 []hueE1
type hueL3 []hueE2

func TestHostUnderlyingEq(t *testing.T) {
	rt := func(x any) reflect.Type { return reflect.TypeOf(x) }
	for _, c := range []struct {
		name string
		a, b reflect.Type
		want bool
	}{
		{"same", rt(net.IP{}), rt(net.IP{}), true},
		{"same unnamed underlying", rt(net.IP{}), rt(net.IPMask{}), true},
		{"named leaf same", rt(hueL1{}), rt(hueL2{}), true},
		{"named leaf differs", rt(hueL1{}), rt(hueL3{}), false},
		{"self-referencing leaves", rt(hueA{}), rt(hueB{}), false},
		{"shared named map shape", rt(http.Header{}), rt(textproto.MIMEHeader{}), true},
		{"distinct scalars", rt(int(0)), rt(uint(0)), false},
		{"nil", nil, rt(net.IP{}), false},
	} {
		if diff := cmp.Diff(c.want, HostUnderlyingEq(c.a, c.b)); diff != "" {
			t.Errorf("%s: mismatch (-want +got):\n%s", c.name, diff)
		}
	}
}
