package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
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
	// entered is the :cd target — a package whose members resolve
	// unqualified through a pseudo dot-import injected at reload.
	entered *runtime.Package
	// writeMode (:pin) publishes each input's new declarations into the
	// entered package's globals — session-scoped monkey-patching.
	// sharedCells tracks repl names bound to the package's own cells so
	// `x = v` writes through and Unpin/Leave can decouple; pinPre saves
	// the repl binding a shared name had before it was aliased (borrowed
	// names are deleted on decouple, pre-existing ones restored).
	// pinnedDecls names the func/type decls published so far — reload's
	// materialization eviction must skip them or `T` and `pkg.T` would
	// diverge into different TypeDef identities.
	writeMode   bool
	sharedCells map[string]*runtime.Cell
	pinPre      map[string]runtime.Value
	pinnedDecls map[string]bool
	// the current input's write-mode records: hoisted names to publish,
	// func/type decl names, and method decls (receiver + name)
	pendingWrite   []string
	pendingDecls   []string
	pendingMethods []methodPatch
	// hasValue marks that the current input ended in an expression
	// statement — only then does EvalLine return the step's value;
	// other inputs (decls, assignments, :=) evaluate for effect only.
	hasValue bool
	// importCands caches the import-path candidate list built for
	// `import "..."` completion — enumerated once per session (newly
	// fetched modules or changed go.mod requires do not refresh it).
	importCands []Candidate
	// loads are the :load units, in load order (repl_load.go);
	// pendingShadow the loaded decls the current input redefined.
	loads         []*loadUnit
	pendingShadow []shadowMark
	// pendingRedecl saves the const cells the current input redeclared
	// (restored if it fails); warnings are the notes the last input
	// produced for the front-end (Warnings).
	pendingRedecl []namedValue
	// pendingUnbind saves load-owned cells the current input's decls
	// took over (shadowLoaded) — restored with the shadow marks.
	pendingUnbind []namedValue
	warnings      []string
	n             int
}

// namedValue pairs a global name with the value it was bound to.
type namedValue struct {
	name  string
	value runtime.Value
}

// namedExpr pairs a hoisted name with an AST expression (a declared type
// or a const initializer).
type namedExpr struct {
	name string
	expr ast.Expr
}

// methodPatch pairs a receiver base name with a method name — a decl to
// graft onto the entered package's type index.
type methodPatch struct {
	recv string
	name string
}

// NewREPL creates a persistent REPL session on a fresh engine derived from
// the receiver's configuration.
func (e *Engine) NewREPL() *REPL {
	sess := e.NewSession()
	p := &runtime.Package{
		Path:     "<repl>",
		Name:     "repl",
		Fset:     sess.fset,
		Globals:  runtime.NewEnv(),
		Scopes:   map[*syntax.File]map[string]*runtime.ImportRef{},
		Imports:  map[*syntax.File][]*runtime.ImportRef{},
		Specials: sess.specials,
		RunInit: func(fn *runtime.Function) error {
			_, err := sess.newVM().Call(fn, nil)
			return err
		},
	}
	p.SetState(runtime.Parsed)
	p.Bootstrap = sess.bootstrap
	return &REPL{engine: sess, pkg: p}
}

// Reset clears all accumulated state: values, declarations and imports.
func (r *REPL) Reset() {
	fresh := r.engine.NewREPL()
	*r = *fresh
}

// EvalLine evaluates one REPL input and returns its value — nil when the
// input produced none. Declaration input (imports, func and type
// declarations) and statements not ending in an expression update the
// persistent package for effect only; a trailing expression statement
// becomes the return value. Lines beginning with `:` are meta commands
// handled by the caller, not EvalLine.
func (r *REPL) EvalLine(ctx context.Context, input string) (runtime.Value, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	r.pending = nil
	r.pendingTyped = nil
	r.pendingConsts = nil
	r.pendingWrite = nil
	r.pendingDecls = nil
	r.pendingMethods = nil
	r.pendingShadow = nil
	r.pendingRedecl = nil
	r.pendingUnbind = nil
	r.warnings = nil
	r.hasValue = false

	// Snapshot the accumulated source so a post-accept failure can roll
	// back exactly what this input appended.
	il, dl, sl := len(r.imports), len(r.decls), len(r.steps)

	// Classify: does the input parse as top-level declarations?
	fset := token.NewFileSet()
	if sf, err := syntax.ParseFile(fset, "repl-decl.go", []byte("package repl\n"+input)); err == nil {
		step, err := r.acceptDecls(ctx, fset, sf.AST)
		if err != nil {
			// decls accepted before the failing one (and loaded decls
			// they shadowed) must not leak into the next reload
			r.rollbackSource(il, dl, sl)
			return nil, err
		}
		if err := r.applyInput(ctx, il, dl, sl); err != nil {
			return nil, err
		}
		if step == "" {
			r.sealConsts()
			r.commitWrites()
			return nil, nil
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
		r.commitWrites()
		return nil, nil
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
	r.dropPending()
	r.pendingTyped = nil
	r.pendingConsts = nil
	r.pendingWrite = nil
	r.pendingDecls = nil
	r.pendingMethods = nil
	for _, m := range r.pendingShadow {
		delete(m.unit.shadowed, m.key)
	}
	for _, nv := range r.pendingUnbind {
		r.pkg.Globals.Set(nv.name, nv.value)
	}
	r.pendingShadow = nil
	r.pendingUnbind = nil
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
		r.dropPending()
		return nil, err
	}
	r.sealConsts()
	r.commitWrites()
	if !r.hasValue {
		return nil, nil
	}
	r.rememberResult(v)
	return v, nil
}

// resultNames hold the latest printed results, newest first — IPython's
// `_`, `__`, `___` (Go's `_` is the blank identifier and cannot be read).
var resultNames = []string{"_1", "_2", "_3"}

// rememberResult shifts _1.._3 and binds the newest result to _1. A
// multi-value result is stored as a []any, since a tuple has no Go
// spelling (`_1[1]` is the second value). Results the REPL does not
// print (no value) are not remembered.
func (r *REPL) rememberResult(v runtime.Value) {
	if v == nil {
		return
	}
	if t, ok := v.(*runtime.Tuple); ok {
		v = &runtime.Slice{Elems: slices.Clone(t.Elems)}
	}
	for i := len(resultNames) - 1; i > 0; i-- {
		if prev, ok := r.pkg.Globals.Get(resultNames[i-1]); ok {
			r.pkg.Globals.Set(resultNames[i], prev)
		}
	}
	r.pkg.Globals.Set(resultNames[0], &runtime.Cell{Elem: v})
}

// commitWrites publishes a successful input's write-mode declarations
// into the entered package. Hoisted names share their cell with the
// package's globals so later reads and `x = v` writes flow both ways;
// func and type decls bind their materialized value — the same object in
// the repl scope, so `F` and `pkg.F` keep one identity for the current
// generation; method decls graft onto the package's type index and the
// materialized typedef is evicted so the next access rebuilds the
// method set with the patch.
func (r *REPL) commitWrites() {
	p := r.entered
	defer func() {
		r.pendingWrite = nil
		r.pendingDecls = nil
		r.pendingMethods = nil
	}()
	if !r.writeMode || p == nil {
		return
	}
	for _, n := range r.pendingWrite {
		if gv, ok := r.pkg.Globals.Get(n); ok {
			p.Globals.Set(n, gv)
			if c, isCell := gv.(*runtime.Cell); isCell {
				r.sharedCells[n] = c
			}
		}
	}
	if len(r.pendingDecls)+len(r.pendingMethods) > 0 {
		r.graftScope(p, r.pkg.Files[0])
	}
	for _, n := range r.pendingDecls {
		d, err := r.engine.lookupDeclIn(r.pkg, n)
		if err != nil || d == nil {
			continue
		}
		v, err := r.engine.materialize(r.pkg, d)
		if err != nil || v == nil || v == runtime.NIL {
			continue
		}
		// the decl moves into the package: retarget its identity so
		// global lookups and SymbolID report the entered package — a
		// patch that keeps Pkg=<repl> would lose the package's own
		// members the moment :cd is left (the pseudo import is gone)
		switch t := v.(type) {
		case *runtime.Function:
			t.Pkg = p
		case *runtime.TypeDef:
			t.Pkg = p
			for _, m := range t.Methods {
				m.Pkg = p
			}
		}
		p.Globals.Set(n, v)
		r.pkg.Globals.Set(n, v)
		// publish the decl into the package's index too — the retargeted
		// Pkg makes SymbolOf/Decls/Symbol consult it, and without an
		// entry the patch's definition is invisible: a new name reads
		// as nil and a patched name resolves back to the ORIGINAL decl.
		if p.Index != nil {
			switch d.Kind {
			case index.FuncDecl:
				p.Index.Funcs[n] = d
			case index.TypeDecl:
				td, ok := p.Index.Types[n]
				if !ok {
					td = &index.TypeDeclInfo{Methods: map[string]*index.Decl{}}
					p.Index.Types[n] = td
				}
				td.Decl = d
			}
			p.Index.Decls = spliceDecl(p.Index.Decls, d)
		}
		if r.pinnedDecls == nil {
			r.pinnedDecls = map[string]bool{}
		}
		r.pinnedDecls[n] = true
	}
	for _, m := range r.pendingMethods {
		src, ok := r.pkg.Index.Types[m.recv]
		if !ok {
			continue
		}
		d, ok := src.Methods[m.name]
		if !ok {
			continue
		}
		if p.Index != nil {
			if td, ok := p.Index.Types[m.recv]; ok && td.Decl != nil {
				if td.Methods == nil {
					td.Methods = map[string]*index.Decl{}
				}
				td.Methods[m.name] = d
				if !r.pinnedDecls[m.recv] {
					// a materialized typedef froze its method set at build
					// time — evict the cached value so the next Member
					// rebuilds it patched. A repl-published type skips the
					// eviction: its shared typedef is patched in place
					// below (evicting would drop the retargeted Pkg).
					p.Globals.Delete(m.recv)
					continue
				}
			}
		}
		// the receiver is not an index type of the entered package
		// (e.g. a type the repl itself just published): graft onto the
		// live typedef instead
		if gv, ok := p.Globals.Get(m.recv); ok {
			if td, ok := gv.(*runtime.TypeDef); ok {
				if td.Methods == nil {
					td.Methods = map[string]*runtime.Function{}
				}
				td.Methods[m.name] = r.engine.methodFunc(p, m.recv, d)
			}
		}
	}
}

// spliceDecl keeps a package's Decls listing coherent across patches:
// a redeclared name replaces the original entry (the patch IS the
// definition now), a new name appends.
func spliceDecl(decls []*index.Decl, d *index.Decl) []*index.Decl {
	for i, old := range decls {
		if old.Name == d.Name && old.Kind == d.Kind {
			decls[i] = d
			return decls
		}
	}
	return append(decls, d)
}

// graftScope registers the repl file's import context inside the
// entered package. Published decls keep their repl syntax.File, and
// name resolution consults pkg.Scopes[file] / pkg.Imports[file] —
// without this their bodies lose every repl import (strings & co).
// The :cd pseudo dot-import back into the package is skipped: the
// package's own names resolve through its globals/index.
func (r *REPL) graftScope(p *runtime.Package, f *syntax.File) {
	if f == nil {
		return
	}
	if p.Scopes == nil {
		p.Scopes = map[*syntax.File]map[string]*runtime.ImportRef{}
	}
	if sc, ok := r.pkg.Scopes[f]; ok && len(sc) > 0 {
		p.Scopes[f] = sc
	}
	if p.Imports == nil {
		p.Imports = map[*syntax.File][]*runtime.ImportRef{}
	}
	for _, ref := range r.pkg.Imports[f] {
		if ref.Alias == "." && ref.Path == p.Path {
			continue
		}
		p.Imports[f] = append(p.Imports[f], ref)
	}
}

// acceptDecls folds top-level declarations into the REPL state and returns
// the generated step name when the input needs runtime evaluation (var/const
// declarations with values).
func (r *REPL) acceptDecls(ctx context.Context, fset *token.FileSet, f *ast.File) (string, error) {
	var stepBody []ast.Stmt
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			switch d.Tok {
			case token.IMPORT:
				var specs []string
				for _, spec := range d.Specs {
					is, ok := spec.(*ast.ImportSpec)
					if !ok {
						continue
					}
					text, err := r.acceptImport(ctx, fset, is)
					if err != nil {
						return "", err
					}
					specs = append(specs, text)
				}
				r.imports = append(r.imports, specs...)
			case token.VAR, token.CONST:
				stepBody = append(stepBody, r.hoistSpecs(d)...)
			case token.TYPE:
				r.shadowLoaded(d)
				r.decls = append(r.decls, formatNode(fset, d))
				if r.writeMode {
					for _, spec := range d.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok {
							r.pendingDecls = append(r.pendingDecls, ts.Name.Name)
						}
					}
				}
			default:
				return "", fmt.Errorf("repl: unsupported declaration: %s", d.Tok)
			}
		case *ast.FuncDecl:
			r.shadowLoaded(d)
			r.decls = append(r.decls, formatNode(fset, d))
			if r.writeMode {
				if d.Recv == nil {
					r.pendingDecls = append(r.pendingDecls, d.Name.Name)
				} else if recv := index.ReceiverTypeName(d.Recv); recv != "" {
					r.pendingMethods = append(r.pendingMethods, methodPatch{recv: recv, name: d.Name.Name})
				}
			}
		case *ast.BadDecl:
			return "", fmt.Errorf("repl: cannot parse declaration")
		default:
			return "", fmt.Errorf("repl: unsupported declaration")
		}
	}
	return r.addStep(fset, stepBody)
}

// acceptImport records one import spec for the rebuilt source. Every
// import resolves eagerly so a typo or a path outside the module's
// requires fails the import line itself rather than the first use.
// Directory refs ("./x", "../x", "/abs/x") — illegal in real Go source
// but natural at a prompt launched from a project root — resolve against
// the engine's start directory. When the import is unaliased and the
// package's declared name differs from the path's last element
// (gopkg.in/yaml.v3 declares yaml; a dir basename may not even be an
// identifier), the spec is rewritten to bind the declared name. The
// package is located and indexed here; its initializers still run on
// first use.
func (r *REPL) acceptImport(ctx context.Context, fset *token.FileSet, spec *ast.ImportSpec) (string, error) {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return "", fmt.Errorf("repl: bad import path: %w", err)
	}
	var p *runtime.Package
	if resolve.LooksLikeDir(path) {
		p, err = r.engine.loadDir(ctx, r.anchor(path))
	} else {
		p, err = r.engine.loadPath(ctx, path)
	}
	if err != nil {
		return "", fmt.Errorf("import %q: %w", path, err)
	}
	// an explicit alias wins over the declared package name, just as Go;
	// a bound package has no package clause — its Name is Bind's guess
	// from the path (`v2` for example.com/foo/v2), so the import keeps
	// Go's path-derived name
	if spec.Name != nil || p.Index == nil || p.Name == "" || p.Name == (&syntax.Import{Path: path}).LocalName() {
		return formatNode(fset, spec), nil
	}
	return p.Name + " " + strconv.Quote(path), nil
}

// anchor resolves a directory-ish import path against the engine's start
// directory — the same root the module resolver anchors to — so
// `import "./x"` means the same thing wherever the host process chdirs.
func (r *REPL) anchor(path string) string {
	if filepath.IsAbs(path) || r.engine.cwd == "" {
		return path
	}
	return filepath.Join(r.engine.cwd, path)
}

// hoistSpecs promotes each declared name to a package-global cell and lowers
// the spec to assignments executed as a step. Const names are recorded in
// pendingConsts (sealed read-only after the step) and names with an explicit
// type in pendingTyped (the cell is stamped with T's typedef after reload).
// Const groups follow Go's rules: a value-less spec repeats the previous
// spec's type and values, and `iota` is the spec's index — every const
// spec's assignment runs inside a block binding `const iota = i`.
func (r *REPL) hoistSpecs(d *ast.GenDecl) []ast.Stmt {
	var out []ast.Stmt
	isConst := d.Tok == token.CONST
	for i, effective := range index.ValueSpecs(d) {
		vs := effective.Spec
		values, typ := effective.Values, effective.Type
		for _, name := range vs.Names {
			r.hoist(name.Name)
			if isConst {
				r.pendingConsts = append(r.pendingConsts, name.Name)
			}
			if typ != nil {
				r.pendingTyped = append(r.pendingTyped, namedExpr{name: name.Name, expr: typ})
			}
		}
		if len(values) > 0 {
			lhs := make([]ast.Expr, len(vs.Names))
			for i, n := range vs.Names {
				lhs[i] = n
			}
			var assign ast.Stmt = &ast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, Rhs: values}
			if isConst {
				assign = &ast.BlockStmt{List: []ast.Stmt{
					&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.CONST, Specs: []ast.Spec{&ast.ValueSpec{
						Names:  []*ast.Ident{ast.NewIdent("iota")},
						Values: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(i)}},
					}}}},
					assign,
				}}
			}
			out = append(out, assign)
			continue
		}
		// `var x T` without a value initializes to a zero value.
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
			r.hasValue = true
		}
	}
	return r.addStep(fset, out)
}

// hoist registers name as a persistent package-global cell, preserving an
// existing entry's value. Newly created cells are recorded in r.pending so
// a failing initializer can roll them back.
//
// In write mode a name that already has a writable cell in the entered
// package is *aliased*: the repl scope binds the package's own cell, so
// `x = v` writes through to the package (OpSetGlobal can only reach the
// executing function's package — sharing the cell is what makes package
// vars patchable). Read-only cells (consts) and non-cell members are not
// aliased — they take a fresh cell that replaces the binding when
// committed.
//
// Every hoist comes from a declaring form (`:=`, `var`, `const`), so a
// name bound to a const is redeclared, not assigned: the newest
// definition wins (as with func redefinition and :load) and a warning
// says so. Under :pin that publishes the new const into the entered
// package — a monkey-patch every importer sees, which the warning names.
// A plain `C = v` still traps like Go.
func (r *REPL) hoist(name string) {
	if name == "_" {
		return
	}
	if r.writeMode && r.entered != nil {
		if gv, ok := r.entered.Globals.Get(name); ok {
			if c, isCell := gv.(*runtime.Cell); isCell && !c.ReadOnly {
				if _, recorded := r.pinPre[name]; !recorded {
					// save the binding being shadowed — but not the
					// package cell itself on a re-hoist
					if old, ok := r.pkg.Globals.Get(name); ok && old != c {
						r.pinPre[name] = old
					}
				}
				r.pkg.Globals.Set(name, c)
				r.sharedCells[name] = c
				// the alias is a loan, not a new global — keep it out of
				// pending so a failed input cannot sever write-through
				r.pendingWrite = append(r.pendingWrite, name)
				return
			}
		}
	}
	if gv, ok := r.pkg.Globals.Get(name); ok {
		c, isCell := gv.(*runtime.Cell)
		if !isCell || !c.ReadOnly {
			return
		}
		r.pendingRedecl = append(r.pendingRedecl, namedValue{name: name, value: gv})
		if pc, ok := r.enteredCell(name); ok && pc == c && r.writeMode {
			r.warnings = append(r.warnings, fmt.Sprintf("const %s redeclared in package %s (was %v): every importer sees the new value", name, r.entered.Path, r.Display(c.Elem)))
		} else {
			r.warnings = append(r.warnings, fmt.Sprintf("const %s redeclared (was %v)", name, r.Display(c.Elem)))
		}
	}
	r.pending = append(r.pending, name)
	c := &runtime.Cell{Elem: runtime.NIL}
	r.pkg.Globals.Set(name, c)
	if r.writeMode && r.entered != nil {
		r.sharedCells[name] = c
		r.pendingWrite = append(r.pendingWrite, name)
	}
}

// enteredCell returns the entered package's cell bound to name, if any.
func (r *REPL) enteredCell(name string) (*runtime.Cell, bool) {
	if r.entered == nil {
		return nil, false
	}
	gv, ok := r.entered.Globals.Get(name)
	if !ok {
		return nil, false
	}
	c, ok := gv.(*runtime.Cell)
	return c, ok
}

// dropPending removes the globals the failed input hoisted and restores
// the const cells it redeclared.
func (r *REPL) dropPending() {
	for _, n := range r.pending {
		r.pkg.Globals.Delete(n)
	}
	for _, nv := range r.pendingRedecl {
		r.pkg.Globals.Set(nv.name, nv.value)
	}
	r.pending = nil
	r.pendingRedecl = nil
	r.warnings = nil
}

// Warnings returns the notes the last EvalLine produced (e.g. a const
// redeclared) — the front-end prints them; they are not errors.
func (r *REPL) Warnings() []string {
	return r.warnings
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
	loaded := r.loadedFiles()
	files := append([]*syntax.File{sf}, loaded...)
	idx, err := index.Build(files)
	if err != nil {
		return fmt.Errorf("repl: internal error: %w", err)
	}
	p := r.pkg
	// Evict values cached from materialized decls so redefinitions pick
	// up the new bodies (resolution consults Globals before the index),
	// and decls a re-:load dropped stop resolving. Only bare decl values
	// are evicted, for names the old or the new index declares: cells
	// belong to the prompt (hoisted names) or to a load unit (whose Load
	// manages them) and survive reloads. Decls published into the entered
	// package under :pin keep their shared binding: evicting would split
	// `T` (fresh typedef) from `pkg.T` (the published one).
	names := map[string]bool{}
	for _, ix := range []*index.Index{p.Index, idx} {
		if ix == nil {
			continue
		}
		for _, d := range ix.Decls {
			names[d.Name] = true
		}
	}
	for name := range names {
		if gv, ok := p.Globals.Get(name); ok && !r.pinnedDecls[name] {
			switch gv.(type) {
			case *runtime.Function, *runtime.TypeDef:
				p.Globals.Delete(name)
			}
		}
	}
	p.Files = files
	p.FileByName = map[string]*syntax.File{}
	for _, f := range files {
		p.FileByName[f.Name] = f
	}
	p.Index = idx
	p.Scopes = map[*syntax.File]map[string]*runtime.ImportRef{}
	p.Imports = map[*syntax.File][]*runtime.ImportRef{}
	// a directory spec recorded by acceptImport (possibly under the
	// package's real name) loads by directory, not import path
	p.Scopes[sf], p.Imports[sf] = r.importRefs(sf, "")
	for _, f := range loaded {
		p.Scopes[f], p.Imports[f] = r.importRefs(f, filepath.Dir(f.Name))
	}
	if r.entered != nil {
		// :cd target — a pseudo dot-import that also admits unexported
		// names so `hiddenFn` resolves like an in-package call.
		p.Imports[sf] = append(p.Imports[sf], &runtime.ImportRef{
			Path:     r.entered.Path,
			Alias:    ".",
			AllNames: true,
			Load: func(path string) (*runtime.Package, error) {
				return r.entered, nil
			},
		})
	}
	return p.EnsureReady()
}

// Enter changes the REPL's resolution scope to the given package — the
// target's members (exported and unexported) resolve unqualified while
// inside, matching the inspect :cd story. ref is an import path
// ("strings", "example.com/mod/pkg") or a directory ("./dir", "/abs/dir").
func (r *REPL) Enter(ctx context.Context, ref string) (*runtime.Package, error) {
	r.Unpin() // write mode is bound to the package it entered
	p, err := r.loadRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	r.entered = p
	if err := r.reload(); err != nil {
		r.entered = nil
		return nil, err
	}
	return p, nil
}

// Leave drops the :cd scope override, returning to plain <repl> lookup.
func (r *REPL) Leave() error {
	if r.entered == nil {
		return nil
	}
	r.Unpin()
	r.entered = nil
	return r.reload()
}

// Pin turns on write mode for the entered package (:cd first): every
// subsequent declaration lands in the package's globals instead of only
// the scratch package — monkey-patching experiments per
// docs/sketch/plan-package-introspection.md. The package is initialized
// at Pin so its var cells exist to alias into the repl scope: `x = v` on
// a package var then writes through. The package object is shared —
// every importer in this engine's session sees the patch.
func (r *REPL) Pin() error {
	if r.entered == nil {
		return fmt.Errorf("pin: not inside a package (:cd first)")
	}
	if r.writeMode {
		return nil
	}
	if err := r.entered.EnsureReady(); err != nil {
		return err
	}
	r.writeMode = true
	r.sharedCells = map[string]*runtime.Cell{}
	r.pinPre = map[string]runtime.Value{}
	for _, name := range r.entered.Globals.Names() {
		gv, _ := r.entered.Globals.Get(name)
		c, ok := gv.(*runtime.Cell)
		if !ok {
			continue
		}
		// a same-named repl binding is shadowed by the alias — save it
		// so Unpin hands the name back to its original owner
		if old, ok := r.pkg.Globals.Get(name); ok {
			r.pinPre[name] = old
		}
		r.pkg.Globals.Set(name, c)
		r.sharedCells[name] = c
	}
	return nil
}

// Pinned reports whether write mode is on.
func (r *REPL) Pinned() bool { return r.writeMode }

// Unpin turns write mode off. What was written stays in the entered
// package; the repl scope decouples — names it owned before the alias
// get their saved bindings back, names borrowed from the package are
// dropped (bare lookup falls back to the :cd dot-import).
func (r *REPL) Unpin() {
	if !r.writeMode {
		return
	}
	r.writeMode = false
	for name, c := range r.sharedCells {
		gv, ok := r.pkg.Globals.Get(name)
		if !ok || gv != c {
			continue
		}
		if old, ok := r.pinPre[name]; ok {
			r.pkg.Globals.Set(name, old)
		} else {
			r.pkg.Globals.Delete(name)
		}
	}
	r.sharedCells = nil
	r.pinPre = nil
	r.pinnedDecls = nil
	r.pendingWrite = nil
	r.pendingDecls = nil
	r.pendingMethods = nil
}

// Current returns the :cd target package, or nil for plain <repl> scope.
func (r *REPL) Current() *runtime.Package {
	return r.entered
}

// List returns one "kind name" line per top-level decl of the given ref
// (or of the entered / scratch package when ref is empty) — :ls output.
// Bound packages enumerate their globals as "host" entries.
func (r *REPL) List(ctx context.Context, ref string) ([]string, error) {
	p := r.entered
	if ref != "" {
		var err error
		p, err = r.loadRef(ctx, ref)
		if err != nil {
			return nil, err
		}
	}
	if p == nil {
		p = r.pkg
	}
	var out []string
	seen := map[string]bool{}
	if p.Index != nil {
		for _, d := range p.Index.Decls {
			if strings.HasPrefix(d.Name, "__") || (d.Kind == index.FuncDecl && d.Name == "init") {
				continue // repl internals (__stepN, __init__); init is unnamable
			}
			var kind string
			switch d.Kind {
			case index.FuncDecl:
				kind = "func"
			case index.VarDecl:
				kind = "var"
			case index.ConstDecl:
				kind = "const"
			default:
				kind = "type"
			}
			if r.pinnedDecls[d.Name] {
				out = append(out, fmt.Sprintf("patch %s", d.Name)) // decl published by :pin
			} else {
				out = append(out, fmt.Sprintf("%s %s", kind, d.Name))
			}
			seen[d.Name] = true
		}
		for name, t := range p.Index.Types {
			for m := range t.Methods {
				out = append(out, fmt.Sprintf("method %s.%s", name, m))
			}
		}
	}
	for _, name := range p.Globals.Names() {
		if strings.HasPrefix(name, "__") || seen[name] {
			continue // index already listed the materialized decl
		}
		if v, ok := p.Globals.Get(name); ok {
			if c, isCell := v.(*runtime.Cell); isCell {
				kind := "var" // hoisted repl name
				if c.ReadOnly {
					kind = "const" // sealed by sealConsts
				}
				out = append(out, fmt.Sprintf("%s %s", kind, name))
			} else if r.pinnedDecls[name] {
				out = append(out, fmt.Sprintf("patch %s", name)) // decl published by :pin
			} else if p.Index == nil || p.Index.Decls == nil {
				out = append(out, fmt.Sprintf("host %s", name))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// BoundPackages lists the session engine's host-bound import paths —
// :bindings output.
func (r *REPL) BoundPackages() []string {
	return r.engine.BoundPackages()
}

// ImportPathOf maps a name bound by a session import (`json` after
// `import "encoding/json"`, `foo` after `import foo "encoding/json"`) to
// the imported path; directory imports come back anchored at the engine's
// start directory. ok is false when no session import binds name.
func (r *REPL) ImportPathOf(name string) (path string, ok bool) {
	file := r.file()
	if file == nil {
		return "", false
	}
	// acceptImport rewrites unaliased imports to bind the declared
	// package name, so the scope is keyed by the name the prompt uses
	ref, ok := r.pkg.Scopes[file][name]
	if !ok {
		return "", false
	}
	if resolve.LooksLikeDir(ref.Path) {
		return r.anchor(ref.Path), true
	}
	return ref.Path, true
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// unquoteRef strips the quotes from a meta-command argument spelled like
// an import path (`:ls "encoding/json"`, `:load "./f.go"`).
func unquoteRef(ref string) string {
	if unq, err := strconv.Unquote(ref); err == nil {
		return unq
	}
	return ref
}

// loadRef resolves a :cd/:ls argument (optionally a quoted path): a name
// bound by a session import stands for its path, an existing directory
// goes through loadDir, and anything else is treated as an import path.
func (r *REPL) loadRef(ctx context.Context, ref string) (*runtime.Package, error) {
	ref = unquoteRef(ref)
	if path, ok := r.ImportPathOf(ref); ok {
		ref = path
	}
	// relative dirs anchor at the engine's start directory, like
	// `import "./x"` and :load — not at the host process's cwd
	if dir := r.anchor(ref); dirExists(dir) {
		return r.engine.loadDir(ctx, dir)
	}
	return r.engine.loadPath(ctx, ref)
}

// Display renders a runtime value for REPL output the way fmt's %v
// does — String/Error methods, &{...} for a pointer to a composite,
// <nil> for nil pointers and interfaces — except that a nil slice or
// map keeps %#v's T(nil) spelling so it reads apart from an empty one.
// A multi-value result renders as (a, b). nil means nothing to print.
func (r *REPL) Display(v runtime.Value) any {
	return r.render(v, "%v")
}

// Dump renders a runtime value in Go syntax like fmt's %#v — type names,
// field names, quoted strings, T(nil) — for :dump. String/Error are not
// consulted (GoString is).
func (r *REPL) Dump(v runtime.Value) any {
	return r.render(v, "%#v")
}

func (r *REPL) render(v runtime.Value, spec string) any {
	if v == nil {
		return nil
	}
	vmm := r.engine.newVM()
	vmm.EnsureProc()
	defer vmm.ReleaseProc()
	format := func(x runtime.Value) string {
		return fmt.Sprintf(spec, &fmtValue{c: vmm, x: x, nilSyntax: true})
	}
	if t, ok := v.(*runtime.Tuple); ok {
		parts := make([]string, len(t.Elems))
		for i, e := range t.Elems {
			parts[i] = format(e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	return format(v)
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
