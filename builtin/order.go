package builtin

import (
	"time"

	"github.com/podhmo/minigo/runtime"
)

// OrderScalar is the scalar the ordering helpers compare for v: tags
// peel, and a host named basic — boxed (a FileMode from os.Stat, a
// token.Token off inspect.AST) or raw (a time.Month or reflect.Kind a
// host call returned) — reads as its payload, so it orders among the
// source values of its type.
func OrderScalar(v runtime.Value) runtime.Value {
	v = runtime.Unwrap(v)
	switch x := v.(type) {
	case int64, float64, string, bool, time.Duration, *runtime.UConst, runtime.Nil:
		return v
	case *runtime.GoValue:
		if p, _, ok := runtime.HostNamedScalar(x); ok {
			return p
		}
		return v
	}
	if p, _, ok := runtime.HostNamedPayload(v); ok {
		return p
	}
	return v
}
