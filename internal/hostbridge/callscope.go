package hostbridge

import (
	"reflect"
	"sync"

	"github.com/podhmo/minigo/runtime"
)

// callScopeStack is the per-goroutine stack of script containers the
// calls running on that goroutine projected to host values. A
// named-typedef map or slice crosses an `any` parameter as a real
// host map/slice — reflect ops like template's {{range}} keep
// working — while this registry lets a callback arg arriving as that
// projection resolve back to the script value, keeping its typedef and
// method set (a FuncMap func's `interface{ Encode() string }` param).
// Entries are pushed per host call and dropped when it returns, so a
// freed host map's address can never resolve to a stale script value.
type callScopeStack struct {
	mu     sync.Mutex
	scopes []map[uintptr]runtime.Value
}

// callScopes maps goroutine id -> its stack of projection scopes. The
// goroutine that runs a host call is the only one that registers and
// resolves projections for it; a foreign goroutine (a parked call's
// helper, a template calling from another proc) simply misses the
// lookup and falls back to the plain unbox.
var callScopes sync.Map // int64 -> *callScopeStack

func (m *Marshal) callGid() int64 {
	if m.GoroutineID == nil {
		return 0
	}
	return m.GoroutineID()
}

// pushCallScope opens a projection scope for the duration of a host
// call; the returned func closes it. Nested calls on one goroutine
// nest scopes so an inner call's close keeps the outer projections.
func (m *Marshal) pushCallScope() func() {
	gid := m.callGid()
	if gid == 0 {
		return func() {}
	}
	st, _ := callScopes.LoadOrStore(gid, &callScopeStack{})
	s := st.(*callScopeStack)
	s.mu.Lock()
	s.scopes = append(s.scopes, map[uintptr]runtime.Value{})
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.scopes = s.scopes[:len(s.scopes)-1]
		n := len(s.scopes)
		s.mu.Unlock()
		if n == 0 {
			callScopes.CompareAndDelete(gid, st)
		}
	}
}

// registerProjected records v for the duration of the running host
// call: v is a script map or slice of a NAMED typedef that crossed an
// `any` parameter as the plain host container out — the projection
// keeps no method set, so a callback receiving out back (a FuncMap
// arg) resolves to v, which still carries the typedef's methods.
func (m *Marshal) registerProjected(v runtime.Value, out any) {
	var td *runtime.TypeDef
	switch x := v.(type) {
	case *runtime.Map:
		td = x.Typ
	case *runtime.Slice:
		td = x.Typ
	default:
		return
	}
	if td == nil || td.Name == "" {
		return
	}
	rv := reflect.ValueOf(out)
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return
	}
	gid := m.callGid()
	if gid == 0 {
		return
	}
	st, ok := callScopes.Load(gid)
	if !ok {
		return
	}
	s := st.(*callScopeStack)
	s.mu.Lock()
	if n := len(s.scopes); n > 0 {
		s.scopes[n-1][rv.Pointer()] = v
	}
	s.mu.Unlock()
}

// projectedVal returns the script value registered for the host
// container an `any` parameter is receiving — the projection an outer
// call on this goroutine handed in — else nil.
func (m *Marshal) projectedVal(rv reflect.Value) runtime.Value {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	var p uintptr
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return nil
	}
	p = rv.Pointer()
	gid := m.callGid()
	if gid == 0 {
		return nil
	}
	st, ok := callScopes.Load(gid)
	if !ok {
		return nil
	}
	s := st.(*callScopeStack)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.scopes) - 1; i >= 0; i-- {
		if v, ok := s.scopes[i][p]; ok {
			return v
		}
	}
	return nil
}
