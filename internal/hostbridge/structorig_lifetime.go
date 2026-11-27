package hostbridge

import (
	"reflect"
	"unsafe"

	"github.com/podhmo/minigo/runtime"
)

// newStructProjection allocates typ inside a wrapper carrying its script
// original. The host sees only the interior *typ pointer, so neither the
// projection's reflect type nor its formatted fields change, while the
// allocation itself ties the original's lifetime exactly to the projection.
func newStructProjection(typ reflect.Type, orig runtime.Value) reflect.Value {
	boxTyp := reflect.StructOf([]reflect.StructField{
		{Name: "P", Type: typ},
		{Name: "_", Type: anyType, PkgPath: "github.com/podhmo/minigo/vm"},
	})
	box := reflect.New(boxTyp)
	fv := box.Elem().Field(1)
	reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem().Set(reflect.ValueOf(orig))
	return box.Elem().Field(0).Addr()
}
