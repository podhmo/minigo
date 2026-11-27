package hostbridge

import (
	"reflect"
	goruntime "runtime"
	"sync"
	"unsafe"

	"github.com/podhmo/minigo/runtime"
)

// structOrigs records the script value each fabricated *struct{...}
// projection stands for: a host that hands one back — a FuncMap func's
// argument, an `any` round-trip — restores the script object it
// started from rather than surfacing the anonymous host struct. Keys
// are projection addresses, not the projections themselves: a strong
// key would pin the whole interpreted session for the process. The
// values are weak too: a strong original makes the table itself the
// root of the session/projection cycle it exists to describe. A cleanup
// drops the entry when the projection becomes unreachable.
var structOrigs sync.Map // uintptr -> structOrigEntry

func structOrigPointer(x any) (unsafe.Pointer, bool) {
	rv := reflect.ValueOf(x)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, false
	}
	return rv.UnsafePointer(), true
}

func structOrigKey(x any) (uintptr, bool) {
	p, ok := structOrigPointer(x)
	return uintptr(p), ok
}

// StructOrig reports the script value a *struct{...} projection minted
// by structDataHost stands for — for recoverers outside the vm package
// (minigo's jsonHostArg). ok is false when x is not such a projection or
// when its original has already been collected: the registry cannot pin
// the original strongly without making itself the root of the session it
// describes. The type gate precedes the lookup: an unhashable x (a slice
// or map payload) would panic in sync.Map.
func StructOrig(x any) (runtime.Value, bool) {
	rt := reflect.TypeOf(x)
	if rt == nil || rt.Kind() != reflect.Pointer {
		return nil, false
	}
	if et := rt.Elem(); et.Kind() != reflect.Struct || et.Name() != "" {
		return nil, false
	}
	key, ok := structOrigKey(x)
	if !ok {
		return nil, false
	}
	if ent, ok := structOrigs.Load(key); ok {
		if orig := ent.(structOrigEntry).orig.Value(); orig != nil {
			return orig, true
		}
	}
	return nil, false
}

type structOrigCleanupArg struct {
	key   uintptr
	token *structOrigToken
}

func forgetStructOrig(arg structOrigCleanupArg) {
	if cur, ok := structOrigs.Load(arg.key); ok && cur.(structOrigEntry).token == arg.token {
		structOrigs.CompareAndDelete(arg.key, cur)
	}
}

// RecordStructOrig stores the script value a fabricated *struct{...}
// projection stands for; structDataHost in vm mints the projections.
func RecordStructOrig(x any, orig runtime.Value) {
	key, ok := structOrigKey(x)
	if !ok {
		return
	}
	w, ok := makeWeakStructOrig(orig)
	if !ok {
		return
	}
	token := &structOrigToken{}
	ent := structOrigEntry{orig: w, token: token}
	structOrigs.Store(key, ent)
	// The key deliberately does not keep x alive, and the table entry keeps
	// orig only weakly; strong ownership would make this process-wide table
	// the root of the very session/projection cycle it describes.
	// AddCleanup, unlike a finalizer, has no dependency-ordering problem
	// with object cycles. The projection has an anonymous dynamic type, so
	// attach through a non-dereferenced byte pointer; the cleanup receives
	// only the address key, never the object.
	p, _ := structOrigPointer(x)
	goruntime.AddCleanup((*byte)(p), forgetStructOrig, structOrigCleanupArg{key: key, token: token})
}
