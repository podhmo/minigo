package hostbridge

import (
	"go/ast"
	"go/token"
	"math"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

// sizedIntTyp names the builtin typedef for a sized int kind — the
// reflect kind string spells the Go name exactly ("int8", "uint32").
func sizedIntTyp(k reflect.Kind) *runtime.TypeDef {
	return &runtime.TypeDef{Name: k.String(), Kind: runtime.KindNamedBasic}
}

// hostRValueType is the facade's host type: a host []*RValue (MapKeys,
// Call results) spells its elements reflect.Value like Go.
var hostRValueType = reflect.TypeOf((*minireflect.RValue)(nil))

// AnonExprOf renders a reflect type as the anonymous type expression a
// typedef carries: named types answer their bare identifier (the same
// spelling elemTypeName produced), anonymous composites answer their
// structure — an empty interface is `interface{}` like typeExprName
// spells it, so `map[string]any`, `[]any`, `chan int` and friends mint
// typedefs whose spelling matches the script-side equivalent instead
// of one unparseable identifier ("map[string]interface {}").
// reflectTypeExpr walks the same reflect.Type→ast.Expr shape with the
// deliberately different contract host-func signatures need — nil for
// named types rather than a fabricated identifier.
func AnonExprOf(t reflect.Type) ast.Expr {
	if t == hostRValueType {
		return ast.NewIdent("reflect.Value")
	}
	if n := t.Name(); n != "" {
		return ast.NewIdent(n)
	}
	switch t.Kind() {
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return &ast.InterfaceType{Methods: &ast.FieldList{}}
		}
	case reflect.Map:
		return &ast.MapType{Key: AnonExprOf(t.Key()), Value: AnonExprOf(t.Elem())}
	case reflect.Slice:
		return &ast.ArrayType{Elt: AnonExprOf(t.Elem())}
	case reflect.Array:
		return &ast.ArrayType{
			Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(t.Len())},
			Elt: AnonExprOf(t.Elem()),
		}
	case reflect.Pointer:
		return &ast.StarExpr{X: AnonExprOf(t.Elem())}
	case reflect.Chan:
		var dir ast.ChanDir
		switch t.ChanDir() {
		case reflect.RecvDir:
			dir = ast.RECV
		case reflect.SendDir:
			dir = ast.SEND
		default:
			dir = ast.RECV | ast.SEND
		}
		return &ast.ChanType{Dir: dir, Value: AnonExprOf(t.Elem())}
	case reflect.Struct:
		fl := &ast.FieldList{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fd := &ast.Field{Type: AnonExprOf(f.Type)}
			if !f.Anonymous {
				fd.Names = []*ast.Ident{ast.NewIdent(f.Name)}
			}
			if f.Tag != "" {
				fd.Tag = &ast.BasicLit{Kind: token.STRING, Value: "`" + string(f.Tag) + "`"}
			}
			fl.List = append(fl.List, fd)
		}
		return &ast.StructType{Fields: fl}
	case reflect.Func:
		fields := func(n int, at func(i int) reflect.Type) *ast.FieldList {
			fl := &ast.FieldList{}
			for i := 0; i < n; i++ {
				fl.List = append(fl.List, &ast.Field{Type: AnonExprOf(at(i))})
			}
			return fl
		}
		ft := &ast.FuncType{Params: fields(t.NumIn(), t.In)}
		if t.IsVariadic() {
			last := ft.Params.List[len(ft.Params.List)-1]
			last.Type = &ast.Ellipsis{Elt: last.Type.(*ast.ArrayType).Elt}
		}
		if t.NumOut() > 0 {
			ft.Results = fields(t.NumOut(), t.Out)
		}
		return ft
	}
	return ast.NewIdent(t.String())
}

// AnonSliceTyp builds the anonymous []name typedef used to tag slices
// unboxed from host values (no package context — the name is a builtin).
func AnonSliceTyp(name string) *runtime.TypeDef {
	return AnonSliceTypX(ast.NewIdent(name))
}

// AnonSliceTypX builds the anonymous []T typedef from a real element
// expression — composite element types (maps, nested slices) need the
// structure a bare identifier cannot carry, or the typedef's spelling
// stops matching the script-side equivalent.
func AnonSliceTypX(elt ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: elt}}
}

// AnonMapTypX builds the anonymous map[K]V typedef from real key and
// element expressions — see AnonSliceTypX for why a bare identifier is
// not enough.
func AnonMapTypX(key, elem ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindMap, Anon: &ast.MapType{
		Key:   key,
		Value: elem,
	}}
}

// AnonArrayTypX builds the anonymous [n]T typedef from a real element
// expression — see AnonSliceTypX.
func AnonArrayTypX(n int, elt ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{
		Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(n)},
		Elt: elt,
	}}
}

// HostNamedKind classifies a named host type with the source
// classifier's kind table (arrays lump with slices, `type P *T` is
// KindPointer): named composites are not structs.
func HostNamedKind(rt reflect.Type) runtime.TypeKind {
	switch rt.Kind() {
	case reflect.Interface:
		return runtime.KindInterface
	case reflect.Slice, reflect.Array:
		return runtime.KindSlice
	case reflect.Map:
		return runtime.KindMap
	case reflect.Chan:
		return runtime.KindChan
	case reflect.Func:
		return runtime.KindFunc
	case reflect.Pointer:
		return runtime.KindPointer
	case reflect.Struct:
		return runtime.KindStruct
	}
	// basic kinds, unsafe.Pointer
	return runtime.KindNamedBasic
}

// HostSliceTypOf is the typedef tag for an unnamed slice of host
// element rt — the anon expression for the shape plus the element's
// package-level identity for display and generic inference, the same
// tag GoValueOf stamps on an unboxed unnamed host slice.
func HostSliceTypOf(rt reflect.Type) *runtime.TypeDef {
	td := AnonSliceTypX(AnonExprOf(rt))
	td.Elem = HostPkgElemTypedef(rt)
	return td
}

// HostPkgElemTypedef is the element identity a host slice keeps when
// its element is a type declared in a package, or a pointer to one: the
// bare name AnonExprOf spells (`[]Stmt`, `[]*Comment`) resolves nowhere,
// so generic inference reads this typedef instead (see
// [VM.sourceTwinOf]). Other elements return nil.
func HostPkgElemTypedef(rt reflect.Type) *runtime.TypeDef {
	if rt.Kind() == reflect.Pointer && rt.Name() == "" {
		if et := HostPkgElemTypedef(rt.Elem()); et != nil {
			return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
		}
		return nil
	}
	if rt.Name() == "" || rt.PkgPath() == "" {
		return nil
	}
	// identity only: a HostNew would make Zero build a host box where
	// an interface element's zero is nil.
	return &runtime.TypeDef{Name: rt.String(), Kind: HostNamedKind(rt), HostIdent: rt}
}

// hostNilPtrTypes memoizes [HostNilPtrTypedef] per reflect type: one
// typedef per host pointer type keeps a nil *T read cheap and lets the
// interface-satisfaction memo key on it.
var hostNilPtrTypes sync.Map

// HostNilPtrTypedef is the typedef a nil host *T carries as a
// [runtime.TypedNil] — [HostPkgElemTypedef]'s anonymous pointer to a
// named host type — or nil when rt is not a pointer to a named host
// type (such nils stay untyped). [HostNilPtrType] reads it back.
func HostNilPtrTypedef(rt reflect.Type) *runtime.TypeDef {
	if td, ok := hostNilPtrTypes.Load(rt); ok {
		return td.(*runtime.TypeDef)
	}
	if rt.Kind() != reflect.Pointer || rt.Name() != "" || rt.Elem().Name() == "" || rt.Elem().PkgPath() == "" {
		return nil
	}
	td := HostPkgElemTypedef(rt)
	if td == nil {
		return nil
	}
	got, _ := hostNilPtrTypes.LoadOrStore(rt, td)
	return got.(*runtime.TypeDef)
}

// hostNils memoizes [HostNil] per reflect type — including a nil
// *runtime.TypedNil for a type whose nils stay untyped.
var hostNils sync.Map

// HostNil is the typed nil a nil host value of type rt reads as: a nil
// *T of a named host type ([HostNilPtrTypedef]), a nil named host slice
// or map (net.IP, url.Values), or a nil chan, func or
// pointer of an unnamed type spelled from predeclared types only
// (`<-chan struct{}` from context.Background().Done(), exec.Cmd's
// `func() error` Cancel, `*int`). It compares equal to nil but keeps
// its dynamic type once stored in an interface. The value is shared per
// type — a nil read allocates nothing — so callers must not mutate it.
// Other types report nil: their nils read as untyped nil.
func HostNil(rt reflect.Type) *runtime.TypedNil {
	if tn, ok := hostNils.Load(rt); ok {
		return tn.(*runtime.TypedNil)
	}
	var tn *runtime.TypedNil
	if td := HostNilPtrTypedef(rt); td != nil {
		tn = &runtime.TypedNil{Typ: td}
	} else if (rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map) && rt.Name() != "" && rt.PkgPath() != "" {
		tn = &runtime.TypedNil{Typ: HostPkgElemTypedef(rt)}
	} else if rt.Name() == "" && (rt.Kind() == reflect.Chan || rt.Kind() == reflect.Func || rt.Kind() == reflect.Pointer) &&
		universeSpelled(rt) {
		tn = &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: HostNamedKind(rt), Anon: AnonExprOf(rt)}}
	}
	got, _ := hostNils.LoadOrStore(rt, tn)
	return got.(*runtime.TypedNil)
}

// universeSpelled reports whether rt is spelled from predeclared types
// only, so [AnonExprOf]'s spelling resolves in any file: no named type
// of a package, no unexported struct field, no non-empty unnamed
// interface.
func universeSpelled(rt reflect.Type) bool {
	if rt.Name() != "" {
		return rt.PkgPath() == "" && rt.Kind() != reflect.UnsafePointer
	}
	switch rt.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return universeSpelled(rt.Elem())
	case reflect.Map:
		return universeSpelled(rt.Key()) && universeSpelled(rt.Elem())
	case reflect.Func:
		for i := 0; i < rt.NumIn(); i++ {
			if !universeSpelled(rt.In(i)) {
				return false
			}
		}
		for i := 0; i < rt.NumOut(); i++ {
			if !universeSpelled(rt.Out(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			if f := rt.Field(i); f.PkgPath != "" || !universeSpelled(f.Type) {
				return false
			}
		}
		return true
	case reflect.Interface:
		return rt.NumMethod() == 0
	}
	return false
}

// HostNilType reports the named host slice or map type a nil of type td
// holds: the typedef [HostNil] mints for one (net.IP, url.Values), or a
// bound host type (hostType) whose zero is a slice or map. nil for any
// other typedef.
func HostNilType(td *runtime.TypeDef) reflect.Type {
	if td == nil || td.Spec != nil {
		return nil
	}
	if (td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) && td.HostIdent != nil {
		return td.HostIdent
	}
	if td.HostNew != nil {
		if rt := td.HostType(); rt != nil && (rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map) && rt.Name() != "" {
			return rt
		}
	}
	return nil
}

// HostNilPtrType reports the host pointer type behind a typedef minted by
// [HostNilPtrTypedef] — an anonymous pointer to a named host type — or
// nil for any other typedef.
func HostNilPtrType(td *runtime.TypeDef) reflect.Type {
	if td == nil || td.Kind != runtime.KindPointer || td.Name != "" || td.Spec != nil || td.Anon != nil ||
		td.Elem == nil || td.Elem.HostIdent == nil {
		return nil
	}
	return reflect.PointerTo(td.Elem.HostIdent)
}

// structOrigs records the script value each fabricated *struct{...}
// projection stands for: a host that hands one back — a FuncMap func's
// argument, an `any` round-trip — restores the script object it
// started from rather than surfacing the anonymous host struct.
// Entries live for the process, the same trade-off as
// runtime.opaqueKeyOrigs.
var structOrigs sync.Map

// StructOrig reports the script value a *struct{...} projection
// minted by structDataHost stands for — for recoverers outside the vm
// package (minigo's jsonHostArg). The type gate precedes the lookup:
// an unhashable x (a slice or map payload) would panic in sync.Map.
func StructOrig(x any) (runtime.Value, bool) {
	rt := reflect.TypeOf(x)
	if rt == nil || rt.Kind() != reflect.Pointer {
		return nil, false
	}
	if et := rt.Elem(); et.Kind() != reflect.Struct || et.Name() != "" {
		return nil, false
	}
	if orig, ok := structOrigs.Load(x); ok {
		return orig, true
	}
	return nil, false
}

// GoValueOf adapts a reflect result to a runtime value: script-native types
// pass through, everything else stays boxed as a host GoValue. Value is
// `any`, so the pass-through list names the concrete runtime types.
func GoValueOf(rv reflect.Value) runtime.Value {
	if !rv.IsValid() {
		return runtime.NIL
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			// a nil *T of a named host type, or a nil chan/func of a
			// predeclared spelling, is a typed nil ([HostNil]): it
			// still compares equal to nil, but keeps its dynamic type
			// when stored into an interface and dispatches T's pointer
			// methods on the nil receiver, as in Go. Other nil
			// pointer-ish host values read as plain nil — boxing them
			// would make `v == nil` comparisons lie.
			if rv.Kind() != reflect.Interface {
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
			return runtime.NIL
		}
	}
	x := rv.Interface()
	// a canonical opaque-store key coming back from a container
	// (sync.Map Range callback, a loaded CAS comparand) restores the
	// script value it was rendered from; everything else is not a
	// canonical form and falls through to marshal as usual.
	if orig, ok := runtime.OpaqueKeyOrig(x); ok {
		return orig
	}
	// an anonymous *struct{...} is the projection structDataHost
	// fabricates for a script struct; a host handing it back (a FuncMap
	// func's argument, an `any` load) restores the script value.
	if orig, ok := StructOrig(x); ok {
		return orig
	}
	switch v := x.(type) {
	case nil:
		return runtime.NIL
	case int:
		return int64(v)
	case int64:
		// a concrete int64 keeps its width so %T spells `int64`; a
		// payload unwrapped from an interface is a script int, spelled
		// `int` (both store int64 — only rv's own kind can tell them
		// apart).
		if rv.Kind() == reflect.Interface {
			return v
		}
		return runtime.Tag(sizedIntTyp(reflect.Int64), v)
	case int8, int16, int32:
		// sized ints keep their declared width so %T spells them
		// like Go (int32 also covers rune — an alias). The tag reads
		// the payload's kind: rv may be interface-shaped while x is
		// the concrete payload.
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), pv.Int())
	case uint, uint8, uint16, uint32, uintptr:
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), int64(pv.Uint()))
	case uint64:
		if v <= math.MaxInt64 {
			if rv.Kind() == reflect.Interface {
				return int64(v)
			}
			return runtime.Tag(sizedIntTyp(reflect.Uint64), int64(v))
		}
		return &runtime.GoValue{V: x}
	case string:
		return v
	case bool:
		return v
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(&runtime.TypeDef{Name: "float32", Kind: runtime.KindNamedBasic}, float64(v))
	case float64:
		return v
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return v
	case []byte:
		// []byte unmarshals to a slice of int64s so `string(b)` and
		// indexing behave like Go source suggests. A nil slice is a
		// typed nil so `b == nil` reads like Go.
		if v == nil {
			return &runtime.TypedNil{Typ: AnonSliceTyp("byte")}
		}
		el := make([]runtime.Value, len(v))
		for i, b := range v {
			el[i] = runtime.ByteValue(b)
		}
		return &runtime.Slice{Elems: el, Typ: AnonSliceTyp("byte")}
	case []string:
		if v == nil {
			return &runtime.TypedNil{Typ: AnonSliceTyp("string")}
		}
		el := make([]runtime.Value, len(v))
		for i, s := range v {
			el[i] = s
		}
		return &runtime.Slice{Elems: el, Typ: AnonSliceTyp("string")}
	case []any:
		// a `chan any` send deep-hosts script containers (see deepHost);
		// the receive rehydrates the flat slice shape so indexing works.
		if v == nil {
			return &runtime.TypedNil{Typ: AnonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(v))
		for i, e := range v {
			el[i] = GoValueOf(reflect.ValueOf(e))
		}
		return &runtime.Slice{Elems: el, Typ: AnonSliceTyp("any")}
	case map[any]any:
		if v == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := runtime.NewMap(nil, false)
		for k, e := range v {
			m.Insert(GoValueOf(reflect.ValueOf(k)), GoValueOf(reflect.ValueOf(e)))
		}
		return m
	case runtime.ScriptBoxed:
		// a script value the host carried in a proxy (a script error
		// returned by a host Unwrap) reads back as itself.
		return v.ScriptValue()
	case error:
		// errors stay boxed — Error() and Unwrap() dispatch via reflection;
		// errors.Is/As unwrap through goNative.
		return &runtime.GoValue{V: x}
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.TypedNil, *runtime.IfaceNil,
		*runtime.Named, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return v
	case runtime.Named:
		// opaqueStoreArg stored a Named by value so `any` storage keys
		// compare by content — a loaded value re-wraps to the pointer
		// form, keeping its tag for script type asserts.
		return NamedLoadValue(v)
	default:
		// an unnamed host slice/array ([N]T, []T) unboxes element-wise so
		// indexing and range work; a named slice type keeps its box to
		// preserve method dispatch. A nil slice is a typed nil so
		// `v == nil` reads like Go.
		switch rv.Kind() {
		case reflect.Slice:
			if rv.IsNil() && rv.Type().Name() != "" {
				// a nil named host slice (net.ParseIP's IP on bad
				// input) is a typed nil of the host type, as a nil *T
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
			if rv.Type().Name() == "" {
				td := HostSliceTypOf(rv.Type().Elem())
				if rv.IsNil() {
					return &runtime.TypedNil{Typ: td}
				}
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = GoValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: td}
			}
		case reflect.Map:
			if rv.IsNil() && rv.Type().Name() != "" {
				// a nil named host map (a request's Form, Trailer) is a
				// typed nil of the host type, like a nil named slice
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
		case reflect.Array:
			if rv.Type().Name() == "" {
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = GoValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: AnonArrayTypX(rv.Len(), AnonExprOf(rv.Type().Elem()))}
			}
		}
		return &runtime.GoValue{V: x}
	}
}

// GoSlotValueOf is GoValueOf for a value read out of a host interface
// slot — a host call's `any` result (sync.Map.Load, context.Value,
// atomic.Value.Load), a callback's `any` param (sync.Map.Range), an
// `any` field. A typed nil held there is a non-nil interface, so it
// reads back boxed as an [runtime.IfaceNil] that keeps its dynamic type:
// `v == nil` is false and `v.(*T)` still matches. GoValueOf alone would
// yield the bare typed nil, which equals nil. Script channels also ride
// on `chan any` but keep their element type, so they use GoValueOf.
func GoSlotValueOf(rv reflect.Value) runtime.Value {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		e := rv.Elem()
		switch e.Kind() {
		case reflect.Chan, reflect.Func:
			// a nil host chan/func of a predeclared spelling
			if e.IsNil() {
				if tn := HostNil(e.Type()); tn != nil {
					return &runtime.IfaceNil{Typ: tn.Typ}
				}
			}
		case reflect.Pointer:
			if e.IsNil() {
				// a nil host *T of a named type (os.Open's file on error)
				if tn := HostNil(e.Type()); tn != nil {
					return &runtime.IfaceNil{Typ: tn.Typ}
				}
			} else if tn, ok := e.Interface().(*runtime.TypedNil); ok && tn.Typ != nil {
				// a script typed nil stored verbatim
				return &runtime.IfaceNil{Typ: tn.Typ}
			}
		}
		// a typed nil stored as a canonical opaque key (a sync.Map
		// value, an atomic.Value) restores to its typed nil — box it
		if orig, ok := runtime.OpaqueKeyOrig(e.Interface()); ok {
			if tn, isTN := orig.(*runtime.TypedNil); isTN && tn.Typ != nil {
				return &runtime.IfaceNil{Typ: tn.Typ}
			}
			return orig
		}
	}
	return GoValueOf(rv)
}

// RecordStructOrig stores the script value a fabricated *struct{...}
// projection stands for; structDataHost in vm mints the projections.
func RecordStructOrig(x any, orig runtime.Value) {
	structOrigs.Store(x, orig)
}
