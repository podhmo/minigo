package minigo

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// memberTypOf resolves the typedef carried by package member name: the
// value's own tag when present, a host-scalar match against the
// package's typedefs (`time.Second` binds a bare int64 but time
// declares Duration), or — for source packages — the declaration's
// written or inferred type read in the declaring file's scope.
func (e *Engine) memberTypOf(p *runtime.Package, name string, depth int) (*runtime.TypeDef, error) {
	if depth > 8 {
		return nil, nil
	}
	m, err := p.Member(name, e.materialize)
	if err != nil {
		return nil, nil
	}
	if td := memberTyp(m); td != nil {
		return td, nil
	}
	if p.Globals != nil {
		if td := hostScalarTypedef(p, m); td != nil {
			return td, nil
		}
	}
	if p.Index == nil {
		return nil, nil
	}
	var d *index.Decl
	if c := p.Index.Consts[name]; c != nil {
		d = c
	} else if v := p.Index.Vars[name]; v != nil {
		d = v
	}
	if d == nil {
		return nil, nil
	}
	vs, ok := d.Spec.(*ast.ValueSpec)
	if !ok {
		return nil, nil
	}
	mctx := &runtime.TypeDef{Pkg: p, File: d.File}
	effType := vs.Type
	var val ast.Expr
	if len(vs.Values) == 0 {
		effType = d.InheritedType
		if d.NameIdx < len(d.Inherited) {
			val = d.Inherited[d.NameIdx]
		}
	} else if d.NameIdx < len(vs.Values) {
		val = vs.Values[d.NameIdx]
	}
	if effType != nil {
		return e.resolveTypeRef(mctx, effType)
	}
	if val == nil {
		return nil, nil
	}
	return e.valueTypDepth(mctx, val, depth+1)
}

// hostScalarTypedef finds the package typedef whose HostScalar Go type
// matches m's — bound packages store typedefs as globals and consts as
// raw host values (compile's hostScalarTypeDefOf counterpart).
func hostScalarTypedef(p *runtime.Package, m runtime.Value) *runtime.TypeDef {
	if gv, ok := m.(*runtime.GoValue); ok {
		m = gv.V
	}
	rt := reflect.TypeOf(m)
	if rt == nil {
		return nil
	}
	for _, n := range p.Globals.Names() {
		v, ok := p.Globals.Get(n)
		if !ok {
			continue
		}
		td, ok := v.(*runtime.TypeDef)
		if !ok || td.HostScalar == nil {
			continue
		}
		if reflect.TypeOf(td.HostScalar) == rt {
			return td
		}
	}
	return nil
}
