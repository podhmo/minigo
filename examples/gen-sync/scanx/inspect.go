// inspect.go holds the helpers that speak inspect view types — the
// half of scanx a script needs to paper over what the Decl view does
// not expose (alias-ness, per-spec file positions).
// The pure-text counterparts live in scanx.go.
package scanx

import (
	"os"
	"strings"

	"github.com/podhmo/minigo/inspect"
)

// lineCache maps file path to its contents as first read. Decl
// positions refer to that snapshot; a run plans every file before
// writing any, so the cache never goes stale mid-scan.
var lineCache = map[string][]string{}

// LinesOf reads a file into lines, cached across calls.
func LinesOf(path string) []string {
	if ls, ok := lineCache[path]; ok {
		return ls
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	ls := strings.Split(string(data), "\n")
	lineCache[path] = ls
	return ls
}

// DeclLine returns the decl's own source line, read from the file its
// Pos names — which is not necessarily the file currently being synced.
func DeclLine(d *inspect.Decl) string {
	pos := inspect.Pos(d)
	n := PosLine(pos)
	ls := LinesOf(PosFile(pos))
	if n <= 0 || n > len(ls) {
		return ""
	}
	return ls[n-1]
}

// IsAlias reports whether a type decl spells `type X = ...` — aliases
// never earn directives of their own. The Decl view does not expose
// alias-ness, so the decl's own line is the source of truth.
func IsAlias(d *inspect.Decl) bool {
	return IsAliasLine(DeclLine(d))
}

// HasMethod reports whether the type declares a nullary method with the
// given result types — scanx.HasMethod(d, "Discriminator", "string")
// matches `func (T) Discriminator() string` (either receiver form) but
// not `func (T) Discriminator() int`.
func HasMethod(d *inspect.Decl, name string, results ...string) bool {
	if inspect.Kind(d) != "type" {
		return false
	}
	for _, m := range inspect.Methods(d) {
		if m.Name != name {
			continue
		}
		sig := inspect.Signature(m)
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
