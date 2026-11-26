package hostbridge

import (
	"go/ast"
	"reflect"
	"sort"

	"github.com/podhmo/minigo/runtime"
)

// structProjectionKey separates a struct value's projection from a
// pointer's projection: Go's method set depends on which one crossed the
// interface, so they must not fold into one cached host object.
type structProjectionKey struct {
	s         *runtime.Struct
	valueRecv bool
}

// structProjectionPlan is the reflect shape and member mapping of one
// script struct's host projection.
type structProjectionPlan struct {
	typ       reflect.Type
	fidx      []int
	mnames    []string
	pnames    []string
	pitems    map[string]promMember
	valueRecv bool
}

// structDataHost marshals a script struct (or a pointer to one) for a
// host `any` parameter as a *struct{...} fabricated by
// reflect.StructOf: the type's exported fields plus its exported
// methods as `any` fields, so a member the type does not offer reports
// like gc — "can't evaluate field X in type ..." — where a map
// projection would read the miss as an absent key and print
// <no value>.
//
// A field carries its member deepHost'd; a method matching the
// template-callable shape (niladic; 1 result, or 2 with error last) is
// invoked at marshal time and its result stands in — text/template's
// evalField does not call what it finds, so Go's field-access call is
// replayed here. The marshal's budget caps these calls; on exhaustion,
// a pointer-receiver method on a value receiver, and any non-matching
// shape, the bound function value stays verbatim. A call that fails —
// panic, or a non-nil error result — leaves the field nil, printing
// <no value> as the dropped map entry did before. Members promoted
// through embedded fields join the projection by promotedData — gc
// resolves them through the interface boundary — and unexported
// members are absent, matching Go's can't-evaluate and
// unexported-field errors as a missing field.
func structDataHost(recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller, h *HostMarshal) any {
	_, valueRecv := recv.(*runtime.Struct)
	key := structProjectionKey{s: s, valueRecv: valueRecv}
	if old, ok := h.Walk.Seen(key); ok {
		// A merge reuses the projection already handed to the host, but it
		// must not reuse stale field values: refresh the existing object in
		// place. A recursive reference reached while that same projection is
		// still being built is not a merge refresh.
		if !h.M.insideMerge() || h.filling[key] {
			return old
		}
		if h.refreshStructProjection(old, recv, s, vc) {
			return old
		}
		// Promoted members depend on embedded values: initializing a nil
		// embedded pointer can enlarge the projection's shape. The old
		// host object cannot grow fields, so fall through and mint the
		// current shape rather than returning a projection known to be
		// missing members.
	}
	plan := planStructProjection(recv, s, vc)
	out := reflect.New(plan.typ)
	h.Walk.Register(key, out.Interface())
	// Keep the receiver itself on the walk too: the weak structOrigs
	// round-trip is backed by the host call's strong ownership for the
	// duration of that call.
	h.Walk.Register(recv, out.Interface())
	RecordStructOrig(out.Interface(), recv)
	if h.filling == nil {
		h.filling = map[runtime.Value]bool{}
	}
	h.filling[key] = true
	defer delete(h.filling, key)
	fillStructProjection(out.Elem(), plan, recv, s, vc, h)
	return out.Interface()
}

// refreshStructProjection rewrites an existing projection from the
// script struct's current values. ok is false when the cached projection
// came from the other receiver shape (value vs pointer), whose method set
// is a different reflect type.
func (h *HostMarshal) refreshStructProjection(old any, recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller) bool {
	rv := reflect.ValueOf(old)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return false
	}
	plan := planStructProjection(recv, s, vc)
	ev := rv.Elem()
	if ev.Type() != plan.typ {
		return false
	}
	if h.filling == nil {
		h.filling = map[runtime.Value]bool{}
	}
	_, valueRecv := recv.(*runtime.Struct)
	key := structProjectionKey{s: s, valueRecv: valueRecv}
	h.filling[key] = true
	defer delete(h.filling, key)
	fillStructProjection(ev, plan, recv, s, vc, h)
	return true
}

// planStructProjection computes the exported member shape structDataHost
// fabricates for recv. The plan is deterministic for one script value and
// receiver form, so a merge can tell whether a cached projection is
// compatible before refreshing it.
func planStructProjection(recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller) structProjectionPlan {
	td := s.Def
	var sfs []reflect.StructField
	var fidx []int
	var mnames []string
	declared := map[string]bool{}
	if td != nil {
		for i, name := range td.Fields {
			if i < len(s.Fields) && ast.IsExported(name) {
				sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
				fidx = append(fidx, i)
				declared[name] = true
			}
		}
		for name := range td.Methods {
			if ast.IsExported(name) {
				mnames = append(mnames, name)
				declared[name] = true
			}
		}
	}
	sort.Strings(mnames)
	for _, name := range mnames {
		sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
	}
	// a value receiver excludes pointer receivers from the method set,
	// exactly as Go does — a *runtime.Struct recv is the struct value,
	// anything else is a pointer carrying its ref.
	_, valueRecv := recv.(*runtime.Struct)
	// members promoted through embedded fields resolve through the
	// interface boundary in gc — `{{.C.Hidden}}` on an embed-receiving
	// *T — so they join the projection as direct members.
	pnames, pitems := promotedData(s, td, vc, !valueRecv, declared)
	for _, name := range pnames {
		sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
	}
	if len(sfs) == 0 {
		// a zero-size struct projects to a shared address (reflect.New
		// of struct{} returns zerobase), so every empty-exported type
		// would collide in structOrigs and fold to the newest orig — a
		// blank field keeps each projection's pointer distinct without
		// adding a member template field syntax can reach.
		sfs = append(sfs, reflect.StructField{Name: "_", Type: reflect.TypeFor[int64](), PkgPath: "github.com/podhmo/minigo/vm"})
	}
	return structProjectionPlan{
		typ:       reflect.StructOf(sfs),
		fidx:      fidx,
		mnames:    mnames,
		pnames:    pnames,
		pitems:    pitems,
		valueRecv: valueRecv,
	}
}

// fillStructProjection writes one projection's exported fields, methods
// and promoted members. The writable fields are zeroed first so a method
// whose refresh fails reads as the same dropped entry a fresh projection
// would have.
func fillStructProjection(ev reflect.Value, plan structProjectionPlan, recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller, h *HostMarshal) {
	for i := 0; i < ev.NumField(); i++ {
		if fv := ev.Field(i); fv.CanSet() {
			fv.SetZero()
		}
	}
	for j, i := range plan.fidx {
		if fv := h.M.DeepHost(s.Fields[i], vc, h); fv != nil {
			ev.Field(j).Set(reflect.ValueOf(fv))
		}
	}
	td := s.Def
	if td == nil {
		return
	}
	for k, name := range plan.mnames {
		h.projMethod(ev.Field(len(plan.fidx)+k), td.Methods[name], recv, td.Methods[name].PtrRecv && plan.valueRecv, vc)
	}
	for j, name := range plan.pnames {
		fv := ev.Field(len(plan.fidx) + len(plan.mnames) + j)
		it := plan.pitems[name]
		switch {
		case it.hm:
			h.projHostMethod(fv, it.hv)
		case it.hv.IsValid():
			fv.Set(it.hv)
		case it.m != nil:
			h.projMethod(fv, it.m, it.recv, false, vc)
		default:
			if hv := h.M.DeepHost(it.fv, vc, h); hv != nil {
				fv.Set(reflect.ValueOf(hv))
			}
		}
	}
}
