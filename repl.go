package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// REPL is a persistent Read-Eval-Print-Loop session over a scratch package.
// Each input line is classified as a top-level declaration (accumulated for
// later lines) or as statements, which run as a generated step function.
// Names introduced by `x := e`, `var` and `const` inputs are promoted to
// package globals so they persist across lines. `x := e` inside a REPL line
// therefore re-uses an existing global instead of shadowing it — a deliberate
// divergence from Go's scoping rules, matching Python-style REPL semantics.
type REPL struct {
	engine  *Engine // session engine (created fresh by NewREPL)
	pkg     *runtime.Package
	imports []string // import specs, e.g. `"fmt"` or `f "fmt"`
	decls   []string // accumulated func/type declarations
	steps   []string // generated step function sources
	pending []string // globals hoisted by the input being evaluated
	// pendingTyped records `var x T` hoists (name -> declared type) so the
	// cell can be stamped with T's typedef after reload; pendingConsts
	// records const names to seal as read-only once their initializer step
	// has run.
	pendingTyped  []namedExpr
	pendingConsts []string
	n             int
}

// namedExpr pairs a hoisted name with an AST expression (a declared type
// or a const initializer).
type namedExpr struct {
	name string
	expr ast.Expr
}

// NewREPL creates a persistent REPL session on a fresh engine derived from
// the receiver's configuration.
func (e *Engine) NewREPL() *REPL {
	sess := e.NewSession()
	p := &runtime.Package{
		Path:     "<repl>",
		Name:     "repl",
		State:    runtime.Parsed,
		Fset:     sess.fset,
		Globals:  runtime.NewEnv(),
		Scopes:   map[*syntax.File]map[string]*runtime.ImportRef{},
		Imports:  map[*syntax.File][]*runtime.ImportRef{},
		Specials: sess.specials,
	}
	p.Bootstrap = sess.bootstrap
	return &REPL{engine: sess, pkg: p}
}

// Reset clears all accumulated state: values, declarations and imports.
func (r *REPL) Reset() {
	fresh := r.engine.NewREPL()
	*r = *fresh
}

// EvalLine evaluates one REPL input and returns its value. Declaration input
// (imports, func and type declarations) updates the persistent package and
// returns nil. Statement input runs as a generated step function; a trailing
// expression statement becomes the return value. Lines beginning with `:` are
// meta commands handled by the caller, not EvalLine.
func (r *REPL) EvalLine(ctx context.Context, input string) (runtime.Value, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return runtime.NIL, nil
	}
	r.pending = nil
	r.pendingTyped = nil
	r.pendingConsts = nil

	// Snapshot the accumulated source so a post-accept failure can roll
	// back exactly what this input appended.
	il, dl, sl := len(r.imports), len(r.decls), len(r.steps)

	// Classify: does the input parse as top-level declarations?
	fset := token.NewFileSet()
	if sf, err := syntax.ParseFile(fset, "repl-decl.go", []byte("package repl\n"+input)); err == nil {
		step, err := r.acceptDecls(fset, sf.AST)
		if err != nil {
			return nil, err
		}
		if err := r.applyInput(ctx, il, dl, sl); err != nil {
			return nil, err
		}
		if step == "" {
			r.sealConsts()
			return runtime.NIL, nil
		}
		return r.runStep(ctx, step)
	}

	// Otherwise parse the input as function-body statements.
	fset = token.NewFileSet()
	sf, err := syntax.ParseFile(fset, "repl-stmt.go", []byte("package repl\nfunc __s() any {\n"+input+"\n}"))
	if err != nil {
		return nil, fmt.Errorf("repl: cannot parse input: %w", err)
	}
	fn, ok := sf.AST.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return nil, fmt.Errorf("repl: cannot parse input")
	}
	step, err := r.acceptStmts(fset, fn.Body.List)
	if err != nil {
		return nil, err
	}
	if err := r.applyInput(ctx, il, dl, sl); err != nil {
		return nil, err
	}
	if step == "" {
		r.sealConsts()
		return runtime.NIL, nil
	}
	return r.runStep(ctx, step)
}

// applyInput commits the accepted input to the accumulated source and
// finishes bindings that need the fresh index: reload, `var x T` type
// stamps, and blank-import materialization. A failure rolls back the
// source entries and hoisted globals this input added, so a bad line
// (e.g. a blank import of a missing package) does not poison later ones.
func (r *REPL) applyInput(ctx context.Context, il, dl, sl int) error {
	if err := r.reload(); err != nil {
		r.rollbackSource(il, dl, sl)
		return err
	}
	if err := r.stampTyped(ctx); err != nil {
		r.rollbackSource(il, dl, sl)
		return err
	}
	// Blank imports registered after package init need explicit
	// initialization: the synthetic __init__ runs once, on the first
	// reload, before this import existed.
	file := r.pkg.Files[0]
	for _, ref := range r.pkg.Imports[file] {
		if ref.Alias != "_" {
			continue
		}
		p, err := ref.Materialize()
		if err != nil {
			r.rollbackSource(il, dl, sl)
			return err
		}
		if err := p.EnsureReady(); err != nil {
			r.rollbackSource(il, dl, sl)
			return err
		}
	}
	return nil
}

// rollbackSource undoes a failed input: trims the source slices appended
// since (il, dl, sl), drops the globals it hoisted, and re-reloads so the
// package is left consistent.
func (r *REPL) rollbackSource(il, dl, sl int) {
	r.imports = r.imports[:il]
	r.decls = r.decls[:dl]
	r.steps = r.steps[:sl]
	for _, n := range r.pending {
		r.pkg.Globals.Delete(n)
	}
	r.pending = nil
	r.pendingTyped = nil
	r.pendingConsts = nil
	_ = r.reload() // best effort; the original error is the one that matters
}

// stampTyped stamps each hoisted `var x T` cell with T's typedef by
// evaluating `new(T)` against the fresh index — later `x = v` stores
// coerce like Go's declared-type assignment.
func (r *REPL) stampTyped(ctx context.Context) error {
	if len(r.pendingTyped) == 0 {
		return nil
	}
	file := r.pkg.Files[0]
	for _, nt := range r.pendingTyped {
		box, err := r.engine.EvalExpr(ctx, r.pkg, file, &ast.CallExpr{
			Fun:  ast.NewIdent("new"),
			Args: []ast.Expr{nt.expr},
		})
		if err != nil {
			return err
		}
		bc, ok := box.(*runtime.Cell)
		if !ok {
			return fmt.Errorf("repl: unexpected new() result %T", box)
		}
		if gv, ok := r.pkg.Globals.Get(nt.name); ok {
			if c, ok := gv.(*runtime.Cell); ok {
				c.Typ = bc.Typ
			}
		}
	}
	return nil
}

// sealConsts marks the cells of this input's const bindings read-only —
// after their initializer step has run — so `k = v` traps like Go.
func (r *REPL) sealConsts() {
	for _, name := range r.pendingConsts {
		if gv, ok := r.pkg.Globals.Get(name); ok {
			if c, ok := gv.(*runtime.Cell); ok {
				c.ReadOnly = true
			}
		}
	}
	r.pendingConsts = nil
}

// runStep calls a generated step function. When it fails, globals hoisted
// by this input are rolled back — a failed initializer must not leave a
// nil cell that later lines read as defined.
func (r *REPL) runStep(ctx context.Context, name string) (runtime.Value, error) {
	v, err := r.engine.Call(ctx, r.pkg, name)
	if err != nil {
		for _, n := range r.pending {
			r.pkg.Globals.Delete(n)
		}
		return nil, err
	}
	r.sealConsts()
	return v, nil
}

// acceptDecls folds top-level declarations into the REPL state and returns
// the generated step name when the input needs runtime evaluation (var/const
// declarations with values).
func (r *REPL) acceptDecls(fset *token.FileSet, f *ast.File) (string, error) {
	var stepBody []ast.Stmt
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			switch d.Tok {
			case token.IMPORT:
				for _, spec := range d.Specs {
					r.imports = append(r.imports, formatNode(fset, spec))
				}
			case token.VAR, token.CONST:
				stepBody = append(stepBody, r.hoistSpecs(d)...)
			case token.TYPE:
				r.decls = append(r.decls, formatNode(fset, d))
			default:
				return "", fmt.Errorf("repl: unsupported declaration: %s", d.Tok)
			}
		case *ast.FuncDecl:
			r.decls = append(r.decls, formatNode(fset, d))
		case *ast.BadDecl:
			return "", fmt.Errorf("repl: cannot parse declaration")
		default:
			return "", fmt.Errorf("repl: unsupported declaration")
		}
	}
	return r.addStep(fset, stepBody)
}

// hoistSpecs promotes each declared name to a package-global cell and lowers
// the spec to assignments executed as a step. Const names are recorded in
// pendingConsts (sealed read-only after the step) and names with an explicit
// type in pendingTyped (the cell is stamped with T's typedef after reload).
func (r *REPL) hoistSpecs(d *ast.GenDecl) []ast.Stmt {
	var out []ast.Stmt
	isConst := d.Tok == token.CONST
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range vs.Names {
			r.hoist(name.Name)
			if isConst {
				r.pendingConsts = append(r.pendingConsts, name.Name)
			}
			if vs.Type != nil {
				r.pendingTyped = append(r.pendingTyped, namedExpr{name: name.Name, expr: vs.Type})
			}
		}
		if len(vs.Values) > 0 {
			lhs := make([]ast.Expr, len(vs.Names))
			for i, n := range vs.Names {
				lhs[i] = n
			}
			out = append(out, &ast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, Rhs: vs.Values})
			continue
		}
		// `var x T` without a value initializes to a zero value. A value-less
		// const keeps its nil cell and is only sealed read-only.
		if !isConst && vs.Type != nil {
			for _, n := range vs.Names {
				out = append(out, &ast.AssignStmt{
					Lhs: []ast.Expr{n},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{&ast.StarExpr{X: &ast.CallExpr{
						Fun:  ast.NewIdent("new"),
						Args: []ast.Expr{vs.Type},
					}}},
				})
			}
		}
	}
	return out
}

// acceptStmts rewrites a statement list so new `:=`/`var`/`const` names become
// package globals, and turns a trailing expression statement into the step's
// return value.
func (r *REPL) acceptStmts(fset *token.FileSet, body []ast.Stmt) (string, error) {
	out := make([]ast.Stmt, 0, len(body))
	for _, st := range body {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						r.hoist(id.Name)
					}
				}
				s.Tok = token.ASSIGN
			}
			out = append(out, s)
		case *ast.DeclStmt:
			if gd, ok := s.Decl.(*ast.GenDecl); ok && (gd.Tok == token.VAR || gd.Tok == token.CONST) {
				out = append(out, r.hoistSpecs(gd)...)
				continue
			}
			out = append(out, s)
		default:
			out = append(out, s)
		}
	}
	if n := len(out); n > 0 {
		if es, ok := out[n-1].(*ast.ExprStmt); ok {
			out[n-1] = &ast.ReturnStmt{Results: []ast.Expr{es.X}}
		}
	}
	return r.addStep(fset, out)
}

// hoist registers name as a persistent package-global cell, preserving an
// existing entry's value. Newly created cells are recorded in r.pending so
// a failing initializer can roll them back.
func (r *REPL) hoist(name string) {
	if name == "_" {
		return
	}
	if _, ok := r.pkg.Globals.Get(name); ok {
		return
	}
	r.pending = append(r.pending, name)
	r.pkg.Globals.Set(name, &runtime.Cell{Elem: runtime.NIL})
}

// addStep emits `func __stepN() any { <body> }` and returns its name; it
// returns "" when there is nothing to run.
func (r *REPL) addStep(fset *token.FileSet, body []ast.Stmt) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	// the step declares one result: fall through to `return nil` so the
	// implicit OpReturn never pops an empty stack
	if _, ok := body[len(body)-1].(*ast.ReturnStmt); !ok {
		body = append(body, &ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("nil")}})
	}
	r.n++
	name := fmt.Sprintf("__step%d", r.n)
	fn := &ast.FuncDecl{
		Name: ast.NewIdent(name),
		Type: &ast.FuncType{
			Params:  &ast.FieldList{},
			Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("any")}}},
		},
		Body: &ast.BlockStmt{List: body},
	}
	r.steps = append(r.steps, formatNode(fset, fn))
	return name, nil
}

// reload re-parses the accumulated source, keeping Globals and State so
// hoisted values survive.
func (r *REPL) reload() error {
	var b strings.Builder
	b.WriteString("package repl\n")
	if len(r.imports) > 0 {
		b.WriteString("import (\n")
		for _, spec := range r.imports {
			b.WriteString("\t" + spec + "\n")
		}
		b.WriteString(")\n")
	}
	for _, d := range r.decls {
		b.WriteString(d + "\n")
	}
	for _, s := range r.steps {
		b.WriteString(s + "\n")
	}

	sf, err := syntax.ParseFile(r.engine.fset, "repl.go", []byte(b.String()))
	if err != nil {
		return fmt.Errorf("repl: internal error: accumulated source does not parse: %w\n%s", err, b.String())
	}
	idx, err := index.Build([]*syntax.File{sf})
	if err != nil {
		return fmt.Errorf("repl: internal error: %w", err)
	}
	p := r.pkg
	// evict values cached from materialized decls so redefinitions pick
	// up the new bodies: resolution consults Globals before the index.
	// Only decl names holding a bare decl value are evicted — hoisted
	// cells and values assigned under non-decl names are untouched.
	for _, d := range idx.Decls {
		if gv, ok := p.Globals.Get(d.Name); ok {
			switch gv.(type) {
			case *runtime.Function, *runtime.TypeDef:
				p.Globals.Delete(d.Name)
			}
		}
	}
	p.Files = []*syntax.File{sf}
	p.FileByName = map[string]*syntax.File{sf.Name: sf}
	p.Index = idx
	p.Scopes = map[*syntax.File]map[string]*runtime.ImportRef{sf: {}}
	p.Imports = map[*syntax.File][]*runtime.ImportRef{sf: {}}
	for _, imp := range sf.Imports {
		ref := &runtime.ImportRef{
			Path:  imp.Path,
			Alias: imp.Alias,
			Load:  func(path string) (*runtime.Package, error) { return r.engine.loadPath(context.Background(), path) },
		}
		p.Imports[sf] = append(p.Imports[sf], ref)
		if imp.Alias != "_" && imp.Alias != "." {
			p.Scopes[sf][imp.LocalName()] = ref
		}
	}
	return p.EnsureReady()
}

// Display renders a runtime value for REPL output.
func (r *REPL) Display(v runtime.Value) any {
	return display(v)
}

// IncompleteInput reports whether a REPL fragment needs more input: either
// a (), [] or {} group is still open, or the fragment ends on a token where
// Go would not insert a semicolon (a trailing operator, comma, or dot). It
// is a tokenization heuristic for driving a continuation prompt, not a
// parser — a "complete" fragment can still fail to parse or evaluate.
//
// Consequences for an interactive loop: `func f() {`, `x := []int{` and
// `x := 1 +` all read as incomplete; `} else {` keeps `else` attached to
// its `if` only when both arrive inside the same unclosed buffer (as in Go
// source, `else` must share a line with the closing `}`). Unterminated
// raw strings and /* */ comments read as incomplete — they are the two
// constructs Go legitimately continues across lines. Every other
// degenerate fragment (comment-only input, unterminated '"' or rune
// literals, illegal characters, negative depth) reads as complete so its
// error surfaces through EvalLine instead of waiting forever.
func IncompleteInput(src string) bool {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("repl-input.go", -1, len(src))
	var scanErr, continueErr bool
	s.Init(file, []byte(src), func(_ token.Position, msg string) {
		scanErr = true
		// The two errors a later line can fix: an open ` raw string and
		// an open /* comment. '"' strings and rune literals cannot span
		// lines, so their "not terminated" errors stay non-continuable.
		if strings.Contains(msg, "raw string literal not terminated") ||
			strings.Contains(msg, "comment not terminated") {
			continueErr = true
		}
	}, 0)
	depth := 0
	last := token.ILLEGAL
	for {
		_, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
		last = tok
	}
	if depth > 0 || continueErr {
		return true
	}
	if scanErr || last == token.ILLEGAL {
		return false
	}
	// A fragment ending where Go inserts a semicolon (or with an explicit
	// `;`) is complete; anything else may continue on the next line.
	return last != token.SEMICOLON
}

// formatNode prints a single AST node.
func formatNode(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := format.Node(&b, fset, n); err != nil {
		return fmt.Sprintf("/* format error: %s */", err)
	}
	return b.String()
}
