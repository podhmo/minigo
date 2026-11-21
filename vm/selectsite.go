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
	ntd  *runtime.TypeDef  // selNamedMethod: the peeled typedef the receiver re-tags to
}

type selKind uint8

const (
	selField selKind = iota
	selMethod
	selPath
	selMemberSet
	selNamedMethod
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

// namedPayload peels a Named's payload through pointer derefs and nested
// tags — the value a field or host member resolves on (the same walk
// namedMemberRes does).
func namedPayload(n *runtime.Named) runtime.Value {
	sv := n.V
	if dv, ok := runtime.Deref(sv); ok {
		sv = dv
	}
	for {
		nn, isNamed := sv.(*runtime.Named)
		if !isNamed {
			break
		}
		sv = nn.V
		if dv, ok := runtime.Deref(sv); ok {
			sv = dv
		}
	}
	return sv
}

// applyNamedSlot replays a cached namedMember resolution on this call's
// value — the receiver bind and field read run per access.
func (v *VM) applyNamedSlot(f *frame, n *runtime.Named, name string, recv runtime.Value, res *selRes) (runtime.Value, bool) {
	switch res.kind {
	case selNamedMethod:
		m := res.m
		if err := m.EnsureCompiled(); err != nil {
			f.trap("%s", err)
		}
		r := recv
		if m.PtrRecv {
			r = v.ptrReceiver(r)
		} else if res.ntd != n.Typ {
			sv := n.V
			if dv, ok := runtime.Deref(sv); ok {
				sv = dv
			}
			r = valueCopy(runtime.Tag(res.ntd, sv))
		} else {
			r = valueCopy(n)
		}
		return &runtime.BoundMethod{Recv: r, Fn: m}, true
	case selField:
		if s, ok := namedPayload(n).(*runtime.Struct); ok && res.idx < len(s.Fields) {
			return s.Fields[res.idx], true
		}
	case selPath:
		if s, ok := namedPayload(n).(*runtime.Struct); ok {
			return v.fieldAtPath(f, s, res.path, name), true
		}
	}
	return nil, false
}

// instSiteSlot is one OpInstantiate site's memo: the result of the last
// (base, targs) pair it saw. Bases and args are pointer values, so ==
// compares identity — a redeclared generic or a changed arg type misses
// and falls back to the full key spell, never stale-hits. env/gen guard
// what pointer identity cannot see: instTypeKey resolves the generic's
// constraint names in its declaring package, and a REPL redeclaration of
// the constraint repoints those names — bumping the env generation —
// without changing base or arg identity.
type instSiteSlot struct {
	base  runtime.Value
	env   *runtime.Env
	gen   uint64
	targs []runtime.Value
	out   runtime.Value
}

func (s *instSiteSlot) match(base runtime.Value, targs []runtime.Value) bool {
	if s.base != base || len(s.targs) != len(targs) {
		return false
	}
	if s.env != nil && s.env.Gen() != s.gen {
		return false
	}
	for i, a := range targs {
		if s.targs[i] != a {
			return false
		}
	}
	return true
}

// instSiteEnv reports the env a memoized base's key can silently depend
// on: the declaring package's Globals, whose generation a constraint
// (re)declaration bumps. Bases that can never memoize — the index
// fallback's non-generic base — report ok false.
func instSiteEnv(base runtime.Value) (*runtime.Env, bool) {
	var pkg *runtime.Package
	switch g := base.(type) {
	case *runtime.Function:
		pkg = g.Pkg
	case *runtime.TypeDef:
		pkg = g.Pkg
	default:
		return nil, false
	}
	if pkg == nil {
		return nil, true
	}
	return pkg.Globals, true
}

// instSiteCacheable reports whether targs can memoize safely: every
// element must be a comparable pointer — a non-pointer arg (the index
// fallback's `a[i]` value args) may hold an uncomparable payload.
func instSiteCacheable(targs []runtime.Value) bool {
	for _, a := range targs {
		if _, ok := a.(*runtime.TypeDef); !ok {
			return false
		}
	}
	return true
}

// genCallSlot memoizes one OpCall site's inferred instance: on a hit
// — same callee, same spread, and every argument pinned on what
// inferKey would read — the inferKey spelling and the package-cache
// probe are skipped. Pinning is identity, not spelling: a const arg
// pins its value pointer (one site re-pushes the same UConst/Named
// object, which carries both payload and tag), a static-covered arg
// the resolved typedef, any other arg the value's own typedef.
type genCallSlot struct {
	fn     *runtime.Function
	spread *runtime.TypeDef
	tds    []*runtime.TypeDef
	consts []runtime.Value // per-arg pinned const values; nil elsewhere
	inst   *runtime.Function
}

func (g *genCallSlot) match(v *VM, fn *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) bool {
	if g.fn != fn || g.spread != spreadTd || len(g.tds) != len(args) {
		return false
	}
	for i, a := range args {
		if _, isC := constPayload(a); isC {
			if g.consts[i] != a {
				return false
			}
			continue
		}
		if st := staticAt(statics, i); st != nil {
			// statics resolve fresh each call — a REPL redeclared
			// global or upval typedef shows up here.
			if st != g.tds[i] {
				return false
			}
			continue
		}
		if v.argTypedef(a) != g.tds[i] {
			return false
		}
	}
	return true
}

// newGenCallSlot records the instance inference produced for this
// site's argument shape. An arg inferKey cannot pin — one that is
// neither const nor static nor typedef-able — leaves no slot: those
// calls always spell a key.
func newGenCallSlot(v *VM, fn, inst *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) *genCallSlot {
	g := &genCallSlot{fn: fn, spread: spreadTd, inst: inst,
		tds: make([]*runtime.TypeDef, len(args)), consts: make([]runtime.Value, len(args))}
	for i, a := range args {
		if _, isC := constPayload(a); isC {
			g.consts[i] = a
			continue
		}
		if st := staticAt(statics, i); st != nil {
			g.tds[i] = st
			continue
		}
		td := v.argTypedef(a)
		if td == nil {
			return nil
		}
		g.tds[i] = td
	}
	return g
}
