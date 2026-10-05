// complete.go — REPL completion, language-processor side.
//
// Complete derives candidates from the interpreter's own state: the
// scratch package's globals and index, file-scope imports, the builtin
// environment, and whatever a selector's base resolves to. There is
// deliberately no go/types pass — enumeration reuses the same machinery
// member dispatch uses at runtime (dispatch.go's method sets, embedded
// typedef resolution, host reflection), so candidates stay correct under
// redefinition, :pin grafting and lazy package materialization, the ways
// REPL state diverges from what a static analysis would believe.
//
// The evaluation policy is "minimal" in IPython's sense: base resolution
// is a pure AST walk — identifier lookup, field/method selection,
// integer indexing, composite-literal zeros — and never evaluates call
// expressions, never runs package init (EnsureReady is avoided; lazy
// packages stay Indexed). `f().` therefore yields no candidates where
// an eval-based completer would offer the result's members; the trade
// keeps TAB free of user-code side effects. Declared types stand in
// where runtime values are absent: `var s pkg.T` in an uninitialized
// package still completes through the decl's type expression.
package minigo

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Candidate is one completion suggestion.
type Candidate struct {
	Name   string // the completion text
	Kind   string // CandKeyword, CandBuiltin, CandFunc, ...
	Detail string // signature or declared-type rendering, when cheaply known
}

// Candidate kinds — loose buckets a UI can group or icon by.
const (
	CandKeyword = "keyword"
	CandBuiltin = "builtin"
	CandFunc    = "func"
	CandMethod  = "method"
	CandField   = "field"
	CandVar     = "var"
	CandConst   = "const"
	CandType    = "type"
	CandPackage = "package"
)

// goKeywords for bare-name completion.
var goKeywords = []string{
	"break", "case", "chan", "const", "continue", "default", "defer",
	"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
	"interface", "map", "package", "range", "return", "select",
	"struct", "switch", "type", "var",
}

// predeclared are the builtin identifiers that live outside the builtin
// env (nil/true/false are compiler literals, iota is const-scope only).
var predeclared = []string{"nil", "true", "false", "iota"}

// Complete returns the candidates for a REPL input fragment with the
// cursor at end of line. A `x.` or `x.pa` tail completes selector
// members of the base expression; an identifier tail completes bare
// names in scope; anything else completes all bare names (e.g. after
// `x := `). An `import "...` tail completes import paths (bound
// intrinsics, GOROOT stdlib, go.mod requires and the module's own
// packages, or `./`/`../`/`/abs` directory imports). Meta-command
// lines (`:`-prefixed) are not completed here — they belong to the
// front-end's own completer.
func (r *REPL) Complete(line string) []Candidate {
	_, cands := r.complete(line)
	return cands
}

// CompleteToken is Complete plus the byte offset where the replaceable
// token begins — every candidate splices over line[start:]. A line
// editor needs it to insert a candidate without re-lexing the tail.
func (r *REPL) CompleteToken(line string) (start int, cands []Candidate) {
	return r.complete(line)
}

func (r *REPL) complete(line string) (start int, cands []Candidate) {
	line = strings.TrimRight(line, " \t")
	if strings.HasPrefix(strings.TrimSpace(line), ":") {
		return len(line), nil
	}
	ctx := completeContext(line)
	if ctx.imp {
		return ctx.start, r.importCandidates(ctx.prefix)
	}
	c := &completer{r: r, seen: map[string]bool{}}
	var out []Candidate
	if ctx.selector {
		if v, ok := r.resolveBase(ctx.base); ok {
			c.members(v, &out, 0)
		}
	} else {
		c.scopeNames(&out)
	}
	if ctx.prefix != "" {
		keep := out[:0]
		for _, cand := range out {
			if strings.HasPrefix(cand.Name, ctx.prefix) {
				keep = append(keep, cand)
			}
		}
		out = keep
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return ctx.start, out
}

// completionCtx is the lexical tail of the line: "selector after a dot"
// (with the base expression's source), "inside an import string", or
// "bare identifier prefix".
type completionCtx struct {
	selector bool   // completing after `.`
	imp      bool   // completing inside an `import "..."` string
	base     string // source text of the selector's base expression
	prefix   string // the partial identifier (or import path) being typed
	start    int    // byte offset where the replaceable token begins
}

// completeContext tokenizes the line and classifies its tail. Only the
// last two tokens matter: `x.` → selector on x; `x.pa` → selector with
// prefix "pa"; `pa` → bare prefix "pa"; everything else → bare with an
// empty prefix (all in-scope names are offered, e.g. inside `f(`).
func completeContext(line string) completionCtx {
	fset := token.NewFileSet()
	f := fset.AddFile("repl-complete.go", -1, len(line))
	var s scanner.Scanner
	s.Init(f, []byte(line), nil, 0)
	var toks []struct {
		tok token.Token
		lit string
		off int
	}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		// a scanner-inserted semicolon (lit "\n") is not user input — drop
		// it so `x.pa` still sees the identifier as the tail.
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		toks = append(toks, struct {
			tok token.Token
			lit string
			off int
		}{tok, lit, f.Offset(pos)})
	}
	n := len(toks)
	if n == 0 {
		return completionCtx{start: len(line)}
	}
	last := toks[n-1]
	// `import "str` (also `import . "`, `import name "`, `import (`)
	// ends inside a string literal: complete import paths, not code.
	if last.tok == token.STRING && toks[0].tok == token.IMPORT {
		prefix := last.lit
		prefix = strings.TrimPrefix(prefix, `"`)
		prefix = strings.TrimPrefix(prefix, "`")
		prefix = strings.TrimSuffix(prefix, `"`)
		prefix = strings.TrimSuffix(prefix, "`")
		return completionCtx{imp: true, prefix: prefix, start: last.off + 1}
	}
	if last.tok == token.PERIOD {
		return completionCtx{selector: true, base: line[:last.off], start: last.off + 1}
	}
	if last.tok == token.IDENT && n >= 2 && toks[n-2].tok == token.PERIOD {
		return completionCtx{selector: true, base: line[:toks[n-2].off], prefix: last.lit, start: last.off}
	}
	if last.tok == token.IDENT {
		return completionCtx{prefix: last.lit, start: last.off}
	}
	return completionCtx{start: len(line)}
}

// completer accumulates candidates for one Complete call; seen dedupes
// by name in resolution-precedence order (globals shadow index decls,
// locals shadow builtins).
type completer struct {
	r    *REPL
	seen map[string]bool
}

func (c *completer) add(out *[]Candidate, name, kind, detail string) {
	if name == "" || c.seen[name] || strings.HasPrefix(name, "__") {
		return
	}
	c.seen[name] = true
	*out = append(*out, Candidate{Name: name, Kind: kind, Detail: detail})
}

// file is the scratch package's single source file, or nil on a fresh
// REPL that has not evaluated anything yet.
func (r *REPL) file() *syntax.File {
	if len(r.pkg.Files) == 0 {
		return nil
	}
	return r.pkg.Files[0]
}

// ---- bare-name enumeration ----

// scopeNames enumerates every name a bare identifier could resolve to at
// the cursor, mirroring the VM's lookup order: file-scope imports,
// globals, index decls, unnamed imports' package names, dot-import
// members (:cd's pseudo import included), builtins, keywords.
func (c *completer) scopeNames(out *[]Candidate) {
	r := c.r
	file := r.file()
	if file != nil {
		for name, ref := range r.pkg.Scopes[file] {
			c.add(out, name, CandPackage, ref.Path)
		}
	}
	for _, name := range r.pkg.Globals.Names() {
		gv, _ := r.pkg.Globals.Get(name)
		c.add(out, name, globalKind(gv), globalDetail(gv))
	}
	if r.pkg.Index != nil {
		for _, d := range r.pkg.Index.Decls {
			c.add(out, d.Name, declKind(d), c.declDetail(r.pkg, d))
		}
	}
	if file != nil {
		for _, ref := range r.pkg.Imports[file] {
			switch ref.Alias {
			case ".":
				// dot-imported members resolve unqualified — :cd's pseudo
				// import also admits unexported names via AllNames.
				if p, err := ref.Materialize(); err == nil && p != nil {
					c.pkgMembers(p, ref.AllNames, out)
				}
			case "", "_":
				if ref.Alias == "_" {
					continue
				}
				// an unaliased import also binds its declared package
				// name (differs from the path tail, e.g. gopkg.in/yaml.v3).
				if p, err := ref.Materialize(); err == nil && p != nil {
					c.add(out, p.Name, CandPackage, ref.Path)
				}
			}
		}
	}
	for _, name := range r.engine.builtins.Names() {
		bv, _ := r.engine.builtins.Get(name)
		c.add(out, name, globalKind(bv), globalDetail(bv))
	}
	for _, name := range predeclared {
		c.add(out, name, CandBuiltin, "")
	}
	for _, kw := range goKeywords {
		c.add(out, kw, CandKeyword, "")
	}
}

// ---- base-expression resolution (pure AST walk, no eval) ----

// resolveBase parses the selector's base text and resolves it to a value.
// The whole text is tried first; when it does not parse or resolve
// (`f(x`, `a + b`), each token-boundary suffix is tried — `x` inside
// `f(x.` is still reachable because a selector binds tighter than any
// operator that could precede it.
func (r *REPL) resolveBase(text string) (runtime.Value, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	fset := token.NewFileSet()
	f := fset.AddFile("repl-complete.go", -1, len(text))
	var s scanner.Scanner
	s.Init(f, []byte(text), nil, 0)
	var offs []int
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		offs = append(offs, f.Offset(pos))
	}
	for _, off := range offs {
		expr, err := parser.ParseExpr(text[off:])
		if err != nil {
			continue
		}
		if v, ok := r.resolveExpr(expr); ok {
			return v, true
		}
	}
	return nil, false
}

// resolveExpr evaluates an expression statically: it walks idents,
// selectors, parens, stars, integer indexing and composite literals —
// everything whose "value" can be known without running user code. Call
// expressions, binary operators and literals stop the walk.
func (r *REPL) resolveExpr(x ast.Expr) (runtime.Value, bool) {
	switch e := x.(type) {
	case *ast.Ident:
		return r.resolveIdent(e.Name)
	case *ast.SelectorExpr:
		base, ok := r.resolveExpr(e.X)
		if !ok {
			return nil, false
		}
		return r.memberValue(base, e.Sel.Name)
	case *ast.ParenExpr:
		return r.resolveExpr(e.X)
	case *ast.StarExpr:
		base, ok := r.resolveExpr(e.X)
		if !ok {
			return nil, false
		}
		// peel through every reference layer — minigo's pointers are
		// cells, and `*x`'s operand arrives as its variable cell. For
		// completion the deepest pointee is the useful approximation.
		for {
			dv, ok := runtime.Deref(base)
			if !ok {
				break
			}
			base = dv
		}
		return base, true
	case *ast.IndexExpr:
		base, ok := r.resolveExpr(e.X)
		if !ok {
			return nil, false
		}
		lit, ok := e.Index.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return nil, false
		}
		i, err := strconv.Atoi(lit.Value)
		if err != nil {
			return nil, false
		}
		// `type T []E` stores the Slice under a Named tag — peel it around
		// the reference dereference (either layer may carry the tag).
		v := runtime.Unwrap(base)
		if dv, ok := runtime.Deref(v); ok {
			v = runtime.Unwrap(dv)
		}
		s, ok := v.(*runtime.Slice)
		if !ok || i < 0 || i >= len(s.Elems) {
			return nil, false
		}
		return s.Elems[i], true
	case *ast.CompositeLit:
		tv, ok := r.resolveExpr(e.Type)
		if !ok {
			return nil, false
		}
		td, ok := tv.(*runtime.TypeDef)
		if !ok {
			return nil, false
		}
		return runtime.Zero(td), true
	}
	return nil, false
}

// resolveIdent mirrors the VM's identifier lookup: file-scope imports,
// globals, index decls (materialized), unnamed imports by package name,
// dot-import members, builtins.
func (r *REPL) resolveIdent(name string) (runtime.Value, bool) {
	file := r.file()
	if file != nil {
		if ref, ok := r.pkg.Scopes[file][name]; ok {
			return ref, true
		}
	}
	if gv, ok := r.pkg.Globals.Get(name); ok {
		return gv, true
	}
	if r.pkg.Index != nil {
		if d, ok := memberDeclOf(r.pkg.Index, name); ok {
			if mv, err := r.engine.materialize(r.pkg, d); err == nil && mv != nil {
				return mv, true
			}
		}
	}
	if file != nil {
		for _, ref := range r.pkg.Imports[file] {
			switch {
			case ref.Alias == ".":
				if p, err := ref.Materialize(); err == nil && p != nil {
					if mv, ok := r.pkgMemberValue(p, name); ok {
						return mv, true
					}
				}
			case ref.Alias == "":
				if p, err := ref.Materialize(); err == nil && p != nil && p.Name == name {
					return ref, true
				}
			}
		}
	}
	if bv, ok := r.engine.builtins.Get(name); ok {
		return bv, true
	}
	return nil, false
}

// memberDeclOf is runtime.memberDecl re-exposed for the completion walk:
// index lookup order funcs, types, consts, vars.
func memberDeclOf(ix *index.Index, name string) (*index.Decl, bool) {
	if d, ok := ix.Funcs[name]; ok {
		return d, true
	}
	if d, ok := ix.Types[name]; ok && d.Decl != nil {
		return d.Decl, true
	}
	if d, ok := ix.Consts[name]; ok {
		return d, true
	}
	if d, ok := ix.Vars[name]; ok {
		return d, true
	}
	return nil, false
}

// ---- member value resolution (read-only selectMember) ----

// memberValue resolves base.name without running user code — the
// read-only twin of the VM's selectMember. It answers "what would x.y
// be" well enough for the next completion level.
func (r *REPL) memberValue(v runtime.Value, name string) (runtime.Value, bool) {
	switch b := v.(type) {
	case *runtime.ImportRef:
		if !b.AllNames && !token.IsExported(name) {
			return nil, false
		}
		p, err := b.Materialize()
		if err != nil || p == nil {
			return nil, false
		}
		return r.pkgMemberValue(p, name)
	case *runtime.Package:
		if !token.IsExported(name) {
			return nil, false
		}
		return r.pkgMemberValue(b, name)
	case *runtime.Struct:
		if fv, ok := r.structMemberValue(b, name); ok {
			return fv, true
		}
		if m, ok := b.Def.Methods[name]; ok {
			return m, true
		}
		for _, spec := range b.Def.EmbedSpecs {
			emb, err := r.engine.resolveTypeRef(b.Def, spec)
			if err != nil || emb == nil {
				continue
			}
			if m, ok := r.typeMethodValue(emb, name); ok {
				return m, true
			}
		}
		return nil, false
	case *runtime.Named:
		if m, ok := r.typeMethodValue(b.Typ, name); ok {
			return m, true
		}
		return r.memberValue(b.V, name)
	case *runtime.Cell:
		if m, ok := r.typeMethodValue(b.Typ, name); ok {
			return m, true
		}
		if dv, ok := runtime.Deref(v); ok {
			return r.memberValue(dv, name)
		}
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		if dv, ok := runtime.Deref(v); ok {
			return r.memberValue(dv, name)
		}
	case *runtime.TypeDef:
		return r.typeMemberValue(b, name)
	case *runtime.TypedNil:
		return r.typeMemberValue(b.Typ, name)
	case *runtime.IfaceNil:
		return r.typeMemberValue(b.Typ, name)
	case *runtime.Slice:
		return r.typeMemberValue(b.Typ, name)
	case *runtime.Map:
		return r.typeMemberValue(b.Typ, name)
	case *runtime.Chan:
		return r.typeMemberValue(b.Typ, name)
	case *runtime.GoValue:
		return hostMemberValue(b.V, name)
	case time.Duration:
		return hostMemberValue(b, name)
	}
	return nil, false
}

// structMemberValue finds a field breadth-first like Go promotion: the
// shallowest match wins; a same-depth ambiguity quietly takes the first
// hit (a completer needs a plausible type, not a verdict). A field whose
// stored value is NIL — the zero of a struct-typed field — still answers
// through its declared type, so `u.Builder.` completes even when the
// Builder slot was never written.
func (r *REPL) structMemberValue(s *runtime.Struct, name string) (runtime.Value, bool) {
	level := []*runtime.Struct{s}
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
		var next []*runtime.Struct
		for _, st := range level {
			if st == nil || st.Def == nil {
				continue
			}
			for i, n := range st.Def.Fields {
				if n != name {
					continue
				}
				fv := st.Fields[i]
				if _, isNil := fv.(runtime.Nil); fv == nil || isNil {
					if fts, err := r.engine.fieldTypes(st.Def); err == nil && i < len(fts) && fts[i] != nil {
						return &runtime.TypedNil{Typ: fts[i]}, true
					}
				}
				return fv, true
			}
			for _, i := range st.Def.EmbedIdx {
				if i < len(st.Fields) {
					if emb := structOfValue(st.Fields[i]); emb != nil {
						next = append(next, emb)
					}
				}
			}
		}
		level = next
	}
	// a nil embedded pointer field hid every promoted member from the
	// value walk — the typedefs still know their types.
	return r.typeFieldValue(s.Def, name)
}

// typeFieldValue finds a field on a typedef — promoted ones included —
// and answers a TypedNil carrying its declared type. Used when the live
// field value is absent (nil embedded field, unwritten slot).
func (r *REPL) typeFieldValue(td *runtime.TypeDef, name string) (runtime.Value, bool) {
	level := []*runtime.TypeDef{td}
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
		var next []*runtime.TypeDef
		for _, t := range level {
			if t == nil {
				continue
			}
			for i, n := range t.Fields {
				if n != name {
					continue
				}
				if fts, err := r.engine.fieldTypes(t); err == nil && i < len(fts) && fts[i] != nil {
					return &runtime.TypedNil{Typ: fts[i]}, true
				}
				return &runtime.TypedNil{Typ: t}, true
			}
			for _, spec := range t.EmbedSpecs {
				if emb, err := r.engine.resolveTypeRef(t, spec); err == nil && emb != nil {
					next = append(next, emb)
				}
			}
		}
		level = next
	}
	return nil, false
}

// structOfValue resolves a field value to its struct — Named tags and
// reference layers peel until a Struct appears (FieldRef.structOf's twin).
func structOfValue(v runtime.Value) *runtime.Struct {
	for {
		if s, ok := v.(*runtime.Struct); ok {
			return s
		}
		if n, ok := v.(*runtime.Named); ok {
			v = n.V
			continue
		}
		dv, ok := runtime.Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}

// typeMethodValue walks a typedef's own methods, promoted methods
// through embeds, host boxes and anonymous-pointer pointee methods.
func (r *REPL) typeMethodValue(td *runtime.TypeDef, name string) (runtime.Value, bool) {
	for td != nil {
		td = r.engine.peelAliasTd(td)
		if td == nil {
			return nil, false
		}
		if m, ok := td.Methods[name]; ok {
			return m, true
		}
		for _, spec := range td.EmbedSpecs {
			emb, err := r.engine.resolveTypeRef(td, spec)
			if err != nil || emb == nil {
				continue
			}
			if m, ok := r.typeMethodValue(emb, name); ok {
				return m, true
			}
		}
		if td.HostNew != nil {
			return hostMemberValue(td.HostNew(), name)
		}
		if td.Kind != runtime.KindPointer || td.Spec != nil {
			break
		}
		et, err := r.engine.elemOf(td)
		if err != nil || et == nil {
			break
		}
		td = et
	}
	return nil, false
}

// typeMemberValue is memberValue for a type-level base: methods, then
// enum members — REPL consts are hoisted into Globals as read-only cells
// stamped with the declared typedef (`const Red Color`), other packages'
// const decls live in the index.
func (r *REPL) typeMemberValue(td *runtime.TypeDef, name string) (runtime.Value, bool) {
	if m, ok := r.typeMethodValue(td, name); ok {
		return m, true
	}
	if td != nil && td.Pkg != nil {
		if gv, ok := td.Pkg.Globals.Get(name); ok {
			// const cells are sealed read-only; a var stamped with the
			// typedef is not a type member (T.Var is not valid Go).
			if cell, ok := gv.(*runtime.Cell); ok && cell.ReadOnly && cell.Typ != nil && runtime.TypIdentical(cell.Typ, td) {
				return gv, true
			}
		}
	}
	for _, cd := range r.enumMembersOf(td) {
		if cd.Name == name {
			// the const's value needs package init; its declared type is
			// enough for the next completion level.
			return &runtime.TypedNil{Typ: td}, true
		}
	}
	return nil, false
}

// enumMembersOf reports the const decls explicitly typed with td — the
// enum-member set (inspect.EnumMembersOf's shape, minus the wrapper).
func (r *REPL) enumMembersOf(td *runtime.TypeDef) []*index.Decl {
	if td == nil || td.Pkg == nil || td.Pkg.Index == nil || td.Name == "" {
		return nil
	}
	var out []*index.Decl
	for _, cd := range td.Pkg.Index.Decls {
		if cd.Kind != index.ConstDecl {
			continue
		}
		var t ast.Expr
		if vs, ok := cd.Spec.(*ast.ValueSpec); ok {
			t = vs.Type
		}
		if t == nil {
			t = cd.InheritedType
		}
		if id, ok := t.(*ast.Ident); ok && id.Name == td.Name {
			out = append(out, cd)
		}
	}
	return out
}

// pkgMemberValue is Package.Member that never runs package init: globals
// (populated for ready and host-bound packages), then the index —
// funcs/types materialize lazily; var/const decls fall back to their
// declared type so `var s pkg.T` completes before init has run.
func (r *REPL) pkgMemberValue(p *runtime.Package, name string) (runtime.Value, bool) {
	if gv, ok := p.Globals.Get(name); ok {
		return gv, true
	}
	if p.Index == nil {
		return nil, false
	}
	d, ok := memberDeclOf(p.Index, name)
	if !ok {
		return nil, false
	}
	switch d.Kind {
	case index.FuncDecl, index.TypeDecl:
		if mv, err := r.engine.materialize(p, d); err == nil && mv != nil {
			return mv, true
		}
	case index.VarDecl, index.ConstDecl:
		// `var s T` carries its type statically — resolve it rather than
		// initializing the package for a live value.
		if vs, ok := d.Spec.(*ast.ValueSpec); ok && vs.Type != nil {
			ctx := &runtime.TypeDef{Pkg: p, File: d.File}
			if td, err := r.engine.resolveTypeRef(ctx, vs.Type); err == nil && td != nil {
				return &runtime.TypedNil{Typ: td}, true
			}
		}
	}
	return nil, false
}

// hostMemberValue reads a field or method off a host Go value by
// reflection — hostMember's read-only twin: fields on the struct the
// pointer chain lands on (exported only via CanInterface), then methods.
func hostMemberValue(x any, name string) (runtime.Value, bool) {
	rv := reflect.ValueOf(x)
	if !rv.IsValid() {
		return nil, false
	}
	fv := rv
	for fv.Kind() == reflect.Pointer || fv.Kind() == reflect.Interface {
		if fv.IsNil() {
			fv = reflect.Value{}
			break
		}
		fv = fv.Elem()
	}
	if fv.IsValid() && fv.Kind() == reflect.Struct {
		f := fv.FieldByName(name)
		if f.IsValid() && f.CanInterface() {
			return &runtime.GoValue{V: f.Interface()}, true
		}
	}
	if m := rv.MethodByName(name); m.IsValid() {
		return &runtime.GoValue{V: m.Interface()}, true
	}
	return nil, false
}

// ---- member enumeration ----

// members enumerates everything `v.` could select — the read-only twin
// of selectMember's dispatch. Declared typedefs contribute methods and
// fields; host values contribute reflect members; packages contribute
// their globals and index.
func (c *completer) members(v runtime.Value, out *[]Candidate, depth int) {
	if depth > 8 || v == nil {
		return
	}
	switch b := v.(type) {
	case *runtime.ImportRef:
		if p, err := b.Materialize(); err == nil && p != nil {
			c.pkgMembers(p, b.AllNames, out)
		}
	case *runtime.Package:
		c.pkgMembers(b, false, out)
	case *runtime.Struct:
		c.structFields(b.Def, out)
		c.typedefMethods(b.Def, out)
	case *runtime.Named:
		td := c.r.engine.peelAliasTd(b.Typ)
		c.typedefMethods(td, out)
		if st, ok := b.V.(*runtime.Struct); ok && td == b.Typ {
			// `type A B` shares B's storage: the inner struct's Def is B's
			// decl — its fields are the layout, but its method set is not
			// inherited (A has only its own methods).
			c.structFields(st.Def, out)
		} else {
			c.members(b.V, out, depth+1) // underlying: struct fields, host box
		}
	case *runtime.Cell:
		c.typedefMethods(b.Typ, out) // declared-type stamp (`var x T`)
		if dv, ok := runtime.Deref(v); ok {
			c.members(dv, out, depth+1)
		}
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		if dv, ok := runtime.Deref(v); ok {
			c.members(dv, out, depth+1)
		}
	case *runtime.TypeDef:
		c.typeMembers(b, out, false) // method expressions need the value set
	case *runtime.TypedNil:
		c.typeMembers(b.Typ, out, true)
	case *runtime.IfaceNil:
		c.typeMembers(b.Typ, out, true)
	case *runtime.Slice:
		c.typedefMethods(b.Typ, out)
	case *runtime.Map:
		c.typedefMethods(b.Typ, out)
	case *runtime.Chan:
		c.typedefMethods(b.Typ, out)
	case *runtime.GoValue:
		c.hostMembers(b.V, out)
	case time.Duration:
		c.hostMembers(b, out)
	}
}

// typeMembers enumerates a type-level selector's members: the method set
// plus enum members (`Color.Red` on `type Color int`) — REPL-hoisted
// cells stamped with the typedef first, then the package's const decls.
// ptrRecv=false restricts to the value method set: `T.M` is a method
// expression, which rejects pointer receivers; `var x T` stays addressable.
func (c *completer) typeMembers(td *runtime.TypeDef, out *[]Candidate, ptrRecv bool) {
	c.typedefMethodsOpt(td, out, ptrRecv)
	if td == nil || td.Pkg == nil || td.Name == "" {
		return
	}
	for _, name := range td.Pkg.Globals.Names() {
		gv, _ := td.Pkg.Globals.Get(name)
		// enum members are read-only const cells; a var stamped with
		// the typedef is a package var, not a member of the type.
		if cell, ok := gv.(*runtime.Cell); ok && cell.ReadOnly && cell.Typ != nil && runtime.TypIdentical(cell.Typ, td) {
			c.add(out, name, CandConst, "")
		}
	}
	for _, cd := range c.r.enumMembersOf(td) {
		c.add(out, cd.Name, CandConst, "")
	}
}

// typedefMethods enumerates the callable method set of a typedef —
// declared plus promoted (embeds resolved through the engine's own
// type-reference machinery), host-backed sets, interface requirements.
// Both value- and pointer-receiver methods are offered: REPL globals
// are addressable cells, so `s.SetX` is valid even on a value-typed s.
func (c *completer) typedefMethods(td *runtime.TypeDef, out *[]Candidate) {
	c.typedefMethodsOpt(td, out, true)
}

// typedefMethodsOpt is typedefMethods with a knob for pointer-receiver
// methods — dropped for type-level selectors (`T.M` method expressions).
func (c *completer) typedefMethodsOpt(td *runtime.TypeDef, out *[]Candidate, ptrRecv bool) {
	if td == nil {
		return
	}
	ptrs := []bool{false}
	if ptrRecv {
		ptrs = append(ptrs, true)
	}
	for _, ptr := range ptrs {
		for name, m := range c.r.engine.methodFuncs(td, ptr, map[*runtime.TypeDef]bool{}) {
			c.add(out, name, CandMethod, funcDetail(m))
		}
	}
	if td.HostNew != nil {
		c.hostMembers(td.HostNew(), out)
	}
}

// structFields enumerates declared and promoted (embedded) fields.
func (c *completer) structFields(td *runtime.TypeDef, out *[]Candidate) {
	if td == nil {
		return
	}
	fts, _ := c.r.engine.fieldTypes(td)
	for i, name := range td.Fields {
		var detail string
		if i < len(fts) && fts[i] != nil {
			detail = runtime.DisplayName(fts[i])
		}
		c.add(out, name, CandField, detail)
	}
	for _, spec := range td.EmbedSpecs {
		emb, err := c.r.engine.resolveTypeRef(td, spec)
		if err != nil || emb == nil {
			continue
		}
		c.structFields(emb, out)
	}
}

// hostMembers enumerates exported fields and methods of a host value via
// reflection — hostMember's read-only twin.
func (c *completer) hostMembers(x any, out *[]Candidate) {
	rv := reflect.ValueOf(x)
	if !rv.IsValid() {
		return
	}
	fv := rv
	for fv.Kind() == reflect.Pointer || fv.Kind() == reflect.Interface {
		if fv.IsNil() {
			fv = reflect.Value{}
			break
		}
		fv = fv.Elem()
	}
	if fv.IsValid() && fv.Kind() == reflect.Struct {
		// VisibleFields flattens promoted (embedded) fields and drops the
		// shadowed deeper ones — hostMember's FieldByName promotes the
		// same way. Unexported entries stay out, as they cannot interface.
		for _, sf := range reflect.VisibleFields(fv.Type()) {
			if !sf.IsExported() {
				continue // unexported
			}
			c.add(out, sf.Name, CandField, sf.Type.String())
		}
	}
	t := rv.Type()
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		// rv.Method(i).Type() is the bound signature (no receiver) — the
		// shape a user typing `x.M<TAB>` actually calls.
		c.add(out, m.Name, CandMethod, rv.Method(i).Type().String())
	}
}

// pkgMembers enumerates a package's selectable members — globals first
// (host-bound packages live entirely in globals), then index decls.
// It never runs package init, so unready packages still enumerate.
func (c *completer) pkgMembers(p *runtime.Package, allNames bool, out *[]Candidate) {
	for _, name := range p.Globals.Names() {
		if !allNames && !token.IsExported(name) {
			continue
		}
		gv, _ := p.Globals.Get(name)
		c.add(out, name, globalKind(gv), globalDetail(gv))
	}
	if p.Index != nil {
		for _, d := range p.Index.Decls {
			if !allNames && !token.IsExported(d.Name) {
				continue
			}
			c.add(out, d.Name, declKind(d), c.declDetail(p, d))
		}
	}
}

// ---- kind/detail rendering ----

func globalKind(v runtime.Value) string {
	switch x := v.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BuiltinFunc, *runtime.BoundMethod:
		return CandFunc
	case *runtime.TypeDef:
		return CandType
	case *runtime.Package, *runtime.ImportRef:
		return CandPackage
	case *runtime.Cell:
		if x.ReadOnly {
			return CandConst
		}
		return CandVar
	}
	return CandVar
}

func globalDetail(v runtime.Value) string {
	switch x := v.(type) {
	case *runtime.Function:
		return funcDetail(x)
	case *runtime.BuiltinFunc:
		if x.Target != nil {
			return reflect.TypeOf(x.Target).String()
		}
	case *runtime.TypeDef:
		return runtime.DisplayName(x)
	case *runtime.Cell:
		if x.Typ != nil {
			return runtime.DisplayName(x.Typ)
		}
	}
	return ""
}

func funcDetail(f *runtime.Function) string {
	if f == nil || f.Decl == nil || f.Decl.Type == nil {
		return ""
	}
	ctx := &runtime.TypeDef{Pkg: f.Pkg, File: f.File, Binds: f.Binds}
	return runtime.TypGoSpelling(f.Decl.Type, ctx)
}

func declKind(d *index.Decl) string {
	switch d.Kind {
	case index.FuncDecl:
		return CandFunc
	case index.VarDecl:
		return CandVar
	case index.ConstDecl:
		return CandConst
	default:
		return CandType
	}
}

func (c *completer) declDetail(p *runtime.Package, d *index.Decl) string {
	if d.Kind == index.FuncDecl && d.Func != nil && d.Func.Type != nil {
		ctx := &runtime.TypeDef{Pkg: p, File: d.File}
		return runtime.TypGoSpelling(d.Func.Type, ctx)
	}
	if d.Kind == index.VarDecl || d.Kind == index.ConstDecl {
		var t ast.Expr
		if vs, ok := d.Spec.(*ast.ValueSpec); ok {
			t = vs.Type
		}
		if t == nil {
			t = d.InheritedType
		}
		if t != nil {
			ctx := &runtime.TypeDef{Pkg: p, File: d.File}
			return runtime.TypSpelling(t, ctx)
		}
	}
	return ""
}

// importCandidates completes import paths inside `import "..."`. A
// `./`, `../` or `/`-leading prefix completes directories (the REPL's
// own directory-import extension); anything else completes from the
// cached importables set.
func (r *REPL) importCandidates(prefix string) []Candidate {
	var out []Candidate
	if strings.HasPrefix(prefix, ".") || strings.HasPrefix(prefix, "/") {
		out = r.dirImportCandidates(prefix)
	} else {
		for _, cand := range r.importables() {
			if strings.HasPrefix(cand.Name, prefix) {
				out = append(out, cand)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// dirImportCandidates lists directories holding Go files that match the
// typed `./`, `../` or `/abs` prefix — the dir-import forms REPL
// accepts but real Go source does not.
func (r *REPL) dirImportCandidates(prefix string) []Candidate {
	i := strings.LastIndex(prefix, "/")
	dirPart := prefix[:i+1] // "./", "../x/", "/abs/to/"
	var dir string
	if strings.HasPrefix(dirPart, "/") {
		dir = filepath.Clean(dirPart)
	} else {
		dir = filepath.Join(r.engine.WorkingDir(), dirPart)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		full := dirPart + name
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		if hasGoFiles(filepath.Join(dir, name)) {
			out = append(out, Candidate{Name: full, Kind: CandPackage, Detail: "dir"})
		}
	}
	return out
}

// importables enumerates the importable package paths: bound intrinsics
// first (they shadow same-named source packages), then GOROOT stdlib,
// then the module's own packages and its go.mod requires. Built once
// and cached on the REPL — path sets do not change mid-session.
func (r *REPL) importables() []Candidate {
	if r.importCands != nil {
		return r.importCands
	}
	var out []Candidate
	seen := map[string]bool{}
	add := func(path, detail string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, Candidate{Name: path, Kind: CandPackage, Detail: detail})
	}
	for path := range r.engine.binds {
		add(path, "bound")
	}
	// stdlib: walk GOROOT/src the same way `go list std` would — dirs
	// holding non-main Go packages, minus trees user code cannot import.
	if goroot := build.Default.GOROOT; goroot != "" {
		src := filepath.Join(goroot, "src")
		_ = filepath.WalkDir(src, func(dir string, d fs.DirEntry, err error) error {
			if err != nil {
				return filepath.SkipDir
			}
			if !d.IsDir() || dir == src {
				return nil
			}
			base := d.Name()
			if strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") ||
				base == "internal" || base == "testdata" || base == "vendor" ||
				base == "cmd" || base == "builtin" {
				return filepath.SkipDir
			}
			if hasPackageFiles(dir) {
				rel, _ := filepath.Rel(src, dir)
				add(filepath.ToSlash(rel), "stdlib")
			}
			return nil
		})
	}
	// the module: its own packages (modulePath/rel) and its requires —
	// the spellings go.mod actually lets you import.
	type moduleLocator interface {
		RootDir() string
		ModulePath() string
		Requires() map[string]string
	}
	if loc, ok := r.engine.resolver.(moduleLocator); ok {
		for mod := range loc.Requires() {
			add(mod, "module")
		}
		root, mod := loc.RootDir(), loc.ModulePath()
		if mod != "" {
			_ = filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return nil
				}
				base := d.Name()
				if strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") ||
					base == "vendor" || base == "testdata" {
					return filepath.SkipDir
				}
				if dir != root {
					if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
						return filepath.SkipDir // nested module
					}
				}
				if hasPackageFiles(dir) {
					if rel, _ := filepath.Rel(root, dir); rel == "." {
						add(mod, "module")
					} else {
						add(mod+"/"+filepath.ToSlash(rel), "module")
					}
				}
				return nil
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	r.importCands = out
	return out
}

// hasPackageFiles reports whether dir directly contains at least one
// non-test .go file belonging to a non-main package — the cheap shape
// of "importable" (a directory of only main/ test files cannot be).
func hasPackageFiles(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err == nil && f.Name != nil && f.Name.Name != "main" {
			return true
		}
	}
	return false
}

// hasGoFiles reports whether dir directly contains at least one non-test
// .go file — for `./` dir imports even a main package is loadable.
func hasGoFiles(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}
