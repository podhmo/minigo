package vm

// The OpSelect/OpSelectCall per-site cache (P4-5b): member selection
// answers at type level once per (TypeID, method-set epoch) and replays
// the per-value part on each hit.

import (
	"go/ast"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// selSite answers an instruction's per-site cache or nil when the
// chunk carries none (hand-built code leaves C = -1).
func (v *VM) selSite(f *frame, ins bytecode.Instruction) *bytecode.GlobalSite {
	if ins.C >= 0 && int(ins.C) < len(f.ch.Sites) {
		return f.ch.Sites[ins.C]
	}
	return nil
}

// A selectSlot is one OpSelect site's cached member resolution (P4-5b):
// the type-level answer to `s.name`, replayable for any struct value
// whose typedef has the same TypeID while the method-set epoch holds.
// Value-level work — receiver copies, interface dispatch, field reads —
// always re-runs on a hit; only the resolution itself is cached.
// Resolutions that depend on the stored values (the promotedMemberEx
// value walk, undecided declared paths) record no descriptor and keep
// re-resolving.
type selectSlot struct {
	tid   runtime.TypeID
	epoch uint64
	name  string // the member name resolved — one site serves many
	res   *selRes
}

// selRes is a type-level member resolution. kind selects the replay:
type selRes struct {
	kind selKind
	idx  int               // selField: direct field index
	path []int             // selPath/selMemberSet: declared index path
	m    *runtime.Function // selMethod/selMemberSet: the method
	via  byte              // selMemberSet: 0 method, 1 iface dispatch, 2 host member
}

type selKind uint8

const (
	selField selKind = iota
	selMethod
	selPath
	selMemberSet
)

func (v *VM) structMember(f *frame, s *runtime.Struct, name string, recv runtime.Value, site *bytecode.GlobalSite) runtime.Value {
	def := s.Def
	if site != nil && v.H.TypeIDOf != nil && len(def.LocalTypes) == 0 {
		// read the epoch first: a method-set edit during resolution
		// leaves the stored slot stale on arrival, so the next read
		// resolves again.
		epoch := runtime.MethodSetEpoch()
		if tid := v.H.TypeIDOf(def); tid != "" && tid != "?" {
			// "?" marks an unidentifiable typedef — unrelated types
			// share it, so a slot may never key on it.
			if slot, _ := site.Cache.Load().(*selectSlot); slot != nil &&
				slot.tid == tid && slot.epoch == epoch && slot.name == name {
				if val, ok := v.applySelectSlot(f, s, name, recv, slot.res); ok {
					return val
				}
			}
			val, res := v.structMemberRes(f, s, name, recv)
			if res != nil {
				site.Cache.Store(&selectSlot{tid: tid, epoch: epoch, name: name, res: res})
			}
			return val
		}
	}
	val, _ := v.structMemberRes(f, s, name, recv)
	return val
}

// applySelectSlot replays a cached resolution on this call's value —
// the part of member selection that must run per access.
func (v *VM) applySelectSlot(f *frame, s *runtime.Struct, name string, recv runtime.Value, res *selRes) (runtime.Value, bool) {
	switch res.kind {
	case selField:
		return s.Fields[res.idx], true
	case selMethod:
		m := res.m
		if err := m.EnsureCompiled(); err != nil {
			f.trap("%s", err)
		}
		r := recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
		} else {
			// value receiver operates on a copy
			if dv, ok := runtime.Deref(r); ok {
				r = dv
			}
			r = valueCopy(r)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}, true
	case selPath:
		return v.fieldAtPath(f, s, res.path, name), true
	case selMemberSet:
		st, ix := v.fieldSlotAtPath(f, s, res.path, name)
		if st == nil {
			return nil, false
		}
		slot := st.Fields[ix]
		switch res.via {
		case 1: // interface-stored value: dispatch on the concrete value
			return v.selectMember(f, slot, name), true
		case 2: // host member: reselect on the unwrapped host value
			return v.selectMember(f, runtime.Unwrap(slot), name), true
		}
		return v.promotedValue(f, name, &promoted{m: res.m, recv: slot, mst: st, midx: ix}), true
	}
	return nil, false
}

// structMemberRes resolves s.name and reports the resolution as a
// replayable selRes when it is type-level — nil when the answer came
// from a value-level walk no cache can replay.
func (v *VM) structMemberRes(f *frame, s *runtime.Struct, name string, recv runtime.Value) (runtime.Value, *selRes) {
	def := s.Def
	// an unexported selector names (its package, name): a member of
	// the same name declared in another package is a different
	// identifier and does not answer (M4-2b).
	sp := selPkg(f)
	for i, fn := range def.Fields {
		if fn == name && selVisible(sp, def.Pkg, fn) {
			return s.Fields[i], &selRes{kind: selField, idx: i}
		}
	}
	if m, ok := def.Methods[name]; ok && selVisible(sp, def.Pkg, name) {
		if err := m.EnsureCompiled(); err != nil {
			f.trap("%s", err)
		}
		r := recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
		} else {
			// value receiver operates on a copy
			if dv, ok := runtime.Deref(r); ok {
				r = dv
			}
			r = valueCopy(r)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}, &selRes{kind: selMethod, m: m}
	}
	// promoted member: fields, methods, interface-stored values and
	// host members all compete at promotion depth, breadth-first like
	// Go (the same walk composite-literal keys use). The declaration
	// answers the field side first when the hook can: a unique field
	// path then only competes against non-field members at a
	// shallower-or-equal depth — the bounded member search owns those.
	// the declaration path has no package notion — skip it for a
	// cross-package unexported select rather than answer with an
	// invisible field.
	pkgOK := ast.IsExported(name) || sp == nil || def.Pkg == nil || sp == def.Pkg || sp.Path == def.Pkg.Path
	if pkgOK && v.H.FieldPath != nil && len(s.Def.LocalTypes) == 0 {
		if idx, found, decided := v.H.FieldPath(s.Def, name); decided {
			// the method set answers the non-field members — methods,
			// interface-dispatched and host members all locate their
			// receiver slot by declared index path, so the value-level
			// walk only re-runs for a member the set cannot name (a
			// promoted host FIELD is not in the method set).
			if mv, res, ok := v.promotedViaMethodSet(f, s, name, idx, found); ok {
				return mv, res
			}
			if found {
				if pr, ok := v.promotedMemberEx(f, s, name, true, false, len(idx)-1); ok {
					return v.promotedValue(f, name, pr), nil
				}
				// the path is declared, so its identity cannot vary per
				// value — only the value it lands on can (nil embeds
				// panic inside fieldAtPath on the hit too).
				return v.fieldAtPath(f, s, idx, name), &selRes{kind: selPath, path: idx}
			}
			if pr, ok := v.promotedMemberEx(f, s, name, true, false, -1); ok {
				return v.promotedValue(f, name, pr), nil
			}
			f.trap("%s has no field or method %s", def.Name, name)
			return nil, nil
		}
	}
	if pr, ok := v.promotedMember(f, s, name, true); ok {
		return v.promotedValue(f, name, pr), nil
	}
	f.trap("%s has no field or method %s", def.Name, name)
	return nil, nil
}
