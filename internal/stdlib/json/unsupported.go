package stdjson

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// jsonUnsupported stands in for a script func value (an iter.Seq from
// maps.Keys, a closure) in the host tree json.Marshal walks: its
// MarshalJSON fails with an unsupportedFunc that jsonUnsupportedOf turns
// into go's "json: unsupported type: T" — walking the builtin or closure
// struct instead would leak interpreter internals.
type jsonUnsupported struct{ e *unsupportedFunc }

// unsupportedFunc carries the script type of an unencodable func value:
// its host type when the binary has one (iter.Seq[string]), else its
// spelling.
type unsupportedFunc struct {
	rt   reflect.Type
	name string
}

// jsonFuncValue reports the stand-in for a script func value.
func jsonFuncValue(v any) (jsonUnsupported, bool) {
	n, _ := v.(*runtime.Named)
	inner := v
	if n != nil {
		inner = n.V
	}
	switch inner.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
	default:
		return jsonUnsupported{}, false
	}
	e := &unsupportedFunc{name: typeStrOf(v)}
	if n != nil {
		e.rt = runtime.HostIterSeqType(n.Typ)
	}
	return jsonUnsupported{e}, true
}

// jsonUnsupportedOf rewrites the marshaler error a jsonUnsupported
// stand-in raised into go's own *json.UnsupportedTypeError (or its text,
// when the type has no host equivalent).
func jsonUnsupportedOf(err error) error {
	if uve, ok := errors.AsType[*json.UnsupportedValueError](err); ok && strings.HasPrefix(uve.Str, jsonCyclePrefix) {
		// a jsonCycle marker raised gc's UnsupportedValueError inside a
		// MarshalerError — unwrap it so callers see gc's exact error.
		return uve
	}
	var ue *unsupportedFunc
	if !errors.As(err, &ue) {
		return err
	}
	if ue.rt != nil {
		return &json.UnsupportedTypeError{Type: ue.rt}
	}
	return ue
}

type jsonScriptErr struct {
	sv      runtime.Value
	method  string // "MarshalJSON" or "MarshalText"
	operand runtime.Value
}
