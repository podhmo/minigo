package vm

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"

	"github.com/podhmo/minigo/runtime"
)

// named composites for hostTypedefOf's kind mapping.
type testPtr *int
type testArr [4]int
type testSlice []int

func TestHostTypedefOfKind(t *testing.T) {
	v := &VM{}
	for _, c := range []struct {
		name string
		rt   reflect.Type
		want runtime.TypeKind
	}{
		{"struct", reflect.TypeOf(struct{}{}), runtime.KindStruct},
		{"named pointer", reflect.TypeOf(testPtr(nil)), runtime.KindPointer},
		{"named array", reflect.TypeOf(testArr{}), runtime.KindSlice},
		{"named slice", reflect.TypeOf(testSlice{}), runtime.KindSlice},
		{"named unsafe.Pointer", reflect.TypeOf(unsafe.Pointer(nil)), runtime.KindNamedBasic},
		{"named basic", reflect.TypeOf(int64(0)), runtime.KindNamedBasic},
		{"interface", reflect.TypeOf((*fmt.Stringer)(nil)).Elem(), runtime.KindInterface},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := v.hostTypedefOf(c.rt)
			if td == nil {
				t.Fatal("nil typedef")
			}
			if td.Kind != c.want {
				t.Fatalf("kind = %v, want %v", td.Kind, c.want)
			}
		})
	}
}

// A package-named `type error` is not the builtin: hostTypedefOf must
// not route rt.Name() through builtinTypedef for a package-scoped name.
// Declared inside the test so the shadow lives only in this scope —
// reflect still reports its (non-empty) package path.
func TestHostTypedefOfBuiltinShadow(t *testing.T) {
	type error struct{}

	builtinInt := &runtime.TypeDef{Name: "int", Kind: runtime.KindNamedBasic}
	v := &VM{H: Hooks{Builtin: func(name string) (runtime.Value, bool) {
		if name == "int" || name == "error" {
			return builtinInt, true
		}
		return nil, false
	}}}

	// a package-declared `type error` must not resolve on the name alone.
	td := v.hostTypedefOf(reflect.TypeOf(error{}))
	if td == builtinInt {
		t.Fatalf("package-named error resolved to the builtin typedef")
	}
	if td == nil || td.Kind != runtime.KindStruct {
		t.Fatalf("kind = %v, want KindStruct", td.Kind)
	}

	// a predeclared name still resolves to the builtin.
	if td := v.hostTypedefOf(reflect.TypeOf(int(0))); td != builtinInt {
		t.Fatalf("predeclared int should resolve to the builtin typedef")
	}
}
