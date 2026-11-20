package minireflect

import "reflect"

// callMethodExpr dispatches a method expression's Func call on the
// concrete receiver — a facade override (EngineStdout.Write routing
// through env.Out) survives where In(0) is the facade's API type. The
// receiver still has to satisfy the declared In(0): (*bytes.Buffer).Len
// on a *strings.Reader falls through so the plain Call path panics on
// the type mismatch like Go's, instead of dispatching Reader.Len.
func (v *RValue) callMethodExpr(mt reflect.Type, in []*RValue) ([]*RValue, bool) {
	if v.mexpr == "" || len(in) == 0 {
		return nil, false
	}
	r := in[0]
	if r == nil || !r.rv.IsValid() || !r.rv.CanInterface() {
		return nil, false
	}
	if _, err := toHost(r.ifaceVal(), mt.In(0), v.e); err != nil {
		return nil, false
	}
	dm := r.rv.MethodByName(v.mexpr)
	if !dm.IsValid() {
		return nil, false
	}
	rest := make([]reflect.Value, len(in)-1)
	for i, a := range in[1:] {
		j := i + 1
		var pt reflect.Type
		if mt.IsVariadic() && j >= mt.NumIn()-1 {
			pt = mt.In(mt.NumIn() - 1).Elem()
		} else {
			pt = mt.In(j)
		}
		rv, err := toHost(a.ifaceVal(), pt, v.e)
		if err != nil {
			trap("reflect.Value.Call: %s", err)
		}
		rest[i] = rv
	}
	out := dm.Call(rest)
	res := make([]*RValue, 0, len(out))
	for _, o := range out {
		res = append(res, v.e.wrapHost(v.vc, o))
	}
	return res, true
}
