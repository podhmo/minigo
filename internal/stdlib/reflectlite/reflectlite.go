// Package reflectlite binds the internal/reflectlite stub surface so
// GOROOT sources running under --src (errors, sort, ...) never reach
// internal/abi's unsafe.Pointer eface reinterpretation: TypeOf/ValueOf
// answer with the same minireflect facades bound `reflect` serves.
package reflectlite

import (
	"reflect"

	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

// Install binds internal/reflectlite over the minireflect facades: its
// Type/Value names differ from reflect's only in the package path, and
// Kind aliases abi.Kind — whose numbering matches reflect.Kind.
func Install(bind func(string, map[string]runtime.Value), hooks minireflect.Hooks) {
	syms := minireflect.Symbols(hooks)
	out := make(map[string]runtime.Value, len(syms)+8)
	for name, v := range syms {
		switch name {
		case "Type", "Value":
			// rebound below under the reflectlite spellings
		default:
			out[name] = v
		}
	}
	out["Type"] = &runtime.TypeDef{
		Name:  "internal/reflectlite.Type",
		Kind:  runtime.KindInterface,
		MReqs: syms["Type"].(*runtime.TypeDef).MReqs,
	}
	out["Value"] = &runtime.TypeDef{
		Name:    "internal/reflectlite.Value",
		Kind:    runtime.KindStruct,
		HostNew: func() any { return &minireflect.RValue{} },
	}
	// reflectlite re-exports abi.Kind's constants; abi keeps reflect's
	// numbering so a facade Kind() compares equal.
	for name, k := range map[string]reflect.Kind{
		"Invalid": reflect.Invalid, "Bool": reflect.Bool,
		"Int": reflect.Int, "Int8": reflect.Int8, "Int16": reflect.Int16,
		"Int32": reflect.Int32, "Int64": reflect.Int64,
		"Uint": reflect.Uint, "Uint8": reflect.Uint8, "Uint16": reflect.Uint16,
		"Uint32": reflect.Uint32, "Uint64": reflect.Uint64,
		"Uintptr": reflect.Uintptr, "Float32": reflect.Float32,
		"Float64": reflect.Float64, "Complex64": reflect.Complex64,
		"Complex128": reflect.Complex128, "Array": reflect.Array,
		"Chan": reflect.Chan, "Func": reflect.Func,
		"Interface": reflect.Interface, "Map": reflect.Map,
		"Pointer": reflect.Pointer, "Slice": reflect.Slice,
		"String": reflect.String, "Struct": reflect.Struct,
		"UnsafePointer": reflect.UnsafePointer,
	} {
		out[name] = int64(k)
	}
	bind("internal/reflectlite", out)
}
