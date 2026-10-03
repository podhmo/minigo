// inspect.go holds the helpers that speak inspect view types —
// method-set queries the script's inference rules phrase directly.
// The pure-text counterparts live in scanx.go.
package scanx

import (
	"github.com/podhmo/minigo/inspect"
)

// HasMethod reports whether a concrete type's method set carries a
// nullary method with the given result types — scanx.HasMethod(d,
// "Discriminator", "string") matches `func (T) Discriminator() string`
// (either receiver form, declared or promoted through an embed) but
// not `func (T) Discriminator() int`. Interface decls answer false —
// their method set is their own specs, which is a different question
// (RequiresMethod asks it).
func HasMethod(d *inspect.Decl, name string, results ...string) bool {
	if inspect.Kind(d) != "type" {
		return false
	}
	def := inspect.Def(d)
	if def == nil || def.Kind == "InterfaceType" {
		return false
	}
	for _, m := range inspect.MethodSet(d) {
		if m.Name != name {
			continue
		}
		sig := m.Sig
		if sig == nil {
			continue
		}
		if len(sig.ParamFields()) != 0 || len(sig.ResultFields()) != len(results) {
			continue
		}
		ok := true
		for i, want := range results {
			if sig.ResultFields()[i].Type.Text != want {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// RequiresMethod reports whether an interface type lists a method spec
// with the given name and signature spelling — scanx.RequiresMethod(d,
// "Discriminator", "func() string") matches `Discriminator() string`
// among the type's members.
func RequiresMethod(d *inspect.Decl, name, sig string) bool {
	if inspect.Kind(d) != "type" {
		return false
	}
	def := inspect.Def(d)
	if def == nil || def.Kind != "InterfaceType" {
		return false
	}
	for _, req := range inspect.MReqs(d) {
		if len(req.Names) > 0 && req.Names[0] == name && req.Type.Text == sig {
			return true
		}
	}
	return false
}
