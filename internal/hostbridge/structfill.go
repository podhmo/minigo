package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// fillStructProjection writes one projection's exported fields, methods
// and promoted members. Initial projection zeroes every writable field so
// a failed member reads as the same dropped entry a fresh projection would
// have. Refresh rewrites data members and expression-only methods.
// Methods with possible side effects keep their first result rather than
// duplicating writes; pure getters reflect post-callback data.
func fillStructProjection(ev reflect.Value, plan structProjectionPlan, recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller, h *HostMarshal, methods bool) {
	for i := 0; i < ev.NumField(); i++ {
		if !methods && i >= len(plan.fidx) {
			break // method and promoted slots handle their own refresh rule
		}
		if fv := ev.Field(i); fv.CanSet() {
			fv.SetZero()
		}
	}
	if !methods {
		base := len(plan.fidx) + len(plan.mnames)
		for j, name := range plan.pnames {
			it := plan.pitems[name]
			if it.hm || it.m != nil {
				continue
			}
			if fv := ev.Field(base + j); fv.CanSet() {
				fv.SetZero()
			}
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
		m := td.Methods[name]
		if methods || pureMethod(m) {
			h.projMethod(ev.Field(len(plan.fidx)+k), m, recv, m.PtrRecv && plan.valueRecv, vc)
		}
	}
	for j, name := range plan.pnames {
		fv := ev.Field(len(plan.fidx) + len(plan.mnames) + j)
		it := plan.pitems[name]
		switch {
		case it.hm:
			if methods {
				h.projHostMethod(fv, it.hv)
			}
		case it.hv.IsValid():
			fv.Set(it.hv)
		case it.m != nil:
			if methods || pureMethod(it.m) {
				h.projMethod(fv, it.m, it.recv, false, vc)
			}
		default:
			if hv := h.M.DeepHost(it.fv, vc, h); hv != nil {
				fv.Set(reflect.ValueOf(hv))
			}
		}
	}
}
