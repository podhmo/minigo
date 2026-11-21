// callhost.go is the reflect call path that invokes host functions with
// script arguments: arity and variadic handling, argument marshal,
// parking-aware joins for blocking host methods, and the write-back that
// propagates callee mutations into script slices and pointer cells.
package hostbridge

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

// CallHooks supplies the VM operations the host-call path needs —
// caller binding, park bookkeeping, process-lifetime ops and the unwind
// panic — so the reflect-driven call can live here without importing vm.
type CallHooks struct {
	// BindCaller binds vc to its process when it is a minigo caller.
	BindCaller func(runtime.VMCaller) runtime.VMCaller
	// Parks reports whether the named host method can park indefinitely,
	// and its wake picture (a parked op that can time out is wakeable).
	Parks func(name string) (wakeable, parks bool)
	// ProcDone is the caller's proc-done channel, nil without a proc.
	ProcDone func(vc runtime.CallerRef) <-chan struct{}
	// ProcOf is the caller's current proc, nil for foreign callers.
	ProcOf func(vc runtime.CallerRef) *process.Proc
	// Park marks the goroutine blocked around a blocking host join.
	Park func(vc runtime.CallerRef, w process.Wake) func()
	// Exit returns the value to panic with when the process dies.
	Exit func() any
	// MaskInt narrows a round-tripped int to the typedef's width.
	MaskInt func(iv int64, td *runtime.TypeDef) int64
	// Materialize resolves a UConst argument.
	Materialize func(u *runtime.UConst) (runtime.Value, error)
	// GoroutineID is the running goroutine's id for WatchCallFrom.
	GoroutineID func() int64
}

type argBack struct {
	arg runtime.Value
	rv  reflect.Value
	// maps records each map element's identity before the call when rv
	// is a slice/array of maps: an element the callee left alone is the
	// host copy of the caller's script map, one it replaced is new data.
	maps []uintptr
	// mapSnaps holds a pre-call clone of each map element (parallel to
	// maps), and snap holds the clone of a top-level map arg — the
	// three-way-diff base for write-back so a callback's mid-call
	// writes survive.
	snap     *runtime.Map
	mapSnaps []*runtime.Map
}

// mapElemPtrs snapshots the map identities of a slice/array arg's
// elements (nil for any other shape); mapElemSnaps clones the
// corresponding script map elements as the write-back diff base.
func mapElemPtrs(rv reflect.Value) []uintptr {
	if rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || rv.Type().Elem().Kind() != reflect.Map {
		return nil
	}
	ps := make([]uintptr, rv.Len())
	for i := range ps {
		ps[i] = rv.Index(i).Pointer()
	}
	return ps
}

// argBackOf builds the write-back record for one marshaled argument:
// map identities (and pre-call clones) of slice elements, plus a clone
// of a top-level map arg.
func (mb *Marshal) argBackOf(a runtime.Value, rv reflect.Value) argBack {
	wb := argBack{arg: a, rv: rv, maps: mapElemPtrs(rv), mapSnaps: mapElemSnaps(a, rv)}
	if sm := mapArg(a); sm != nil {
		wb.snap = sm.Clone()
	}
	return wb
}

// mapElemSnaps clones each script-map element of a slice/array arg
// (parallel to mapElemPtrs; nil for any other shape).
func mapElemSnaps(a runtime.Value, rv reflect.Value) []*runtime.Map {
	ss := sliceArg(a)
	if ss == nil {
		return nil
	}
	rv2 := rv
	if rv2.Kind() == reflect.Interface {
		rv2 = rv2.Elem()
	}
	if !rv2.IsValid() || rv2.Type().Elem().Kind() != reflect.Map {
		return nil
	}
	snaps := make([]*runtime.Map, rv2.Len())
	for i := range snaps {
		if i < len(ss.Elems) {
			if sm, ok := runtime.Unwrap(ss.Elems[i]).(*runtime.Map); ok {
				snaps[i] = sm.Clone()
			}
		}
	}
	return snaps
}

// scalarPayload is a script scalar's payload: the value itself, or a
// Named's payload.
func scalarPayload(v runtime.Value) runtime.Value {
	if n, ok := v.(*runtime.Named); ok {
		return n.V
	}
	return v
}

// unchangedScalar reports whether the host element ev still holds the
// basic value the script element old carried — the common case after a
// read-only host call, which then skips the copy-back (no boxing per
// element, and old keeps its declared tag).
func unchangedScalar(old runtime.Value, ev reflect.Value) bool {
	p := scalarPayload(old)
	switch k := ev.Kind(); {
	case k >= reflect.Int && k <= reflect.Int64:
		n, ok := p.(int64)
		return ok && n == ev.Int()
	case k >= reflect.Uint && k <= reflect.Uintptr:
		n, ok := p.(int64)
		return ok && uint64(n) == ev.Uint()
	case k == reflect.Float32 || k == reflect.Float64:
		f, ok := p.(float64)
		return ok && f == ev.Float()
	case k == reflect.String:
		s, ok := p.(string)
		return ok && s == ev.String()
	case k == reflect.Bool:
		b, ok := p.(bool)
		return ok && b == ev.Bool()
	}
	return false
}

// retagElem keeps a script slice element's declared type across the
// copy-back after a host call: the host sees `type U8 uint8` elements as
// plain uint8, and the value coming back carries that basic tag. When
// the old element was tagged and the new payload has the same Go shape,
// the old tag wins; anything else takes the host value as is.
func retagElem(old, nv runtime.Value) runtime.Value {
	on, ok := old.(*runtime.Named)
	if !ok || on.Typ == nil {
		return nv
	}
	p := runtime.Unwrap(nv)
	if reflect.TypeOf(p) != reflect.TypeOf(runtime.Unwrap(on.V)) {
		return nv
	}
	if nn, ok := nv.(*runtime.Named); ok && nn.Typ == on.Typ {
		return nv
	}
	return runtime.Tag(on.Typ, p)
}

// mapArg resolves an argument to the script map it carries, through
// Named tags and one level of pointer-like deref.
func mapArg(a runtime.Value) *runtime.Map {
	u := runtime.Unwrap(a)
	if dv, ok := runtime.Deref(u); ok {
		u = runtime.Unwrap(dv)
	}
	m, _ := u.(*runtime.Map)
	return m
}

// sliceArg resolves an argument to the script slice it carries, through
// Named tags and one level of pointer-like deref.
func sliceArg(a runtime.Value) *runtime.Slice {
	u := runtime.Unwrap(a)
	if dv, ok := runtime.Deref(u); ok {
		u = runtime.Unwrap(dv)
	}
	s, _ := u.(*runtime.Slice)
	return s
}

// refArg resolves an argument to the pointer-like node (Cell/FieldRef/
// IndexRef) nearest its leaf value — the write-back target for a
// pointer crossed by address — plus the leaf it resolved to.
func refArg(a runtime.Value) (ref runtime.Value, leaf runtime.Value) {
	u := a
	var seen map[runtime.Value]bool
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		switch u.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if _, isDR := u.(*runtime.DerefRef); !isDR {
				ref = u
			}
			// a cyclic pointer (x := any(nil); x = &x) resolves to
			// itself forever — stop on a revisit.
			if seen == nil {
				seen = map[runtime.Value]bool{u: true}
			} else if seen[u] {
				return ref, u
			} else {
				seen[u] = true
			}
		case *runtime.GoValue:
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			return ref, u
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	return ref, u
}

// callReflectFunc invokes a host func reflect.Value with script args:
// arity checks, variadic/CallSlice handling, and result marshaling —
// shared by host method values and callable GoValue funcs.
func (mb *Marshal) CallReflectFunc(h *CallHooks, name string, m reflect.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	vc = h.BindCaller(vc)
	mt := m.Type()
	nin := mt.NumIn()
	if !mt.IsVariadic() && len(args) != nin {
		return nil, fmt.Errorf("%s needs %d args, got %d", name, nin, len(args))
	}
	if mt.IsVariadic() && len(args) < nin-1 {
		return nil, fmt.Errorf("%s needs at least %d args, got %d", name, nin-1, len(args))
	}
	// a trailing slice assignable to the variadic parameter calls
	// through CallSlice — `f(xs...)`-style forwarding on natives.
	in := make([]reflect.Value, len(args))
	var backs []argBack
	useSlice := false
	for i, a := range args {
		pt := mt.In(min(i, nin-1))
		if mt.IsVariadic() && i >= nin-1 {
			if i == nin-1 && len(args) == nin {
				if rv, err := mb.ToReflect(a, pt, vc); err == nil {
					in[i] = rv
					useSlice = true
					backs = append(backs, mb.argBackOf(a, rv))
					continue
				}
			}
			pt = pt.Elem()
		}
		rv, err := mb.ToReflect(a, pt, vc)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
		}
		in[i] = rv
		backs = append(backs, mb.argBackOf(a, rv))
	}
	var out []reflect.Value
	watched := false
	wakeable := false
	if w, parks := h.Parks(name); parks {
		if done := h.ProcDone(vc); done != nil {
			watched = true
			wakeable = w
			// a method that can park indefinitely must not outlive its
			// process: run it on a helper goroutine and select on
			// proc.done, so a sibling's death unwinds this frame with
			// procExit instead of hanging the process (mu held by a
			// dead goroutine never releases its Lock waiters). The
			// helper keeps running the real call — a leaked goroutine,
			// the same documented limit as any host park.
			type callRes struct {
				out []reflect.Value
				p   any
			}
			resCh := make(chan callRes, 1)
			p := h.ProcOf(vc)
			go func() {
				// register before m.Call can invoke a script callback:
				// a Call arriving from this goroutine re-enters the VM
				// synchronously, so its panic propagates back through the
				// join rather than failing the process. p is captured
				// before the goroutine starts — it may already be dead,
				// which marking is harmless for; a nil receiver is not.
				if p != nil {
					defer p.WatchCallFrom(h.GoroutineID())()
				}
				defer func() {
					if r := recover(); r != nil {
						resCh <- callRes{p: r}
					}
				}()
				if useSlice {
					resCh <- callRes{out: m.CallSlice(in)}
				} else {
					resCh <- callRes{out: m.Call(in)}
				}
			}()
			// the join can park indefinitely — wakeable only when the
			// call itself can time out (see blockingHostMethods).
			unpark := h.Park(vc, process.Wake{Misc: wakeable})
			defer unpark()
			select {
			case <-done:
				panic(h.Exit())
			case r := <-resCh:
				unpark()
				if r.p != nil {
					panic(r.p)
				}
				out = r.out
			}
		}
	}
	if !watched {
		if useSlice {
			out = m.CallSlice(in)
		} else {
			out = m.Call(in)
		}
	}
	// callee writes propagate back: a script slice crossed as a fresh
	// host slice shares nothing (Reader.Read's buffer), a script map
	// crossed as a fresh host map needs its diff applied, and an
	// addressable arg crossed as a fresh host pointer (a *T param or an
	// interface the pointer implements) needs its pointee mirrored.
	for _, wb := range backs {
		handled := false
		if sm := mapArg(wb.arg); sm != nil {
			rv := wb.rv
			if rv.Kind() == reflect.Interface {
				rv = rv.Elem()
			}
			if rv.IsValid() && rv.Kind() == reflect.Map {
				snap := wb.snap
				if snap == nil {
					snap = &runtime.Map{}
				}
				mb.mapWriteBack(h, vc, sm, snap, rv, map[writeBackEdge]bool{})
				continue
			}
		}
		if ss := sliceArg(wb.arg); ss != nil {
			rv := wb.rv
			if rv.Kind() == reflect.Interface {
				rv = rv.Elem()
			}
			if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
				// a []byte arg commits in one pass — GoValueOf per
				// element would box and tag every byte again (#362).
				// Unaddressable arrays can't view as Bytes — they keep
				// the generic element loop.
				if rv.Type().Elem().Kind() == reflect.Uint8 && (rv.Kind() == reflect.Slice || rv.CanAddr()) {
					bs := rv.Bytes()
					for i := 0; i < len(bs) && i < len(ss.Elems); i++ {
						if p, ok := scalarPayload(ss.Elems[i]).(int64); ok && p == int64(bs[i]) {
							continue // unchanged: keep the element (and its tag)
						}
						ss.Elems[i] = retagElem(ss.Elems[i], runtime.ByteValue(bs[i]))
					}
					handled = true
					continue
				}
				for i := 0; i < rv.Len() && i < len(ss.Elems); i++ {
					ev := rv.Index(i)
					if unchangedScalar(ss.Elems[i], ev) {
						continue // keep the element (and its tag) as is
					}
					if ev.Kind() == reflect.Interface && !ev.IsNil() {
						if k := ev.Elem().Kind(); k == reflect.Slice || k == reflect.Map {
							// an `any` element holding a container is the
							// marshaled copy of a script container, which
							// may be this very slice: keep the script
							// element rather than rebuilding (and looping
							// on) the copy.
							continue
						}
					}
					if ev.Kind() == reflect.Map {
						if _, ok := runtime.Unwrap(ss.Elems[i]).(*runtime.Map); ok {
							// a map element crossed as a host copy: left
							// alone, the script map is still the caller's
							// value — swapping in the copy's GoValue box
							// would break index/assign on it
							// (`rows[0]["a"]`). A map the callee put in its
							// place lands as a script map, not a box.
							if i < len(wb.maps) && wb.maps[i] == ev.Pointer() {
								// the copy is still in place but the
								// callee may have written into it —
								// apply the diff to the script map.
								isnap := &runtime.Map{}
								if i < len(wb.mapSnaps) && wb.mapSnaps[i] != nil {
									isnap = wb.mapSnaps[i]
								}
								mb.mapWriteBack(h, vc, runtime.Unwrap(ss.Elems[i]).(*runtime.Map), isnap, ev, map[writeBackEdge]bool{})
								continue
							}
							ss.Elems[i] = retagElem(ss.Elems[i], SnapshotOf(ev.Interface()))
							continue
						}
					}
					ss.Elems[i] = retagElem(ss.Elems[i], GoValueOf(ev))
				}
				handled = true
			}
		}
		if handled {
			continue
		}
		ref, leaf := refArg(wb.arg)
		if ref == nil || wb.rv.Kind() != reflect.Pointer {
			continue
		}
		// the arg's own pointer crossing directly isn't a temp —
		// copying its pointee back would un-box the cell.
		if gv, ok := leaf.(*runtime.GoValue); ok {
			if pv := reflect.ValueOf(gv.V); pv.IsValid() && pv.Kind() == reflect.Pointer && pv.Pointer() == wb.rv.Pointer() {
				continue
			}
		}
		val := GoValueOf(wb.rv.Elem())
		if cell, ok := ref.(*runtime.Cell); ok {
			if n, ok := cell.Elem.(*runtime.Named); ok {
				// a declared cell keeps its tag through the round trip
				vv := runtime.Unwrap(val)
				if iv, ok := vv.(int64); ok {
					vv = h.MaskInt(iv, n.Typ)
				}
				val = runtime.Tag(n.Typ, vv)
			}
		}
		runtime.SetRef(ref, val)
	}
	return goResults(out)
}

// mapWriteBack applies the diff between the host map copy a callee
// received and the script map it was marshaled from. The diff is
// three-way — snap is the script map cloned before the call — so a
// script callback that mutated the map while the host call ran keeps
// its writes: an entry the callee left alone is identified against the
// snapshot, not the live map, and only keys present in the snapshot and
// absent from the copy delete (a callback-added key is never one).
// A nested map value recurses so in-place writes inside `m["row"]["k"]`
// reach the script map's own entry (replacing it would detach a
// `row := m["row"]` alias — gc maps share); seen stops the recursion on
// a self-referential map. Keys compare at the script-canonical level —
// the host copy's key spelling (int vs int64) cannot make a live key
// look absent.
// writeBackEdge keys the map-write-back recursion guard: one edge per
// (host copy, script map) pair so aliasing one host map under two
// script maps still writes back to each.
type writeBackEdge struct {
	h uintptr
	s *runtime.Map
}

func (mb *Marshal) mapWriteBack(h *CallHooks, vc runtime.VMCaller, sm, snap *runtime.Map, rv reflect.Value, seen map[writeBackEdge]bool) {
	if rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Map {
		return
	}
	// seen keys the (host map, script map) pair, not the host map
	// alone: one host map aliased under two existing script maps must
	// write back into both.
	edge := writeBackEdge{h: rv.Pointer(), s: sm}
	if edge.h != 0 {
		if seen[edge] {
			return
		}
		seen[edge] = true
	}
	canonical := func(k runtime.Value) runtime.Value {
		if sm.IfaceKey {
			return runtime.IfaceKeyOf(k)
		}
		return runtime.CanonicalKey(k)
	}
	alive := map[runtime.Value]bool{}
	it := rv.MapRange()
	for it.Next() {
		sk := GoSlotValueOf(it.Key())
		alive[canonical(sk)] = true
		ev := it.Value()
		sv, had := snap.Get(sk)
		if had && unchangedScalar(sv, ev) {
			continue
		}
		cur, has := sm.Get(sk)
		if ev.Kind() == reflect.Map && ev.Pointer() != 0 {
			if inner, ok := runtime.Unwrap(cur).(*runtime.Map); ok {
				isnap, _ := runtime.Unwrap(sv).(*runtime.Map)
				if isnap == nil {
					isnap = &runtime.Map{}
				}
				mb.mapWriteBack(h, vc, inner, isnap, ev, seen)
				continue
			}
		}
		if has {
			sm.Insert(sk, retagElem(cur, GoSlotValueOf(ev)))
		} else {
			sm.Insert(sk, GoSlotValueOf(ev))
		}
	}
	// a snapshot key with no host entry was deleted by the callee.
	disps, cks := snap.SnapshotKeys()
	for i, ck := range cks {
		if !alive[ck] {
			sm.Delete(disps[i])
		}
	}
}

// goResults converts a host call's results to script values: no result
// is NIL, one unboxes via GoValueOf, several become a Tuple.
func goResults(out []reflect.Value) (runtime.Value, error) {
	switch len(out) {
	case 0:
		return runtime.NIL, nil
	case 1:
		return GoSlotValueOf(out[0]), nil
	default:
		el := make([]runtime.Value, len(out))
		for i, o := range out {
			el[i] = GoSlotValueOf(o)
		}
		return &runtime.Tuple{Elems: el}, nil
	}
}

// callOpaqueStore calls a container method (OpaqueContainers) whose
// `any` parameters are pure storage: each `any` arg crosses inside the
// interface box — what gc's boxing stores — so the struct's host
// projection, and the niladic method calls it runs at marshal time,
// never happen. A position gc hashes or compares as a key (a sync.Map
// key, a context key, CompareAndSwap's comparand) crosses in its
// canonical key form: equal script content hits and unhashable payloads
// panic with a hash-type error, exactly where gc runs the check. Other
// params marshal as usual, and no arg joins the write-back pass: a
// store shares nothing addressable.
func (mb *Marshal) CallOpaqueStore(h *CallHooks, recv, name string, m reflect.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	mt := m.Type()
	nin := mt.NumIn()
	if !mt.IsVariadic() && len(args) != nin {
		return nil, fmt.Errorf("%s needs %d args, got %d", name, nin, len(args))
	}
	if mt.IsVariadic() && len(args) < nin-1 {
		return nil, fmt.Errorf("%s needs at least %d args, got %d", name, nin-1, len(args))
	}
	in := make([]reflect.Value, len(args))
	for i, a := range args {
		pt := mt.In(min(i, nin-1))
		if mt.IsVariadic() && i >= nin-1 {
			pt = pt.Elem()
		}
		if pt == anyType {
			rv, err := OpaqueStoreArg(recv, name, i, a, h.Materialize)
			if err != nil {
				return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
			}
			in[i] = rv
			continue
		}
		rv, err := mb.ToReflect(a, pt, vc)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
		}
		in[i] = rv
	}
	return goResults(m.Call(in))
}
