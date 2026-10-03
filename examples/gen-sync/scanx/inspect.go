// inspect.go holds the helpers that speak inspect view types — the
// half of scanx a script needs to paper over what the Decl view does
// not expose (ValueSpec types, alias-ness, per-spec file positions).
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

// HasConstOfType reports whether some const spec in decls (usually the
// whole package's decls, so constants in sibling files count) declares
// the named type. Specs that omit the type inherit it from the block's
// first spec — the file is scanned upwards to the enclosing `const (`,
// which also means `Cadence Level = "4/4"; Beat` types Beat as Level,
// not as the queried name.
func HasConstOfType(decls []*inspect.Decl, name string) bool {
	for _, c := range decls {
		if inspect.Kind(c) != "const" {
			continue
		}
		pos := inspect.Pos(c)
		ls := LinesOf(PosFile(pos))
		n := PosLine(pos)
		if n <= 0 || n > len(ls) {
			continue
		}
		if SpecHasType(ls[n-1], name) {
			return true
		}
		if !strings.Contains(ls[n-1], "=") {
			// no `=` on the spec: it repeats the nearest spec above
			// that carries one — walk up inside the block and check
			// that line. Either way keep scanning decls, since other
			// blocks may still declare the name.
			for j := n - 2; j >= 0; j-- {
				t := strings.TrimSpace(ls[j])
				if strings.HasPrefix(t, "const") || t == ")" {
					break
				}
				if strings.Contains(t, "=") {
					if SpecHasType(ls[j], name) {
						return true
					}
					break
				}
			}
		}
	}
	return false
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
