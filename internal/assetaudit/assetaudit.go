// Package assetaudit fingerprints a package's shared assets — its
// parsed ASTs and declaration index — so the opt-in asset audit can
// detect mutation after the package was built. It is intentionally
// off by default: the fingerprint walks the whole AST, which the
// D-own spike measured at roughly 1-1.6 s per audit (a plain
// reload+call is 2-3 ms).
package assetaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"sort"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// HashPackage fingerprints the package's shared assets: every file's
// AST plus the declaration index. The digest is stable while the
// assets are unmodified and changes if any node, name, or decl
// position is rewritten.
func HashPackage(fset *token.FileSet, files []*syntax.File, ix *index.Index) (string, error) {
	h := sha256.New()
	for _, f := range files {
		io.WriteString(h, "\x00file\x00"+f.Name+"\x00")
		if err := ast.Fprint(h, fset, f.AST, nil); err != nil {
			return "", fmt.Errorf("assetaudit: print %s: %w", f.Name, err)
		}
	}
	if ix != nil {
		hashIndex(h, fset, ix)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashIndex writes a deterministic projection of the index: every
// decl's kind, name, spec position, and in-spec ordinals (Idx,
// NameIdx), in a canonical order — decl lists in source order, the
// lookup maps sorted by name, Types with each type's method set.
func hashIndex(h io.Writer, fset *token.FileSet, ix *index.Index) {
	pos := func(d *index.Decl) string {
		switch {
		case d == nil:
			return "!"
		case d.Func != nil:
			return fset.Position(d.Func.Pos()).String()
		case d.Spec != nil:
			return fset.Position(d.Spec.Pos()).String()
		default:
			return "?"
		}
	}
	writeDecl := func(d *index.Decl) {
		if d == nil {
			io.WriteString(h, "\x00decl\x00nil")
			return
		}
		fmt.Fprintf(h, "\x00decl\x00%d\x00%s\x00%s\x00%d\x00%d", d.Kind, d.Name, pos(d), d.Idx, d.NameIdx)
	}
	for _, d := range ix.Decls {
		writeDecl(d)
	}
	for _, d := range ix.Methods {
		writeDecl(d)
	}
	for _, d := range ix.Inits {
		writeDecl(d)
	}
	writeMap := func(tag string, m map[string]*index.Decl) {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			io.WriteString(h, "\x00"+tag+"\x00"+n)
			writeDecl(m[n])
		}
	}
	// a fixed tag order: ranging a Go map literal would make the digest
	// nondeterministic and flag an unmodified package as mutated.
	writeMap("funcs", ix.Funcs)
	writeMap("vars", ix.Vars)
	writeMap("consts", ix.Consts)

	types := make([]string, 0, len(ix.Types))
	for n := range ix.Types {
		types = append(types, n)
	}
	sort.Strings(types)
	for _, n := range types {
		tdi := ix.Types[n]
		io.WriteString(h, "\x00type\x00"+n)
		writeDecl(tdi.Decl)
		writeMap("methods", tdi.Methods)
	}

	// LocalGens is keyed by *ast.TypeSpec — order by spec position.
	gens := make([]*ast.TypeSpec, 0, len(ix.LocalGens))
	for ts := range ix.LocalGens {
		gens = append(gens, ts)
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].Pos() < gens[j].Pos() })
	for _, ts := range gens {
		fmt.Fprintf(h, "\x00localgen\x00%s\x00%d", fset.Position(ts.Pos()), ix.LocalGens[ts])
	}
}
