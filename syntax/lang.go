// Language-version gating (-lang), mirroring cmd/compile: a file may use
// only language features its effective language version reaches. The
// effective version is the owning module's `go` directive — REPLACED, not
// just raised, by a //go:build go1.x constraint on the file (floored at
// go1.21, exactly like types2/check.go) — or unbounded for files outside
// any module, matching `go run` on a module-less file.
//
// The gate is deliberately syntactic: it covers constructs a parse tree
// can prove versioned (type-parameter declarations, constraint type-set
// elements, instantiation syntax, generic methods, and calls to the
// versioned builtins when the name is not shadowed package-wide).
// Features gc gates only semantically — `for range` over int/func values,
// new(expr) with a non-literal operand, the 1.27 inference relaxations —
// stay permissive; detecting them needs the type information minigo does
// not compute.
package syntax

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/version"
)

// DeclKind classifies a package-level name for the language gate.
type DeclKind byte

const (
	DeclOther DeclKind = iota // var/const, or undeclared
	DeclType
	DeclFunc
	// DeclGeneric marks decls carrying a type-parameter list —
	// use sites of such names may be implicit instantiations.
	DeclGeneric = 4
)

// DeclaredKinds collects package-level declared names across a package's
// files. It is the shadow table for the predeclared-name checks: a package
// that declares `any`, `min`, or `new` keeps the old meaning, like gc.
func DeclaredKinds(files []*File) map[string]DeclKind {
	m := map[string]DeclKind{}
	for _, f := range files {
		if f.AST == nil {
			continue
		}
		for _, d := range f.AST.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch s := sp.(type) {
					case *ast.TypeSpec:
						k := DeclType
						if s.TypeParams != nil {
							k |= DeclGeneric
						}
						m[s.Name.Name] = k
					case *ast.ValueSpec:
						for _, n := range s.Names {
							m[n.Name] = DeclOther
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					k := DeclFunc
					if d.Type.TypeParams != nil {
						k |= DeclGeneric
					}
					m[d.Name.Name] = k
				}
			}
		}
	}
	return m
}

// Lang reports the file's effective language version ("go1.26"), or "" when
// unbounded. A //go:build go1.x constraint on the file replaces the module
// version outright, floored at go1.21 (so a `go1.19` tag lands on go1.21);
// otherwise the owning module's `go` directive applies.
func (f *File) Lang() string {
	if v := f.AST.GoVersion; v != "" {
		return langMax("go1.21", v)
	}
	return langVersion("go" + f.LangMod)
}

// langSuffix reproduces gc's version-source annotation: the file's own
// //go:build version when it is the binding ceiling ("file declares"),
// otherwise the module's -lang ("check go.mod"), else nothing.
func (f *File) langSuffix() string {
	if gv := f.AST.GoVersion; gv != "" && version.Compare(gv, "go1.21") >= 0 {
		return fmt.Sprintf("(file declares //go:build %s)", gv)
	}
	if f.LangMod != "" {
		return fmt.Sprintf("(-lang was set to go%s; check go.mod)", f.LangMod)
	}
	return ""
}

func langVersion(v string) string {
	if version.IsValid(v) {
		return version.Lang(v)
	}
	return ""
}

func langMax(a, b string) string {
	if version.Compare(a, b) >= 0 {
		return a
	}
	return b
}

// CheckLang reports the first use of a language feature newer than the
// file's effective Lang — the load-time equivalent of gc's -lang rejections
// (e.g. "generic method requires go1.27 or later (-lang was set to go1.26;
// check go.mod)"). An unbounded file (LangMod == "" and no //go:build
// version constraint) is never checked. declared is the package-wide
// shadow table from DeclaredKinds.
func CheckLang(fset *token.FileSet, f *File, declared map[string]DeclKind) error {
	lang := f.Lang()
	if lang == "" {
		return nil
	}
	c := &langChecker{
		fset:     fset,
		lang:     lang,
		suffix:   f.langSuffix(),
		declared: declared,
		locals:   localDecls(f.AST),
		seen:     map[ast.Node]bool{},
	}
	ast.Inspect(f.AST, c.node)
	return c.err
}

type langChecker struct {
	fset     *token.FileSet
	lang     string
	suffix   string
	declared map[string]DeclKind
	// locals records the [decl, scope-end) spans of function-level names so
	// shadowedAt can answer "is this ident a local, not the builtin" —
	// `min := func(...)` in one function does not make another function's
	// builtin `min(...)` call legal, so a flat name set is not enough.
	locals map[string][]declSpan
	// seen marks nodes already gated by the type-grammar walk so the
	// expression pass does not re-flag (or mis-word) them.
	seen map[ast.Node]bool
	err  error
}

// declSpan marks where a locally declared name is in scope: from its
// declaration position to the end of the enclosing scope.
type declSpan struct{ lo, hi token.Pos }

// localDecls collects the function-level declarations of f: signature
// names (receivers, params, named results, type params), := assignments,
// local type/var/const decls, and range/comm-clause variables. gc gates
// versioned predeclared names only when they resolve to the universe —
// a local `min`/`any`/`new` keeps its user meaning.
func localDecls(f *ast.File) map[string][]declSpan {
	m := map[string][]declSpan{}
	// stack entries with a nonzero end open a scope (function bodies,
	// blocks, statement clauses); endOf finds the innermost open scope.
	type frame struct {
		end token.Pos
	}
	var stack []frame
	endOf := func() token.Pos {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].end != 0 {
				return stack[i].end
			}
		}
		return token.NoPos
	}
	add := func(name string, lo, hi token.Pos) {
		if name == "_" || hi == token.NoPos {
			return
		}
		m[name] = append(m[name], declSpan{lo, hi})
	}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		hi := endOf()
		for _, fd := range fl.List {
			for _, n := range fd.Names {
				add(n.Name, n.Pos(), hi)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		switch n := n.(type) {
		case *ast.FuncDecl:
			end := n.End()
			if n.Body != nil {
				end = n.Body.End()
			}
			stack = append(stack, frame{end})
			addFields(n.Recv)
			if n.Type != nil {
				addFields(n.Type.Params)
				addFields(n.Type.Results)
				addFields(n.Type.TypeParams)
			}
			return true
		case *ast.FuncLit:
			stack = append(stack, frame{n.Body.End()})
			addFields(n.Type.Params)
			addFields(n.Type.Results)
			addFields(n.Type.TypeParams)
			return true
		case *ast.RangeStmt:
			stack = append(stack, frame{n.Body.End()})
			if n.Tok == token.DEFINE {
				for _, e := range []ast.Expr{n.Key, n.Value} {
					if id, ok := e.(*ast.Ident); ok {
						add(id.Name, id.Pos(), n.Body.End())
					}
				}
			}
			return true
		case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
			stack = append(stack, frame{n.End()})
			return true
		case *ast.AssignStmt:
			stack = append(stack, frame{0})
			if n.Tok == token.DEFINE {
				hi := endOf()
				for _, e := range n.Lhs {
					if id, ok := e.(*ast.Ident); ok {
						add(id.Name, id.Pos(), hi)
					}
				}
			}
			return true
		case *ast.GenDecl:
			// a decl inside any scope is local (top-level decls live in
			// declared, collected package-wide by DeclaredKinds)
			if hi := endOf(); hi != token.NoPos {
				for _, sp := range n.Specs {
					switch s := sp.(type) {
					case *ast.TypeSpec:
						add(s.Name.Name, s.Pos(), hi)
					case *ast.ValueSpec:
						for _, id := range s.Names {
							add(id.Name, id.Pos(), hi)
						}
					}
				}
			}
			stack = append(stack, frame{0})
			return true
		default:
			stack = append(stack, frame{0})
			return true
		}
	})
	return m
}

// shadowedAt reports whether name resolves to a package-level or
// function-level declaration at pos rather than to a predeclared builtin.
func (c *langChecker) shadowedAt(name string, pos token.Pos) bool {
	if _, ok := c.declared[name]; ok {
		return true
	}
	return c.localAt(name, pos)
}

// localAt reports whether a function-level declaration of name is in
// scope at pos — for the implicit-instantiation check, where the
// package-level generic decl is the target, not a shadow.
func (c *langChecker) localAt(name string, pos token.Pos) bool {
	for _, s := range c.locals[name] {
		if s.lo <= pos && pos < s.hi {
			return true
		}
	}
	return false
}

// typeishIndex reports whether an index expression's argument is
// unambiguously a type — used to gate pkg.F[T] instantiation without
// mistaking package-level indexing (pkg.V[k]) for it. A bare Ident index
// (pkg.F[T] vs pkg.V[k]) is undecidable without imports and is skipped.
func typeishIndex(e ast.Expr) bool {
	switch e.(type) {
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.StarExpr,
		*ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.Ellipsis,
		*ast.StructType, *ast.InterfaceType, *ast.FuncType,
		*ast.UnaryExpr, *ast.BinaryExpr:
		return true
	}
	return false
}

// pkgQualifier reports whether sel's operand can name an imported package:
// a bare identifier no package-level or function-level decl rebinds.
// Field or method selections (p.anchors[k], node.Content[i+1]) are plain
// indexing, never instantiation.
func (c *langChecker) pkgQualifier(sel *ast.SelectorExpr) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && !c.shadowedAt(id.Name, id.Pos())
}

func (c *langChecker) fail(pos token.Pos, minv, feat string) {
	if c.err != nil || version.Compare(minv, c.lang) <= 0 {
		return
	}
	c.err = fmt.Errorf("%s: %s requires %s or later %s", c.fset.Position(pos), feat, minv, c.suffix)
}

func (c *langChecker) render(e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, c.fset, e); err != nil {
		return "element"
	}
	return buf.String()
}

// node is the expression-level Inspect pass: declaration headers,
// call/range/instantiation constructs. Nodes claimed by the type-grammar
// walk (typeExpr) are skipped via seen.
func (c *langChecker) node(n ast.Node) bool {
	if n == nil {
		return true
	}
	if c.err != nil || c.seen[n] {
		return false
	}
	c.seen[n] = true
	switch n := n.(type) {
	case *ast.GenDecl:
		for _, sp := range n.Specs {
			switch s := sp.(type) {
			case *ast.TypeSpec:
				if s.TypeParams != nil && len(s.TypeParams.List) > 0 {
					if s.Assign.IsValid() {
						c.fail(s.TypeParams.Pos(), "go1.23", "generic type alias")
					} else {
						c.fail(s.TypeParams.Pos(), "go1.18", "type parameter")
					}
				}
				c.typeParams(s.TypeParams)
				c.typeExpr(s.Type)
			case *ast.ValueSpec:
				c.typeExpr(s.Type)
			}
		}
	case *ast.FuncDecl:
		c.sigTypes(n.Type, n.Recv != nil)
		c.fieldTypes(n.Recv)
	case *ast.FuncType:
		// a FuncType not reached through a FuncDecl or a type expression
		// (e.g. a func literal's signature)
		c.sigTypes(n, false)
	case *ast.CallExpr:
		c.builtinCall(n)
		// a conversion on a composite type keeps its type in Fun —
		// `[]any(x)` — which the value walk would miss.
		switch n.Fun.(type) {
		case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.StructType,
			*ast.InterfaceType, *ast.Ellipsis:
			c.typeExpr(n.Fun)
		}
		// `Id(1)` on a declared generic func is gc's implicit instantiation —
		// unless a function-level decl rebinds the name at the call site.
		if id, ok := n.Fun.(*ast.Ident); ok &&
			c.declared[id.Name]&DeclGeneric != 0 &&
			c.declared[id.Name]&DeclFunc == DeclFunc &&
			!c.localAt(id.Name, id.Pos()) {
			c.fail(n.Fun.Pos(), "go1.18", "implicit function instantiation")
		}
	case *ast.RangeStmt:
		if lit, ok := n.X.(*ast.BasicLit); ok && lit.Kind == token.INT {
			c.fail(lit.Pos(), "go1.22",
				fmt.Sprintf("cannot range over %s (untyped int constant):", lit.Value))
		}
	case *ast.IndexExpr:
		// F[T] in expression position is ambiguous with indexing — flag
		// only when the declared kind or a clearly-typed index settles it.
		switch x := n.X.(type) {
		case *ast.Ident:
			switch c.declared[x.Name] &^ DeclGeneric {
			case DeclFunc:
				c.fail(n.Pos(), "go1.18", "function instantiation")
			case DeclType:
				c.fail(n.Pos(), "go1.18", "type instantiation")
			}
		case *ast.SelectorExpr:
			// pkg.F[T]: indexing a package member is possible too
			// (pkg.V[k]) — flag only an unmistakable type index. The
			// member kind is unknowable here; call sites are usually
			// functions, so the wording prefers "function".
			if typeishIndex(n.Index) && c.pkgQualifier(x) {
				c.fail(n.Pos(), "go1.18", "function instantiation")
			}
		}
	case *ast.IndexListExpr:
		// F[T, U] is always instantiation; the declared kind picks the
		// wording gc uses.
		feat := "instantiation"
		if id, ok := n.X.(*ast.Ident); ok {
			switch c.declared[id.Name] &^ DeclGeneric {
			case DeclType:
				feat = "type instantiation"
			case DeclFunc:
				feat = "function instantiation"
			}
		} else if _, ok := n.X.(*ast.SelectorExpr); ok {
			feat = "function instantiation"
		}
		c.fail(n.Pos(), "go1.18", feat)
	case *ast.CompositeLit:
		c.typeExpr(n.Type)
	case *ast.TypeAssertExpr:
		c.typeExpr(n.Type)
	}
	return c.err == nil
}

// builtinCall gates calls to versioned predeclared functions when the name
// is not declared package-wide: min/max/clear (go1.21) and the new(expr)
// form (go1.26). new's operand is flagged only when it is unambiguously a
// value — a literal or a computation, never an identifier, which could be
// a type name.
func (c *langChecker) builtinCall(n *ast.CallExpr) {
	id, ok := n.Fun.(*ast.Ident)
	if !ok {
		return
	}
	// type-position arguments are versioned no matter who the callee
	// resolves to: make's first argument is always a type, and new's
	// argument is a type unless it takes the go1.26 value form.
	switch id.Name {
	case "make":
		if len(n.Args) > 0 {
			c.typeExpr(n.Args[0])
		}
	case "new":
		if len(n.Args) == 1 {
			switch n.Args[0].(type) {
			case *ast.BasicLit, *ast.CompositeLit, *ast.CallExpr,
				*ast.BinaryExpr, *ast.UnaryExpr:
				// value form — gated below
			default:
				c.typeExpr(n.Args[0])
			}
		}
	}
	if c.shadowedAt(id.Name, id.Pos()) {
		return
	}
	switch id.Name {
	case "any":
		c.fail(id.Pos(), "go1.18", "predeclared any")
	case "min", "max":
		c.fail(id.Pos(), "go1.21", "built-in "+id.Name)
	case "clear":
		c.fail(id.Pos(), "go1.21", "clear")
	case "new":
		if len(n.Args) != 1 {
			return
		}
		switch arg := n.Args[0].(type) {
		case *ast.BasicLit:
			c.fail(id.Pos(), "go1.26", fmt.Sprintf("new(%s)", arg.Value))
		case *ast.CompositeLit, *ast.CallExpr, *ast.BinaryExpr, *ast.UnaryExpr:
			c.fail(id.Pos(), "go1.26", "new(expr)")
		}
	}
}

// sigTypes gates a signature's type-parameter list, its constraint
// expressions, and its parameter/result types.
func (c *langChecker) sigTypes(ft *ast.FuncType, isMethod bool) {
	if ft == nil {
		return
	}
	if ft.TypeParams != nil && len(ft.TypeParams.List) > 0 {
		if isMethod {
			c.fail(ft.TypeParams.Pos(), "go1.27", "generic method")
		} else {
			c.fail(ft.TypeParams.Pos(), "go1.18", "type parameter")
		}
	}
	c.seen[ft] = true
	c.typeParams(ft.TypeParams)
	c.fieldTypes(ft.Params)
	c.fieldTypes(ft.Results)
}

func (c *langChecker) typeParams(fl *ast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		c.typeElem(f.Type)
	}
}

func (c *langChecker) fieldTypes(fl *ast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		c.typeExpr(f.Type)
	}
}

// typeElem gates one interface element or type-parameter constraint:
// the type-set surface (~T, A|B) is the go1.18 feature gc words as
// "embedding interface element <elem> requires go1.18 or later".
func (c *langChecker) typeElem(e ast.Expr) {
	switch e.(type) {
	case *ast.UnaryExpr, *ast.BinaryExpr:
		c.fail(e.Pos(), "go1.18", "embedding interface element "+c.render(e))
		return
	}
	c.typeExpr(e)
}

// typeExpr walks a type expression: instantiation syntax, the predeclared
// `any` ident, and nested signatures/aggregates. Everything it touches is
// marked seen so the expression pass skips it.
func (c *langChecker) typeExpr(e ast.Expr) {
	if e == nil || c.err != nil || c.seen[e] {
		return
	}
	c.seen[e] = true
	switch t := e.(type) {
	case *ast.Ident:
		if t.Name == "any" && !c.shadowedAt("any", t.Pos()) {
			c.fail(t.Pos(), "go1.18", "predeclared any")
		}
	case *ast.IndexExpr:
		c.fail(t.Pos(), "go1.18", "type instantiation")
		c.typeExpr(t.X)
		c.typeExpr(t.Index)
	case *ast.IndexListExpr:
		c.fail(t.Pos(), "go1.18", "type instantiation")
		c.typeExpr(t.X)
		for _, ix := range t.Indices {
			c.typeExpr(ix)
		}
	case *ast.StarExpr:
		c.typeExpr(t.X)
	case *ast.ParenExpr:
		c.typeExpr(t.X)
	case *ast.ArrayType:
		if t.Len != nil {
			ast.Inspect(t.Len, c.node) // the length is a value expression
		}
		c.typeExpr(t.Elt)
	case *ast.MapType:
		c.typeExpr(t.Key)
		c.typeExpr(t.Value)
	case *ast.ChanType:
		c.typeExpr(t.Value)
	case *ast.Ellipsis:
		c.typeExpr(t.Elt)
	case *ast.StructType:
		c.fieldTypes(t.Fields)
	case *ast.FuncType:
		c.sigTypes(t, false)
	case *ast.InterfaceType:
		if t.Methods == nil {
			return
		}
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				c.typeElem(m.Type)
			} else {
				c.typeExpr(m.Type)
			}
		}
	case *ast.UnaryExpr, *ast.BinaryExpr:
		// ~ or | reached outside an interface element (e.g. `type T ~int`)
		c.fail(e.Pos(), "go1.18", "embedding interface element "+c.render(e))
	}
}
