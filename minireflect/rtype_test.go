package minireflect

import (
	"reflect"
	"testing"
)

// recNodes recurs through its own element — hostTypeOf must not walk
// the shape forever.
type recNodes []recNodes

func TestHostTypeOfRecursiveHostType(t *testing.T) {
	e := &Env{}
	if got := e.hostTypeOf(reflect.TypeOf(recNodes{})); got == nil {
		t.Fatal("hostTypeOf returned nil")
	}
}
