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
// keeps TAB free of user-code side effects. A session may opt into the
// tier above via [REPL.SetEvalPolicy] — EvalBase evaluates a
// parsed-but-unresolvable base through EvalExpr, so `f().` then offers
// the result's members at the cost of running the call's side effects.
// Declared types stand in where runtime values are absent: `var s pkg.T`
// in an uninitialized package still completes through the decl's type
// expression.

package repl

import (
	"context"
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

// EvalPolicy is the IPython-style `evaluation` knob: how much user
// code completion may run to resolve a selector's base expression.
type EvalPolicy int

const (
	// EvalMinimal is the default: base resolution is a pure AST walk
	// and never runs user code, so `f().` yields no candidates.
	EvalMinimal EvalPolicy = iota
	// EvalBase lets a parsed-but-unresolvable base expression evaluate
	// through the engine's EvalExpr bridge — `f().`, `m[k].` and
	// `a+b.` then complete against the result value, at the price of
	// running the expression's side effects on every TAB press.
	EvalBase
)

// SetEvalPolicy switches how much user code [REPL.Complete] may run to
// resolve a selector's base. The default, [EvalMinimal], evaluates
// nothing — the static walk alone answers.
func (r *REPL) SetEvalPolicy(p EvalPolicy) {
	r.evalPolicy = p
}

// EvalPolicy reports the current completion evaluation policy.
func (r *REPL) EvalPolicy() EvalPolicy {
	return r.evalPolicy
}

// Candidate is one completion suggestion.
type Candidate struct {
	Name   string // the completion text
	Kind   string // CandKeyword, CandBuiltin, CandFunc, ...
	Detail string // signature or declared-type rendering, when cheaply known
	// Ambiguous marks a promoted member provided by two embeds at the
	// same depth — Go rejects the selector, but completion still offers
	// the name with the flag set (the completer offers, not referees).
	Ambiguous bool
}

// Values of [Candidate.Kind], for a UI to group or decorate candidates.
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
	CandFile    = "file" // a path argument naming a file
	CandDir     = "dir"  // a path argument naming a directory
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

// Complete returns completion candidates, sorted by name, for a REPL
// input with the cursor at end of line. [REPL.CompleteToken] also
// returns where to insert them; meta-command lines (starting with `:`)
// yield nothing here and are completed by [REPL.CompleteCommandArg].
// Complete offers members after `x.` or `x.pa`, names in scope after an
// identifier or elsewhere, and import paths after `import "`. Completion
// never runs user code, so `f().` yields nothing.
func (r *REPL) Complete(line string) []Candidate {
	_, cands := r.complete(line)
	return cands
}

// CompleteToken differs from [REPL.Complete] only in also returning the
// byte offset where the token being completed begins: a candidate
// replaces line[start:].
func (r *REPL) CompleteToken(line string) (start int, cands []Candidate) {
	return r.complete(line)
}

func (r *REPL) complete(line string) (start int, cands []Candidate) {
	if strings.HasPrefix(strings.TrimSpace(line), ":") {
		return len(line), nil
	}
	ctx := completeContext(line)
	if ctx.done {
		return ctx.start, nil
	}
	if ctx.imp {
		return ctx.start, r.importCandidates(ctx.prefix)
	}
	c := &completer{r: r, seen: map[string]bool{}}
	var out []Candidate
	if ctx.selector {
		if ctx.idxBase != "" {
			// a `.`-tail inside an unclosed `[` — suppress member
			// completion when the bracket's base is a value (an
			// index slot: `m[k.`); keep it when it names a type
			// (`List[pkg.` instantiation) or is no expression at all
			// (`map[pkg.` — `map` is a keyword). Classify statically:
			// evaluating `getMap()` would run user code.
			v, prim := r.resolveBaseStatic(ctx.idxBase)
			switch {
			case prim == nil:
				// no expression tail — `map[`/`chan[` keyword.
			case v == nil && !typeShaped(prim):
				// a primary the static walk cannot answer and whose
				// shape can only be a value (call, index, composite).
				// Ident/Selector tails stay live: `Undeclared[pkg.`
				// still completes package members.
				return ctx.start, nil
			case v != nil:
				if _, isType := v.(*runtime.TypeDef); !isType {
					return ctx.start, nil
				}
			}
		}
		if v, ok := r.resolveBase(ctx.base); ok {
			c.members(v, &out, 0)
		}
	} else if ctx.mapkey {
		if v, ok := r.resolveBase(ctx.base); ok {
			c.mapKeys(v, ctx.quote, &out)
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
// (with the base expression's source), "inside an import string",
// "a map's string key inside `m["..."`", or "bare identifier prefix".
type completionCtx struct {
	selector bool   // completing after `.`
	imp      bool   // completing inside an `import "..."` string
	mapkey   bool   // completing a string key inside `m["..."`
	done     bool   // no completion at this tail (e.g. a closed import literal)
	base     string // source text of the selector's (or map's) base expression
	idxBase  string // text before the innermost unclosed `[` around a `.`-tail, if any
	prefix   string // the partial identifier (or import path) being typed
	start    int    // byte offset where the replaceable token begins
	quote    byte   // the delimiter of an unterminated literal ('"' or '`')
}

// unclosedLit reports whether a scanned STRING literal's text is
// unterminated. A trailing opener is not a closer when an odd run of
// backslashes escapes it: `"say\"` is still open, `"a\\"` is closed.
// Raw literals cannot escape their closer.
func unclosedLit(lit string) bool {
	if len(lit) < 2 || lit[len(lit)-1] != lit[0] {
		return true
	}
	if lit[0] == '`' {
		return false
	}
	bs := 0
	for i := len(lit) - 2; i >= 0 && lit[i] == '\\'; i-- {
		bs++
	}
	return bs%2 == 1
}

// scannedTok is a scanned line token with its byte offset.
type scannedTok struct {
	tok token.Token
	lit string
	off int
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
	var toks []scannedTok
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
		toks = append(toks, scannedTok{tok, lit, f.Offset(pos)})
	}
	n := len(toks)
	if n == 0 {
		return completionCtx{start: len(line)}
	}
	last := toks[n-1]
	// `import "str` (also `import . "`, `import name "`, `import (`)
	// ends inside a string literal: complete import paths, not code.
	if last.tok == token.STRING && toks[0].tok == token.IMPORT {
		// a closed literal (`import "strings"`) is past the import
		// context — offering paths here would splice over the
		// closing quote.
		if !unclosedLit(last.lit) {
			return completionCtx{done: true, start: len(line)}
		}
		prefix := last.lit
		prefix = strings.TrimPrefix(prefix, `"`)
		prefix = strings.TrimPrefix(prefix, "`")
		prefix = strings.TrimSuffix(prefix, `"`)
		prefix = strings.TrimSuffix(prefix, "`")
		return completionCtx{imp: true, prefix: prefix, start: last.off + 1}
	}
	// `m["k` (also `u.M["`, `f()["`) ends inside a string literal that
	// opens an index expression: complete the live map's string keys.
	// A closed literal (`m["k"`) is past the context — a splice would
	// eat the closing quote.
	if last.tok == token.STRING && n >= 2 && toks[n-2].tok == token.LBRACK {
		if lit := last.lit; unclosedLit(lit) {
			return completionCtx{mapkey: true, base: line[:toks[n-2].off], prefix: lit[1:], start: last.off + 1, quote: lit[0]}
		}
	}
	// A line ending in whitespace sits on a fresh empty token: the
	// last scanned token is not the replacement target (`var |x`
	// must splice at the cursor, not pull the space into the token
	// and produce `varx`).
	if line[len(line)-1] == ' ' || line[len(line)-1] == '\t' {
		return completionCtx{start: len(line)}
	}
	if last.tok == token.PERIOD {
		c := completionCtx{selector: true, base: line[:last.off], start: last.off + 1}
		if o := unclosedBracketOff(toks[:n-1]); o >= 0 {
			// `m[k.` — the `.`-tail sits inside an unclosed index slot:
			// the base suffix (`k`) parses, so the fallback would offer
			// k's members as a map-index false positive. idxBase keeps
			// the text before the `[` so `complete` can tell index
			// bases (suppress) from type arguments — `map[pkg.` and
			// `List[pkg.` are real selector contexts. `(` stays live:
			// `(u.` resolving u is correct (grouping).
			c.idxBase = line[:o]
		}
		return c
	}
	if last.tok == token.IDENT && n >= 2 && toks[n-2].tok == token.PERIOD {
		c := completionCtx{selector: true, base: line[:toks[n-2].off], prefix: last.lit, start: last.off}
		if o := unclosedBracketOff(toks[:n-2]); o >= 0 {
			c.idxBase = line[:o]
		}
		return c
	}
	if last.tok == token.IDENT {
		return completionCtx{prefix: last.lit, start: last.off}
	}
	return completionCtx{start: len(line)}
}

// unclosedBracketOff returns the offset of the innermost still-open `[`
// when it tops the paren/bracket stack — a `.`-tail there sits in an
// index slot. -1 when the innermost group is `(` or everything is
// closed. `(`/`]` mismatches are left alone.
func unclosedBracketOff(toks []scannedTok) int {
	var stack []scannedTok
	for _, t := range toks {
		switch t.tok {
		case token.LPAREN, token.LBRACK:
			stack = append(stack, t)
		case token.RPAREN:
			stack = popGroup(stack, token.LPAREN)
		case token.RBRACK:
			stack = popGroup(stack, token.LBRACK)
		}
	}
	if n := len(stack); n > 0 && stack[n-1].tok == token.LBRACK {
		return stack[n-1].off
	}
	return -1
}

// popGroup drops the innermost still-open open from the stack; a closer
// can only match its own kind (`]` inside `(` closes nothing).
func popGroup(stack []scannedTok, open token.Token) []scannedTok {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].tok == open {
			return append(stack[:i], stack[i+1:]...)
		}
	}
	return stack
}

// completer accumulates candidates for one Complete call; seen dedupes
// by name in resolution-precedence order (globals shadow index decls,
// locals shadow builtins).
type completer struct {
	r        *REPL
	seen     map[string]bool
	fieldAt  map[string]int // field name -> promotion depth it was offered at
	fieldIdx map[string]int // field name -> index into out
}

func (c *completer) add(out *[]Candidate, name, kind, detail string) {
	if name == "" || c.seen[name] || strings.HasPrefix(name, "__") {
		return
	}
	c.seen[name] = true
	*out = append(*out, Candidate{Name: name, Kind: kind, Detail: detail})
}

// addField is add for struct fields, depth-aware: a name reaching the
// output through a second embed at the SAME depth flags the emitted
// candidate ambiguous rather than first-wins (Go rejects `x.F` there).
// A shallower provider still shadows deeper ones — that is not
// ambiguity, so differing depths dedup silently as before.
func (c *completer) addField(out *[]Candidate, name, detail string, depth int) {
	if name == "" || strings.HasPrefix(name, "__") {
		return
	}
	if c.seen[name] {
		prev, ok := c.fieldAt[name]
		if !ok {
			return // the name came from a method or scope, not a field
		}
		i := c.fieldIdx[name]
		switch {
		case depth == prev && depth > 0:
			(*out)[i].Ambiguous = true
		case depth < prev:
			// DFS reaches a deep subtree first: a shallower provider
			// shadows — and un-ambiguates — the deeper offering.
			(*out)[i].Detail = detail
			(*out)[i].Ambiguous = false
			c.fieldAt[name] = depth
		}
		return
	}
	c.seen[name] = true
	if c.fieldAt == nil {
		c.fieldAt = map[string]int{}
		c.fieldIdx = map[string]int{}
	}
	c.fieldAt[name] = depth
	c.fieldIdx[name] = len(*out)
	*out = append(*out, Candidate{Name: name, Kind: CandField, Detail: detail})
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
	for _, name := range r.engine.Builtins.Names() {
		bv, _ := r.engine.Builtins.Get(name)
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

// resolveBaseStatic scans like resolveBase but never evaluates —
// classifying an index-slot tail must not run user code
// (`getMap()[u.` would call getMap under EvalBase). Returns the
// resolved value (nil when static resolution cannot answer) and the
// scanOffsets lists each token's byte offset in text — the
// parse-suffix scan both base resolvers share.
func scanOffsets(text string) []int {
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
	return offs
}

// primary expression the tail parsed to (nil when no suffix parses,
// e.g. the `map`/`chan` keywords before `[`).
func (r *REPL) resolveBaseStatic(text string) (runtime.Value, ast.Expr) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	offs := scanOffsets(text)
	var prim ast.Expr
	for _, off := range offs {
		expr, err := parser.ParseExpr(text[off:])
		if err != nil || !selectorBase(expr) {
			continue
		}
		if prim == nil {
			prim = expr
		}
		if v, ok := r.resolveExpr(expr); ok {
			return v, expr
		}
	}
	return nil, prim
}

// typeShaped reports whether a primary expression can name a type —
// a bare ident or a package-qualified selector. Calls, indexes and
// composites before `[` can only be values.
func typeShaped(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return true
	}
	return false
}

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
	offs := scanOffsets(text)
	for _, off := range offs {
		expr, err := parser.ParseExpr(text[off:])
		if err != nil {
			continue
		}
		// `.` binds tighter than any operator, so a suffix that parses
		// to a binary or unary expression has the dot bound inside its
		// operand — `f() == q.` completes on `q`, not on the comparison
		// (which under EvalBase would also run `f()`'s side effects).
		// Keep scanning for the primary expression at the tail.
		if !selectorBase(expr) {
			continue
		}
		if v, ok := r.resolveExpr(expr); ok {
			return v, true
		}
		// the EvalBase tier: the expression parses but the static walk
		// cannot answer it (call expressions, non-literal indexes) —
		// evaluate the base itself when the policy allows.
		if r.evalPolicy >= EvalBase {
			if v, ok := r.evalBase(expr); ok {
				return v, true
			}
		}
	}
	return nil, false
}

// selectorBase reports whether a parsed base text can be what the `.`
// actually binds to: a primary expression. Binary, unary, star and
// receive roots mean the selector lives inside their operand instead.
func selectorBase(x ast.Expr) bool {
	switch x.(type) {
	case *ast.BinaryExpr, *ast.UnaryExpr, *ast.StarExpr, *ast.KeyValueExpr:
		return false
	}
	return true
}

// evalBase runs a parsed base expression through the EvalExpr bridge —
// the EvalBase tier. The expression's side effects and package init
// really happen; a trap, error or panic just yields no candidates.
func (r *REPL) evalBase(expr ast.Expr) (v runtime.Value, ok bool) {
	file := r.file()
	if file == nil {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			v, ok = nil, false
		}
	}()
	res, err := r.engine.EvalExpr(context.Background(), r.pkg, file, expr)
	if err != nil || res == nil || res == runtime.NIL {
		return nil, false
	}
	return res, true
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
		// The bound keeps a cyclic store (`var x any; x = &x`) from
		// hanging the walk.
		for i := 0; i < 32; i++ {
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
		if d, ok := r.pkg.Index.Lookup(name); ok {
			if mv, err := r.engine.Materialize(r.pkg, d); err == nil && mv != nil {
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
	if bv, ok := r.engine.Builtins.Get(name); ok {
		return bv, true
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
			emb, err := r.engine.ResolveTypeRef(b.Def, spec)
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
					if fts, err := r.engine.FieldTypes(st.Def); err == nil && i < len(fts) && fts[i] != nil {
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
				if fts, err := r.engine.FieldTypes(t); err == nil && i < len(fts) && fts[i] != nil {
					return &runtime.TypedNil{Typ: fts[i]}, true
				}
				return &runtime.TypedNil{Typ: t}, true
			}
			for _, spec := range t.EmbedSpecs {
				if emb, err := r.engine.ResolveTypeRef(t, spec); err == nil && emb != nil {
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
	return r.typeMethodValueOf(td, name, map[*runtime.TypeDef]bool{})
}

// typeMethodValueOf is typeMethodValue with a visited set — recursive
// embeds (`type Node struct{ *Node }`) are legal Go and must terminate.
func (r *REPL) typeMethodValueOf(td *runtime.TypeDef, name string, seen map[*runtime.TypeDef]bool) (runtime.Value, bool) {
	for td != nil {
		td = r.engine.PeelAliasTd(td)
		if td == nil || seen[td] {
			return nil, false
		}
		seen[td] = true
		if m, ok := td.Methods[name]; ok {
			return m, true
		}
		for _, spec := range td.EmbedSpecs {
			emb, err := r.engine.ResolveTypeRef(td, spec)
			if err != nil || emb == nil {
				continue
			}
			if m, ok := r.typeMethodValueOf(emb, name, seen); ok {
				return m, true
			}
		}
		if td.HostNew != nil {
			return hostMemberValue(td.HostNew(), name)
		}
		if td.Kind != runtime.KindPointer || td.Spec != nil {
			break
		}
		et, err := r.engine.ElemOf(td)
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
	d, ok := p.Index.Lookup(name)
	if !ok {
		return nil, false
	}
	switch d.Kind {
	case index.FuncDecl, index.TypeDecl:
		if mv, err := r.engine.Materialize(p, d); err == nil && mv != nil {
			return mv, true
		}
	case index.VarDecl, index.ConstDecl:
		// `var s T` carries its type statically — resolve it rather than
		// initializing the package for a live value. An untyped
		// `var x = f()` gets an inferred one from the initializer.
		if vs, ok := d.Spec.(*ast.ValueSpec); ok {
			t := vs.Type
			if t == nil {
				t = d.InheritedType
			}
			var td *runtime.TypeDef
			if t != nil {
				ctx := &runtime.TypeDef{Pkg: p, File: d.File}
				td, _ = r.engine.ResolveTypeRef(ctx, t)
			} else {
				td = r.inferInitType(p, d, vs, 4)
			}
			if td != nil {
				return &runtime.TypedNil{Typ: td}, true
			}
		}
	}
	return nil, false
}

// ---- init-free initializer inference ----
//
// A foreign package's `var x = <expr>` has no declared type to stand
// in for a live value. inferInitType derives one from the initializer
// without running init — the statically readable shapes: composite
// literals, conversions, make/new, calls to an indexed function (same
// package, file imports, dot imports), and index/selector/deref
// unwraps onto a declared-typed base. fuel bounds var-to-var chains
// so a redeclaration cycle can't spin the walk.

// inferInitType picks the initializer for the decl's name slot and
// derives its type; `var a, b = f()` splits the call's results, so a
// single value carries the name's index as a result index.
func (r *REPL) inferInitType(p *runtime.Package, d *index.Decl, vs *ast.ValueSpec, fuel int) *runtime.TypeDef {
	if fuel <= 0 || len(vs.Values) == 0 {
		return nil
	}
	x := vs.Values[0]
	resIdx := 0
	if len(vs.Values) > 1 {
		if d.NameIdx >= len(vs.Values) {
			return nil
		}
		x = vs.Values[d.NameIdx]
	} else {
		resIdx = d.NameIdx
	}
	return r.initExprType(p, d.File, x, resIdx, fuel)
}

// initExprType derives a typedef for one initializer expression.
// resIdx selects which result of a call expression the var takes.
func (r *REPL) initExprType(p *runtime.Package, file *syntax.File, x ast.Expr, resIdx, fuel int) *runtime.TypeDef {
	ctx := &runtime.TypeDef{Pkg: p, File: file}
	switch e := x.(type) {
	case *ast.ParenExpr:
		return r.initExprType(p, file, e.X, resIdx, fuel)
	case *ast.CompositeLit:
		return r.typeOf(ctx, e.Type)
	case *ast.CallExpr:
		return r.callResultType(p, file, e, resIdx, fuel)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			// `&T{...}` → *T.
			if cl, ok := e.X.(*ast.CompositeLit); ok {
				return r.ptrTo(r.typeOf(ctx, cl.Type), cl.Type, p, file)
			}
			return nil
		}
		// `var n = -i` keeps the operand's type
		return r.initExprType(p, file, e.X, 0, fuel-1)
	case *ast.StarExpr:
		if td := r.initExprType(p, file, e.X, 0, fuel-1); td != nil {
			return r.elemInitType(td)
		}
	case *ast.IndexExpr:
		if resIdx > 0 {
			// `var v, ok = m[k]` — the comma-ok slot is bool.
			return nil
		}
		// `var v = m[k]` — the container's declared element type
		if td := r.initExprType(p, file, e.X, 0, fuel-1); td != nil {
			// `a[0]` on a `*[N]T` both derefs and indexes — peel the
			// pointer layers, then take the container's element.
			for i := 0; i < 8 && td != nil && td.Kind == runtime.KindPointer; i++ {
				td = td.Elem
			}
			if td != nil {
				return r.elemInitType(td)
			}
		}
	case *ast.SelectorExpr:
		return r.selInitType(p, file, e, fuel)
	case *ast.Ident:
		return r.declTypeOf(p, file, e.Name, fuel)
	}
	return nil
}

// typeOf resolves a type expression to its typedef.
func (r *REPL) typeOf(ctx *runtime.TypeDef, x ast.Expr) *runtime.TypeDef {
	td, err := r.engine.ResolveTypeRef(ctx, x)
	if err != nil {
		return nil
	}
	return td
}

// ptrTo mints a `*x` pointer typedef around td — resolveTypeRef
// collapses stars, so `new(T)`/`&T{...}` can't get one by resolving.
// Elem holds the named pointee: `*x` on the result hands it back with
// its methods instead of an anonymous twin.
func (r *REPL) ptrTo(td *runtime.TypeDef, x ast.Expr, p *runtime.Package, file *syntax.File) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	return &runtime.TypeDef{
		Kind: runtime.KindPointer,
		Anon: &ast.StarExpr{X: x},
		Elem: td, Pkg: p, File: file,
	}
}

// elemTypeOf resolves a container typedef's element typedef.
func (r *REPL) elemTypeOf(td *runtime.TypeDef) *runtime.TypeDef {
	et, err := r.engine.ElemOf(td)
	if err != nil {
		return nil
	}
	return et
}

// elemInitType resolves a container typedef's element type for an
// inferred initializer: a minted Elem, else the container's own type
// expression with its stars kept, else ElemOf's walk.
func (r *REPL) elemInitType(td *runtime.TypeDef) *runtime.TypeDef {
	if td.Elem != nil {
		return td.Elem
	}
	switch t := td.TypeExpr().(type) {
	case *ast.StarExpr:
		return r.typeOfWithPtrs(td, t.X)
	case *ast.ArrayType:
		return r.typeOfWithPtrs(td, t.Elt)
	case *ast.MapType:
		return r.typeOfWithPtrs(td, t.Value)
	case *ast.ChanType:
		return r.typeOfWithPtrs(td, t.Value)
	}
	return r.elemTypeOf(td)
}

// typeOfWithPtrs resolves a type expression keeping its leading stars —
// resolveTypeRef collapses `*T` into T, so each peeled star re-wraps
// the result in a minted pointer typedef (`*x` gets the pointee back
// through Elem, methods intact).
func (r *REPL) typeOfWithPtrs(ctx *runtime.TypeDef, x ast.Expr) *runtime.TypeDef {
	ptrs := 0
	for {
		switch e := x.(type) {
		case *ast.StarExpr:
			ptrs++
			x = e.X
			continue
		case *ast.ParenExpr:
			x = e.X
			continue
		}
		break
	}
	td := r.typeOf(ctx, x)
	for ; ptrs > 0; ptrs-- {
		td = r.ptrTo(td, x, ctx.Pkg, ctx.File)
	}
	return td
}

// callResultType derives the resIdx'th result type of a call
// expression — `f()`, `pkg.F()`, `T(x)`, `make(T, ...)`, `new(T)`.
func (r *REPL) callResultType(p *runtime.Package, file *syntax.File, e *ast.CallExpr, resIdx, fuel int) *runtime.TypeDef {
	ctx := &runtime.TypeDef{Pkg: p, File: file}
	switch f := e.Fun.(type) {
	case *ast.Ident:
		switch f.Name {
		case "make":
			if len(e.Args) > 0 {
				return r.typeOf(ctx, e.Args[0])
			}
			return nil
		case "new":
			// `var p = new(T)` — the pointer typedef.
			if len(e.Args) > 0 {
				return r.ptrTo(r.typeOf(ctx, e.Args[0]), e.Args[0], p, file)
			}
			return nil
		}
		// a same-package (or dot-imported) call first: a func name must
		// never reach typeOf — resolving it through Package.Member would
		// materialize (and initialize) the imported package.
		if fp, fd := r.indexFuncOf(p, file, f.Name); fd != nil {
			return r.funcResultType(fp, fd, resIdx)
		}
		// `T(x)` conversion
		if td := r.typeOf(ctx, f); td != nil {
			return td
		}
	case *ast.SelectorExpr:
		// `pkg.F()` / `pkg.T(x)` through the file's imports (Indexed,
		// not initialized).
		id, ok := f.X.(*ast.Ident)
		if !ok {
			return nil
		}
		ref := fileImportRef(p, file, id.Name)
		if ref == nil {
			return nil
		}
		ip, err := ref.Materialize()
		if err != nil || ip == nil || ip.Index == nil {
			return nil
		}
		d, ok := ip.Index.Lookup(f.Sel.Name)
		if !ok {
			return nil
		}
		switch d.Kind {
		case index.FuncDecl:
			return r.funcResultType(ip, d, resIdx)
		case index.TypeDecl:
			if mv, err := r.engine.Materialize(ip, d); err == nil {
				if td, ok := mv.(*runtime.TypeDef); ok {
					return td
				}
			}
		}
	}
	return nil
}

// selInitType derives the type of a selector initializer — `pkg.Name`
// through the file's imports, or the declared field type of `s.F` on
// a declared-typed base.
func (r *REPL) selInitType(p *runtime.Package, file *syntax.File, e *ast.SelectorExpr, fuel int) *runtime.TypeDef {
	id, ok := e.X.(*ast.Ident)
	if !ok {
		return nil
	}
	if ref := fileImportRef(p, file, id.Name); ref != nil {
		ip, err := ref.Materialize()
		if err != nil || ip == nil {
			return nil
		}
		// ip's own dot imports aren't reachable here — nil file.
		return r.declTypeOf(ip, nil, e.Sel.Name, fuel-1)
	}
	td := r.declTypeOf(p, file, id.Name, fuel-1)
	if td == nil {
		return nil
	}
	return r.fieldDeclType(td, e.Sel.Name, map[*runtime.TypeDef]bool{})
}

// declTypeOf resolves a package-level name's declared type — the
// ValueSpec's type, the TypeDef a TypeDecl names, or (untyped) the
// inferred initializer type — then dot-imported decls.
func (r *REPL) declTypeOf(p *runtime.Package, file *syntax.File, name string, fuel int) *runtime.TypeDef {
	if p == nil {
		return nil
	}
	if p.Index != nil {
		if d, ok := p.Index.Lookup(name); ok {
			return r.declTypeOfDecl(p, d, fuel)
		}
	}
	if ip, d, ok := dotImportDecl(p, file, name); ok {
		return r.declTypeOfDecl(ip, d, fuel)
	}
	return nil
}

// dotImportDecl finds name in the indexes of the file's dot-imported
// packages (Indexed only — never initialized).
func dotImportDecl(p *runtime.Package, file *syntax.File, name string) (*runtime.Package, *index.Decl, bool) {
	for _, ref := range p.Imports[file] {
		if ref.Alias != "." {
			continue
		}
		ip, err := ref.Materialize()
		if err != nil || ip == nil || ip.Index == nil {
			continue
		}
		if d, ok := ip.Index.Lookup(name); ok {
			return ip, d, true
		}
	}
	return nil, nil, false
}

// declTypeOfDecl is declTypeOf for a resolved decl.
func (r *REPL) declTypeOfDecl(p *runtime.Package, d *index.Decl, fuel int) *runtime.TypeDef {
	ctx := &runtime.TypeDef{Pkg: p, File: d.File}
	switch d.Kind {
	case index.TypeDecl:
		return r.typeOf(ctx, ast.NewIdent(d.Name))
	case index.VarDecl, index.ConstDecl:
		vs, ok := d.Spec.(*ast.ValueSpec)
		if !ok {
			return nil
		}
		t := vs.Type
		if t == nil {
			t = d.InheritedType
		}
		if t != nil {
			return r.typeOf(ctx, t)
		}
		return r.inferInitType(p, d, vs, fuel-1)
	}
	return nil
}

// indexFuncOf finds a FuncDecl in the package's index, falling back to
// dot-imported packages (Indexed only — never initialized).
func (r *REPL) indexFuncOf(p *runtime.Package, file *syntax.File, name string) (*runtime.Package, *index.Decl) {
	if p.Index != nil {
		if d, ok := p.Index.Lookup(name); ok && d.Kind == index.FuncDecl {
			return p, d
		}
	}
	if ip, d, ok := dotImportDecl(p, file, name); ok && d.Kind == index.FuncDecl {
		return ip, d
	}
	return nil, nil
}

// funcResultType resolves a FuncDecl's resIdx'th result typedef,
// resolved in the declaring file's scope.
func (r *REPL) funcResultType(p *runtime.Package, fd *index.Decl, resIdx int) *runtime.TypeDef {
	fn := fd.Func
	if fn == nil || fn.Type == nil || fn.Type.Results == nil {
		return nil
	}
	i := 0
	for _, fld := range fn.Type.Results.List {
		n := len(fld.Names)
		if n == 0 {
			n = 1
		}
		if resIdx < i+n {
			ctx := &runtime.TypeDef{Pkg: p, File: fd.File}
			return r.typeOfWithPtrs(ctx, fld.Type)
		}
		i += n
	}
	return nil
}

// fieldDeclType resolves a field's declared typedef, walking embeds
// like structFieldsOf does for enumeration.
func (r *REPL) fieldDeclType(td *runtime.TypeDef, name string, seen map[*runtime.TypeDef]bool) *runtime.TypeDef {
	// `h.U` on a `*Holder` — auto-deref like Go's selector does.
	for i := 0; i < 8 && td != nil && td.Kind == runtime.KindPointer; i++ {
		td = td.Elem
	}
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	fts, _ := r.engine.FieldTypes(td)
	for i, fname := range td.Fields {
		if fname == name && i < len(fts) {
			return fts[i]
		}
	}
	for _, spec := range td.EmbedSpecs {
		emb, err := r.engine.ResolveTypeRef(td, spec)
		if err != nil || emb == nil {
			continue
		}
		if et := r.fieldDeclType(emb, name, seen); et != nil {
			return et
		}
	}
	return nil
}

// fileImportRef finds the file's import ref bound to a name — the
// alias, or the imported package's own clause name.
func fileImportRef(p *runtime.Package, file *syntax.File, name string) *runtime.ImportRef {
	for _, ref := range p.Imports[file] {
		if ref.Alias == name || (ref.Alias == "" && ref.PackageName() == name) {
			return ref
		}
	}
	return nil
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
		td := c.r.engine.PeelAliasTd(b.Typ)
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
		// a typed nil pointer (`var p *T`) holds no live pointee —
		// its fields still come from the declared element type.
		td := b.Typ
		for i := 0; i < 8 && td != nil && td.Kind == runtime.KindPointer; i++ {
			et, err := c.r.engine.ElemOf(td)
			if err != nil || et == nil {
				td = nil
				break
			}
			td = et
		}
		switch {
		case td == nil:
		case td.HostNew != nil:
			c.hostMembers(td.HostNew(), out)
		case td.Kind == runtime.KindStruct:
			c.structFields(td, out)
		}
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

// mapKeys enumerates the live map's string keys — the completion inside
// `m["..."`. Only string-valued keys surface: an int- or
// interface-keyed map simply has fewer to offer, and a nil or non-map
// base offers nothing. Names are spliced inside the literal, so a
// double-quoted key is escaped with strconv.Quote's inner text while a
// raw literal takes the key verbatim.
func (c *completer) mapKeys(v runtime.Value, quote byte, out *[]Candidate) {
	for i := 0; i < 8; i++ {
		switch b := v.(type) {
		case *runtime.Named:
			v = runtime.Unwrap(v)
			continue
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			dv, ok := runtime.Deref(v)
			if !ok {
				return
			}
			v = dv
			continue
		case *runtime.Map:
			disp, _ := b.SnapshotKeys()
			for _, k := range disp {
				s, ok := k.(string)
				if !ok {
					// an interface-keyed map hashes on (dynamic type,
					// value): a `K("id")` entry is not found by the
					// string literal `"id"` the splice would write.
					if b.IfaceKey {
						continue
					}
					s, ok = mapKeyString(k)
					if !ok {
						continue
					}
				}
				name := s
				if quote == '"' {
					if q := strconv.Quote(s); len(q) >= 2 {
						name = q[1 : len(q)-1]
					}
				}
				// keys are arbitrary strings, not identifiers — keep
				// the dedup but skip add's identifier gate, which
				// would drop `__x` and the empty key.
				if !c.seen[name] {
					c.seen[name] = true
					*out = append(*out, Candidate{Name: name, Kind: CandConst, Detail: "map key"})
				}
			}
			return
		default:
			return
		}
	}
}

// mapKeyString unwraps a map key to its string when it has one —
// `type K string` keys arrive Named-wrapped.
func mapKeyString(k runtime.Value) (string, bool) {
	s, ok := runtime.Unwrap(k).(string)
	return s, ok
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
		for name, m := range c.r.engine.MethodFuncs(td, ptr) {
			c.add(out, name, CandMethod, funcDetail(m))
		}
	}
	if td.HostNew != nil {
		hv := td.HostNew()
		if !ptrRecv {
			// `T.M` is a method expression — bound struct reps mint
			// pointers (*bytes.Buffer) whose method set would leak
			// pointer receivers; enumerate the modeled type's set.
			rv := reflect.ValueOf(hv)
			for rv.IsValid() && rv.Kind() == reflect.Pointer {
				rv = rv.Elem()
			}
			if rv.IsValid() {
				hv = rv.Interface()
			}
		}
		c.hostMembers(hv, out)
	}
}

// structFields enumerates declared and promoted (embedded) fields.
func (c *completer) structFields(td *runtime.TypeDef, out *[]Candidate) {
	c.structFieldsOf(td, out, map[*runtime.TypeDef]int{}, 0)
}

// structFieldsOf is structFields with a visited-depth set — recursive embeds
// (`type Node struct{ *Node }`) are legal Go and must terminate. depth
// counts promotion hops so same-depth providers flag Ambiguous.
func (c *completer) structFieldsOf(td *runtime.TypeDef, out *[]Candidate, seenAt map[*runtime.TypeDef]int, depth int) {
	if td == nil {
		return
	}
	if prev, ok := seenAt[td]; ok && depth > prev {
		// already offered through a shallower path — a deeper reach
		// adds nothing; its members lose to the shallow offering.
		return
	}
	// an equal- or shallower-depth reach still walks: addField marks
	// each member ambiguous on a tie, or swaps Detail on a shadow.
	seenAt[td] = depth
	fts, _ := c.r.engine.FieldTypes(td)
	for i, name := range td.Fields {
		var detail string
		if i < len(fts) && fts[i] != nil {
			detail = runtime.DisplayName(fts[i])
		}
		c.addField(out, name, detail, depth)
	}
	for _, spec := range td.EmbedSpecs {
		emb, err := c.r.engine.ResolveTypeRef(td, spec)
		if err != nil || emb == nil {
			continue
		}
		c.structFieldsOf(emb, out, seenAt, depth+1)
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
		if vs, ok := d.Spec.(*ast.ValueSpec); ok {
			if td := c.r.inferInitType(p, d, vs, 4); td != nil {
				return runtime.DisplayName(td)
			}
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
		dir = filepath.Join(r.engine.Cwd, dirPart)
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

// CompleteCommandArg is [REPL.CompleteToken] for meta-command lines,
// which Complete and CompleteToken leave uncompleted. It completes the
// command's argument with the cursor at end of line:

//   - `:load <path>` offers .go files and directories (a directory ends
//     in "/" so the next completion descends); `:unload <ref>` offers the
//     current loads.
//   - `:cd <ref>` and `:ls <ref>` offer package refs: session import
//     names, import paths, and `./`, `../` or `/abs` directories.
//   - `:doc <ref>` offers the same, and `name.Sym` after a session
//     import name completes the package's members.
//   - `:comp <code>` completes its argument as code, like the prompt.
//
// start is the byte offset every candidate splices over; other commands
// yield no candidates.
func (r *REPL) CompleteCommandArg(line string) (start int, cands []Candidate) {
	cmd, arg, ok := strings.Cut(line, " ")
	if !ok {
		return len(line), nil
	}
	arg = strings.TrimLeft(arg, " \t")
	start = len(line) - len(arg)
	switch cmd {
	case ":comp":
		s, cands := r.complete(arg)
		return start + s, cands
	case ":doc":
		if head, _, ok := strings.Cut(arg, "."); ok && head != "" {
			if _, isImport := r.ImportPathOf(head); isImport {
				s, cands := r.complete(arg)
				return start + s, cands
			}
		}
	case ":load", ":unload", ":cd", ":ls", ":imports", ":importers":
	default:
		return len(line), nil
	}
	rest, quoted := strings.CutPrefix(arg, `"`)
	if quoted {
		if strings.Contains(rest, `"`) {
			return len(line), nil // past a closed literal
		}
		arg, start = rest, start+1
	}
	switch cmd {
	case ":load":
		cands = r.loadPathCandidates(arg)
	case ":unload":
		cands = r.loadedCandidates(arg)
	default:
		cands = r.pkgRefCandidates(arg, quoted)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Name < cands[j].Name })
	return start, cands
}

// pkgRefCandidates lists the package refs :cd, :ls and :doc accept for
// a typed prefix: directories for a `.`- or `/`-led prefix, otherwise
// import paths plus — unquoted — the names session imports bind.
func (r *REPL) pkgRefCandidates(prefix string, quoted bool) []Candidate {
	if strings.HasPrefix(prefix, ".") || strings.HasPrefix(prefix, "/") {
		return r.dirImportCandidates(prefix)
	}
	out := r.importCandidates(prefix)
	if quoted {
		return out
	}
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.Name] = true
	}
	if f := r.file(); f != nil {
		for name, ref := range r.pkg.Scopes[f] {
			if strings.HasPrefix(name, prefix) && !seen[name] {
				out = append(out, Candidate{Name: name, Kind: CandPackage, Detail: ref.Path})
			}
		}
	}
	return out
}

// loadPathCandidates lists the .go files and directories matching a
// typed :load path, which anchors like :load itself — at the engine's
// start directory unless absolute. Dot entries show only once the typed
// name starts with a dot.
func (r *REPL) loadPathCandidates(prefix string) []Candidate {
	dirPart := prefix[:strings.LastIndex(prefix, "/")+1] // "", "./", "../x/", "/abs/"
	dir := r.anchor(dirPart)
	if dir == "" {
		dir = "."
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	hidden := strings.HasPrefix(prefix[len(dirPart):], ".")
	var out []Candidate
	for _, e := range ents {
		name := e.Name()
		full := dirPart + name
		if !strings.HasPrefix(full, prefix) || (strings.HasPrefix(name, ".") && !hidden) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		switch {
		case isDir:
			out = append(out, Candidate{Name: full + "/", Kind: CandDir})
		case strings.HasSuffix(name, ".go"):
			out = append(out, Candidate{Name: full, Kind: CandFile})
		}
	}
	return out
}

// loadedCandidates lists the current loads matching a typed :unload
// ref, spelled the way the prefix is: absolute for a "/" prefix,
// otherwise relative to the engine's start directory ("./"-led when the
// prefix is). A load outside the start directory is always absolute.
func (r *REPL) loadedCandidates(prefix string) []Candidate {
	var out []Candidate
	for _, u := range r.loads {
		name := u.origin
		if rel, err := filepath.Rel(r.engine.Cwd, name); err == nil && r.engine.Cwd != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(prefix) {
			name = rel
			if strings.HasPrefix(prefix, "./") {
				name = "./" + rel
			}
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		kind := CandFile
		if u.dir {
			kind = CandDir
		}
		out = append(out, Candidate{Name: name, Kind: kind, Detail: "loaded"})
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
	for path := range r.engine.Binds {
		add(path, "bound")
	}
	// stdlib: walk GOROOT/src the same way `go list std` would — dirs
	// holding non-main Go packages, minus trees user code cannot import.
	if goroot := build.Default.GOROOT; goroot != "" {
		stdlibCandidates(filepath.Join(goroot, "src"), add)
	}
	// the module: its own packages (modulePath/rel) and its requires —
	// the spellings go.mod actually lets you import.
	type moduleLocator interface {
		RootDir() string
		ModulePath() string
		Requires() map[string]string
	}
	if loc, ok := r.engine.Resolver.(moduleLocator); ok {
		for mod := range loc.Requires() {
			add(mod, "module")
		}
		root, mod := loc.RootDir(), loc.ModulePath()
		if mod != "" {
			root = resolveWalkRoot(root)
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

// stdlibCandidates walks a GOROOT/src tree the same way `go list std`
// would — every dir holding non-main Go files names an importable
// package, minus the trees user code cannot reach — and hands each
// path to add.
func stdlibCandidates(src string, add func(path, detail string)) {
	src = resolveWalkRoot(src)
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

// resolveWalkRoot resolves a WalkDir root through symlinks. WalkDir
// Lstats its root, so a root that is itself a link is reported as a
// lone file and never descends — distro splits like Debian's
// /usr/lib/go-1.26/src -> ../../share/go-1.26/src would enumerate
// nothing. An unresolvable root is returned unchanged and WalkDir
// reports the error to the callback, like before.
func resolveWalkRoot(root string) string {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real
	}
	return root
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
