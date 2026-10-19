// inspect.go holds the helpers that speak inspect view types —
// method-set queries the script's inference rules phrase directly.
// The pure-text counterparts live in scanx.go.

package scanx

import (
	"github.com/podhmo/minigo/inspect"
)

// MethodSet returns d's flattened method set, excluding methods declared
// in files marked foreign, including promoted methods from those files.
func MethodSet(d *inspect.Decl, foreign map[string]bool) []*inspect.Method {
	if inspect.Kind(d) != "type" {
		return nil
	}
	def := inspect.Def(d)
	if def == nil || def.Kind == "InterfaceType" {
		return nil
	}
	var out []*inspect.Method
	for _, m := range inspect.MethodSet(d) {
		if m.Decl != nil && foreign[m.Decl.File] {
			continue // the method itself was declared in a foreign file
		}
		if m.Via != nil && foreign[m.Via.File] {
			continue // promoted out of a decl that lives in a foreign file
		}
		out = append(out, m)
	}
	return out
}

// HasMethod reports whether a concrete type's method set carries a
// nullary method with the given result types — scanx.HasMethod(d,
// foreign, "Discriminator", "string") matches `func (T) Discriminator()
// string` (either receiver form, declared or promoted through an embed)
// but not `func (T) Discriminator() int`. Interface decls answer false —
// their method set is their own specs, which is a different question
// (RequiresMethod asks it). Members sourced from foreign-package files
// do not count — see MethodSet.
func HasMethod(d *inspect.Decl, foreign map[string]bool, name string, results ...string) bool {
	return MethodNamed(d, foreign, name, results...) != nil
}

// MethodNamed returns the member of d's method set that matches name
// and the given result types, or nil — the matched member, not just the
// fact of the match, so callers can say where the method came from
// (Decl for the declaring file, Via for the decl it promoted through).
func MethodNamed(d *inspect.Decl, foreign map[string]bool, name string, results ...string) *inspect.Method {
	for _, m := range MethodSet(d, foreign) {
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
			return m
		}
	}
	return nil
}

// RequiresMethod reports whether an interface directly declares name with
// signature sig; embedded interface methods are excluded. For example,
// RequiresMethod(d, "Discriminator", "func() string") matches a nullary
// method returning string.
func RequiresMethod(d *inspect.Decl, name, sig string) bool {
	if inspect.Kind(d) != "type" {
		return false
	}
	for _, req := range inspect.MReqs(d) {
		if len(req.Names) > 0 && req.Names[0] == name && req.Type.Text == sig {
			return true
		}
	}
	return false
}
