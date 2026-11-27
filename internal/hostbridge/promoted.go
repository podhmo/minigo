package hostbridge

import (
	"go/ast"
	"reflect"
	"sort"

	"github.com/podhmo/minigo/runtime"
)

// promMember is one member a projection promotes through embedding —
// a script field to DeepHost, a script method to bind (invoked eagerly
// when template-callable, like a declared method), or a host member
// already in host form: a field value, or a bound host method to call.
type promMember struct {
	fv   runtime.Value
	m    *runtime.Function
	recv runtime.Value
	hv   reflect.Value // a host member: field value or bound method
	hm   bool          // hv holds a bound method
}

// promEmbed is one embedded field reached while walking promoted
// members: a script-struct embed carries the inner struct (layout and
// deeper embeds) plus the typedef its methods promote from; a host
// embed carries the stored value's reflect.Value — pointer kept for a
// *T embed, where the method set and the nil check both need it.
type promEmbed struct {
	st   *runtime.Struct
	td   *runtime.TypeDef
	shp  *runtime.TypeDef // underlying typedef — the host type source
	recv runtime.Value
	hv   reflect.Value
	addr bool // a pointer link (or a pointer outer receiver) — ptr-receiver methods promote
}

// promotedData enumerates the members a script struct promotes through
// its embedded fields — the projection's missing half of gc's selector
// semantics (an interface-held *T answers .Hidden through embedding
// where an unprojected member list would report "can't evaluate
// field"). The shallowest embed depth wins a name; two distinct
// members claiming it there are ambiguous like a compile-time Go
// rejection and the name stays out. declared holds the type's own
// member names — they outrank every promoted spelling.
//
// Residuals: a nil embedded pointer's members stay out (gc panics on
// access — the projection reports them missing instead), and
// interface-typed embeds are leaves the walk does not enter.
func promotedData(s *runtime.Struct, td *runtime.TypeDef, vc runtime.VMCaller, outerPtr bool, declared map[string]bool) (names []string, items map[string]promMember) {
	items = map[string]promMember{}
	if td == nil || vc == nil {
		return nil, items
	}
	depth := map[string]int{}
	ambig := map[string]bool{}
	record := func(name string, d int, pm promMember) {
		if declared[name] || ambig[name] || !ast.IsExported(name) {
			return
		}
		if w, ok := depth[name]; ok {
			switch {
			case d > w:
				return
			case d == w:
				ambig[name] = true
				delete(items, name)
				delete(depth, name)
				return
			}
		}
		items[name], depth[name] = pm, d
	}
	// a value cycle through pointer embeds (a.B = &a) would loop the
	// walk — a struct already enqueued contributes nothing new.
	seen := map[*runtime.Struct]bool{s: true}
	level := expandEmbeds(s, td, outerPtr, vc, seen)
	for d := 1; len(level) > 0 && d < 32; d++ {
		var next []promEmbed
		for _, it := range level {
			if it.hv.IsValid() {
				recordHostEmbed(record, d, it)
				continue
			}
			st := it.st
			if st == nil || st.Def == nil {
				continue
			}
			for i, name := range st.Def.Fields {
				if i < len(st.Fields) {
					record(name, d, promMember{fv: st.Fields[i]})
				}
			}
			for name, m := range it.td.Methods {
				// a pointer receiver through an all-value path is not
				// in the method set — gc's lookup reports it missing,
				// so the name stays unprojected.
				if m.PtrRecv && !it.addr {
					continue
				}
				record(name, d, promMember{m: m, recv: it.recv})
			}
			next = append(next, expandEmbeds(st, it.shp, it.addr, vc, seen)...)
		}
		level = next
	}
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, items
}

// expandEmbeds reads parent's embedded fields — from's EmbedSpecs carry
// the type spellings, resolved in from's package scope — into the walk's
// next level. addr tells whether the walk reached the parent through a
// pointer: a *T embed's pointer-receiver methods promote into a value
// receiver, while a T embed's need the outer receiver addressable.
func expandEmbeds(parent *runtime.Struct, from *runtime.TypeDef, addr bool, vc runtime.VMCaller, seen map[*runtime.Struct]bool) []promEmbed {
	var out []promEmbed
	for k, spec := range from.EmbedSpecs {
		if k >= len(from.EmbedIdx) {
			continue
		}
		idx := from.EmbedIdx[k]
		if idx >= len(parent.Fields) {
			continue
		}
		texpr, ptr := spec, false
		if sx, ok := spec.(*ast.StarExpr); ok {
			ptr, texpr = true, sx.X
		}
		rtd, err := vc.ResolveType(from, texpr)
		if err != nil || rtd == nil {
			continue
		}
		utd := rtd
		if u, err := vc.Underlying(rtd); err == nil && u != nil {
			utd = u
		}
		stored := parent.Fields[idx]
		if utd.HostNew != nil {
			var hv reflect.Value
			if gv, ok := stored.(*runtime.GoValue); ok {
				hv = reflect.ValueOf(gv.V)
			}
			// a nil embedded pointer's members panic on access in gc —
			// leaving them out reports them missing instead.
			if !hv.IsValid() || (hv.Kind() == reflect.Pointer && hv.IsNil()) {
				continue
			}
			out = append(out, promEmbed{td: rtd, shp: utd, recv: stored, hv: hv, addr: addr || ptr})
			continue
		}
		if utd.Kind != runtime.KindStruct {
			// an interface-typed embed dispatches on its stored value —
			// a leaf the walk does not enter.
			continue
		}
		// a value embed stores the bare struct (the interface-boxing
		// model: a `T` field IS the struct value) — a defined-type embed
		// wraps it in Named; a *T embed needs the dereference.
		raw := runtime.Unwrap(stored)
		inner, isStruct := raw.(*runtime.Struct)
		if !isStruct {
			dv, ok := runtime.Deref(raw)
			if !ok {
				continue // nil embedded pointer — same residual as above
			}
			if inner, isStruct = dv.(*runtime.Struct); !isStruct {
				continue
			}
		}
		if seen[inner] {
			continue
		}
		seen[inner] = true
		recv := stored
		if !ptr {
			// methods promoted through a value embed bind a field ref:
			// pointer receivers reach the slot, value receivers deref
			// to the struct — either spelling reads the stored field.
			recv = &runtime.FieldRef{Base: parent, Name: from.Fields[idx]}
		}
		out = append(out, promEmbed{st: inner, td: rtd, shp: utd, recv: recv, addr: addr || ptr})
	}
	return out
}

// recordHostEmbed registers the promoted surface of a host-typed
// embedded field: exported fields (the host type's own promotions rank
// by their inner depth, so a member promoted inside the embed loses to
// a shallower script member) and methods — the boxed type's method set
// when the embed spells the host type, or, for a defined type over it
// (`type Wrap Base`), its script-declared methods plus the methods
// promoted through the host's anonymous fields.
func recordHostEmbed(record func(string, int, promMember), d int, it promEmbed) {
	hv := it.hv
	rt := hv.Type()
	bt := rt
	if bt.Kind() == reflect.Pointer {
		bt = bt.Elem()
	}
	base := it.td.HostNew != nil // the embed spells the host type itself
	if bt.Kind() == reflect.Struct {
		fv := hv
		if fv.Kind() == reflect.Pointer {
			fv = fv.Elem()
		}
		for _, f := range reflect.VisibleFields(bt) {
			if !f.IsExported() {
				continue
			}
			// walk the index path a link at a time: an anonymous field
			// the host type itself embeds can be a nil pointer
			// (template.Template's unparsed *Tree), where FieldByIndex
			// panics — the member reads missing, like the script
			// side's nil-link residual.
			v, ok := fv, true
			for _, ix := range f.Index {
				if v.Kind() == reflect.Pointer {
					if v.IsNil() {
						ok = false
						break
					}
					v = v.Elem()
				}
				v = v.Field(ix)
			}
			if !ok || !v.CanInterface() {
				continue
			}
			record(f.Name, d+len(f.Index)-1, promMember{hv: v})
		}
	}
	n := rt.NumMethod()
	for i := 0; i < n; i++ {
		name := rt.Method(i).Name
		var inner int
		if base {
			inner = HostMemberInner(it.shp.HostNew(), name, true)
		} else {
			inner = HostPromotedMethodInner(it.shp.HostNew(), name)
		}
		if inner < 0 {
			continue
		}
		record(name, d+inner, promMember{hv: hv.MethodByName(name), hm: true})
	}
	if !base {
		for name, m := range it.td.Methods {
			if m.PtrRecv && !it.addr {
				continue
			}
			record(name, d, promMember{m: m, recv: it.recv})
		}
	}
}

// projMethod fills one projection field with a method's host form: the
// BoundMethod verbatim when the member sits outside the callable shape
// (no caller, no marshal budget, a signature template cannot invoke,
// or a pointer receiver on a path the method set excludes), else its
// eagerly-run result — template's evalField reads fields, it does not
// call them, so Go's field-access call replays here. Inside a sync/pull
// merge the call defers to eval time instead: the merge's
// re-projection running it eagerly would have that call's own host
// calls re-enter the merge, compounding without bound (issue #1399).
func (h *HostMarshal) projMethod(fv reflect.Value, m *runtime.Function, recv runtime.Value, verbatim bool, vc runtime.VMCaller) {
	r := recv
	if !m.PtrRecv {
		if dv, ok := runtime.Deref(r); ok {
			r = dv
		}
	}
	bm := &runtime.BoundMethod{Recv: r, Fn: m}
	nout := h.M.TemplateCallableNout(m)
	if vc == nil || h.Budget <= 0 || nout == 0 || verbatim {
		fv.Set(reflect.ValueOf(bm))
		return
	}
	if h.M.insideMerge() {
		fv.Set(h.lazyMethod(bm, vc, nout))
		return
	}
	h.Budget--
	res, err := vc.Call(bm, nil)
	h.M.noteImpureMethodCallback(m)
	if err != nil {
		return
	}
	if nout == 2 {
		t, ok := res.(*runtime.Tuple)
		if !ok || len(t.Elems) != 2 || !nilishHost(t.Elems[1]) {
			return
		}
		res = t.Elems[0]
	}
	if hv := h.M.DeepHost(res, vc, h); hv != nil {
		fv.Set(reflect.ValueOf(hv))
	}
}

// projHostMethod fills a promoted host method's projection field the
// way projMethod does for a script method: the template-callable shape
// (niladic; one result, or two with error last) runs eagerly inside the
// marshal budget, and anything else binds the func value verbatim. A
// non-nil error result leaves the field nil, printing <no value> like
// the script side's dropped entry.
func (h *HostMarshal) projHostMethod(fv reflect.Value, mv reflect.Value) {
	mt := mv.Type()
	nout := 0
	switch {
	case mt.NumIn() == 0 && mt.NumOut() == 1:
		nout = 1
	case mt.NumIn() == 0 && mt.NumOut() == 2 && mt.Out(1).Implements(reflect.TypeFor[error]()):
		nout = 2
	}
	if h.Budget <= 0 || nout == 0 || h.M.insideMerge() {
		// the verbatim bound method already reads lazily — inside a
		// merge it is also the safe binding (issue #1399).
		fv.Set(mv)
		return
	}
	h.Budget--
	res := func() (out []reflect.Value) {
		// a panicking host method reads like the script side's failed
		// call — the field stays nil, printing <no value>, instead of
		// aborting the marshal (and the whole template execution).
		defer func() {
			if recover() != nil {
				out = nil
			}
		}()
		return mv.Call(nil)
	}()
	if res == nil || (nout == 2 && !res[1].IsNil()) {
		return
	}
	fv.Set(res[0])
}
