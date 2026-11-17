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
// decl's kind, name, and position, in a canonical order (decl lists in
// source order, the maps sorted by name).
func hashIndex(h io.Writer, fset *token.FileSet, ix *index.Index) {
	pos := func(d *index.Decl) string {
		switch {
		case d.Func != nil:
			return fset.Position(d.Func.Pos()).String()
		case d.Spec != nil:
			return fset.Position(d.Spec.Pos()).String()
		default:
			return "?"
		}
	}
	writeDecl := func(d *index.Decl) {
		fmt.Fprintf(h, "\x00decl\x00%d\x00%s\x00%s", d.Kind, d.Name, pos(d))
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
	for tag, m := range map[string]map[string]*index.Decl{
		"funcs":  ix.Funcs,
		"vars":   ix.Vars,
		"consts": ix.Consts,
	} {
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
}
