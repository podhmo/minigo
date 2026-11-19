package minigo

import (
	"go/ast"

	"github.com/podhmo/minigo/internal/typedef"
	"github.com/podhmo/minigo/runtime"
)

// ifaceReqs returns the required method set of an interface typedef:
// declared methods union the requirements of embedded interface elements.
// Constraint elements (~T, unions) are approximated away — satisfaction
// checks treat them as fulfilled.
func (e *Engine) ifaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	return e.ifaceReqSet(td), nil
}

// ifaceReqSet is ifaceReqs cached on td (callers treat the set as
// read-only). A set built while an embed failed to resolve — a
// constraint element (~T, unions) or a not-yet-loadable package — is
// not cached, so it is recomputed exactly as before.
func (e *Engine) ifaceReqSet(td *runtime.TypeDef) map[string]bool {
	if td == nil {
		return nil
	}
	if set, ok := td.CachedIfaceReqs(); ok {
		return set
	}
	complete := true
	set := e.ifaceReqsRec(td, map[*runtime.TypeDef]bool{}, &complete)
	if complete {
		td.SetIfaceReqs(set)
	}
	return set
}

func (e *Engine) ifaceReqsRec(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool, complete *bool) map[string]bool {
	td = e.td.PeelAliasTd(td)
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	set := map[string]bool{}
	for _, m := range td.MReqs {
		set[typedef.ReqKey(td.Pkg, m)] = true
	}
	for _, spec := range td.IEmbeds {
		sub, err := e.td.ResolveTypeRef(td, spec)
		if err != nil || sub == nil {
			*complete = false
			continue // constraint exprs (~T, |) don't resolve to typedefs
		}
		for m := range e.ifaceReqsRec(sub, seen, complete) {
			set[m] = true
		}
	}
	return set
}

// ifaceSig is an interface's required method together with the typedef
// context that declared it — its package, file imports and binds spell
// the signature.
type ifaceSig struct {
	decl *ast.FuncType
	ctx  *runtime.TypeDef
}

// ifaceSigFunc synthesizes the member Function an interface typedef's
// required method resolves to — the declared signature plus the typedef
// context (package, file imports, binds) that spells it.
func ifaceSigFunc(sig ifaceSig) *runtime.Function {
	return &runtime.Function{
		Decl: &ast.FuncDecl{Type: sig.decl},
		Pkg:  sig.ctx.Pkg, File: sig.ctx.File, Binds: sig.ctx.Binds,
	}
}

// ifaceSigFuncs maps an interface typedef's signature-bearing required
// methods to their synthesized member Functions — nil when it declares
// no signature (name-only requirements). The single conversion behind
// methodWalkU's interface arms and the IfaceSigs hook.
//
// The result is cached on td (callers treat it as read-only): every
// conversion to an interface runs this, and the stable shell pointers
// let SigMemo remember each requirement's comparison. A set built while
// an embed failed to resolve is not cached — it may be incomplete.
func (e *Engine) ifaceSigFuncs(td *runtime.TypeDef) map[string]*runtime.Function {
	if td == nil {
		return nil
	}
	if out, ok := td.CachedIfaceSigs(); ok {
		return out
	}
	complete := true
	sigs := e.ifaceSigsOf(td, map[*runtime.TypeDef]bool{}, &complete)
	var out map[string]*runtime.Function
	if len(sigs) > 0 {
		out = make(map[string]*runtime.Function, len(sigs))
		for name, sig := range sigs {
			out[name] = ifaceSigFunc(sig)
		}
	}
	if complete {
		td.SetIfaceSigs(out)
	}
	return out
}

// ifaceSigReqs implements the Hooks.IfaceSigs hook: the interface's
// required methods that carry a declared signature, as Function shells
// spelling that signature in their declaring typedef's context.
func (e *Engine) ifaceSigReqs(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	return e.ifaceSigFuncs(td), nil
}

// ifaceSigsOf maps an interface's required methods to their declared
// signatures — a name-only set can't distinguish F(int) from F(string),
// which interface satisfaction via reflect needs.
//
// complete turns false when an embedded element fails to resolve.
func (e *Engine) ifaceSigsOf(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool, complete *bool) map[string]ifaceSig {
	td = e.td.PeelAliasTd(td)
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	out := map[string]ifaceSig{}
	var it *ast.InterfaceType
	for _, x := range []ast.Expr{td.Anon, typedef.SpecType(td)} {
		if s, ok := x.(*ast.InterfaceType); ok {
			it = s
			break
		}
	}
	if it != nil {
		for _, m := range it.Methods.List {
			if len(m.Names) == 0 {
				continue
			}
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			for _, n := range m.Names {
				sig := ifaceSig{decl: ft, ctx: td}
				out[n.Name] = sig
				// unexported requirements compare under their
				// declaring package's key (M4-2b); ifaceTypEq looks
				// signatures up by the qualified requirement key.
				if k := typedef.ReqKey(td.Pkg, n.Name); k != n.Name {
					out[k] = sig
				}
			}
		}
	}
	for _, spec := range td.IEmbeds {
		emb, err := e.td.ResolveTypeRef(td, spec)
		if err != nil || emb == nil {
			*complete = false
			continue
		}
		for name, sig := range e.ifaceSigsOf(emb, seen, complete) {
			if _, dup := out[name]; !dup {
				out[name] = sig
			}
		}
	}
	return out
}
