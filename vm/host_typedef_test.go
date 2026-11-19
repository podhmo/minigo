package vm

import (
	"fmt"
	"testing"
	"unsafe"

	"github.com/podhmo/minigo/internal/hostbridge"
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
		rt   hostbridge.Type
		want runtime.TypeKind
	}{
		{"struct", hostbridge.TypeOf(struct{}{}), runtime.KindStruct},
		{"named pointer", hostbridge.TypeOf(testPtr(nil)), runtime.KindPointer},
		{"named array", hostbridge.TypeOf(testArr{}), runtime.KindSlice},
		{"named slice", hostbridge.TypeOf(testSlice{}), runtime.KindSlice},
		{"named unsafe.Pointer", hostbridge.TypeOf(unsafe.Pointer(nil)), runtime.KindNamedBasic},
		{"named basic", hostbridge.TypeOf(int64(0)), runtime.KindNamedBasic},
		{"interface", hostbridge.TypeOf((*fmt.Stringer)(nil)).Elem(), runtime.KindInterface},
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
	td := v.hostTypedefOf(hostbridge.TypeOf(error{}))
	if td == builtinInt {
		t.Fatalf("package-named error resolved to the builtin typedef")
	}
	if td == nil || td.Kind != runtime.KindStruct {
		t.Fatalf("kind = %v, want KindStruct", td.Kind)
	}

	// a predeclared name still resolves to the builtin.
	if td := v.hostTypedefOf(hostbridge.TypeOf(int(0))); td != builtinInt {
		t.Fatalf("predeclared int should resolve to the builtin typedef")
	}
}

// source twins of these types fill through toReflectValue.
type testPair struct {
	a string
	b int64
}
type testCyc struct{ next *testCyc }

var _ = testCyc{next: nil}

func testTwinDef(name string, fields ...string) *runtime.TypeDef {
	return &runtime.TypeDef{
		Pkg:    &runtime.Package{Path: "github.com/podhmo/minigo/vm"},
		Name:   name,
		Kind:   runtime.KindStruct,
		Fields: fields,
	}
}

// TestTwinStructConversion checks the source-twin arm of toReflectValue:
// a script struct mints a real host struct — unexported fields included —
// and a self-referential twin fails on the depth cap instead of crashing.
func TestTwinStructConversion(t *testing.T) {
	pair := &runtime.Struct{Def: testTwinDef("testPair", "a", "b"),
		Fields: []runtime.Value{"x", int64(3)}}
	rv, err := marshalHost.ToReflect(pair, hostbridge.TypeOf(testPair{}), nil)
	if err != nil {
		t.Fatalf("twin convert: %v", err)
	}
	if got := rv.Interface().(testPair); got.a != "x" || got.b != 3 {
		t.Fatalf("converted %+v, want {a:x b:3}", got)
	}

	// `type P testPair` is not a twin — a Named tag of a different
	// declared type must not convert, like gc's unassignability.
	aliased := &runtime.Named{Typ: testTwinDef("notPair", "a", "b"), V: pair}
	if _, err := marshalHost.ToReflect(aliased, hostbridge.TypeOf(testPair{}), nil); err == nil {
		t.Fatal("differently-named struct converted; want unassignable")
	}

	// a cyclic twin must error, not recurse until the stack gives out.
	cyc := &runtime.Struct{Def: testTwinDef("testCyc", "next"), Fields: []runtime.Value{nil}}
	cyc.Fields[0] = &runtime.Cell{Elem: cyc}
	if _, err := marshalHost.ToReflect(&runtime.Cell{Elem: cyc}, hostbridge.TypeOf((*testCyc)(nil)), nil); err == nil {
		t.Fatal("cyclic twin converted; want depth error")
	}
}
