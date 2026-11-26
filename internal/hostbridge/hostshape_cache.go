package hostbridge

import (
	"reflect"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/podhmo/minigo/runtime"
)

var (
	hostShapes    sync.Map // weak.Pointer[runtime.TypeDef] -> reflect.Type (nil: no shape)
	hostShapeSize atomic.Int64
)

const hostShapeCap = 4096

// forgetHostShape drops a cached shape when its typedef dies. Typedefs
// are session-scoped, so a strong key would pin every session that ever
// marshaled a typed container for a host.
func forgetHostShape(key weak.Pointer[runtime.TypeDef]) {
	if _, ok := hostShapes.LoadAndDelete(key); ok {
		hostShapeSize.Add(-1)
	}
}

func (m *Marshal) cachedHostShape(vc runtime.VMCaller, td *runtime.TypeDef) reflect.Type {
	if td == nil {
		return nil
	}
	key := weak.Make(td)
	if rt, ok := hostShapes.Load(key); ok {
		t, _ := rt.(reflect.Type)
		return t
	}
	rt := m.hostShapeOf(vc, td, 0)
	// without a VM the type hooks are out of reach and the answer is a
	// provisional nil — keep it out of the cache.
	if m.HasVM(vc) && hostShapeSize.Load() < hostShapeCap {
		if _, loaded := hostShapes.LoadOrStore(key, rt); !loaded {
			hostShapeSize.Add(1)
			goruntime.AddCleanup(td, forgetHostShape, key)
		}
	}
	return rt
}
