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
						m[s.Name.Name] = DeclType
					case *ast.ValueSpec:
						for _, n := range s.Names {
							m[n.Name] = DeclOther
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					m[d.Name.Name] = DeclFunc
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
	// seen marks nodes already gated by the type-grammar walk so the
	// expression pass does not re-flag (or mis-word) them.
	seen map[ast.Node]bool
	err  error
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
	case *ast.RangeStmt:
		if lit, ok := n.X.(*ast.BasicLit); ok && lit.Kind == token.INT {
			c.fail(lit.Pos(), "go1.22",
				fmt.Sprintf("cannot range over %s (untyped int constant):", lit.Value))
		}
	case *ast.IndexExpr:
		// F[T] in expression position is ambiguous with indexing — flag
		// only when the declared kind settles it.
		if id, ok := n.X.(*ast.Ident); ok {
			switch c.declared[id.Name] {
			case DeclFunc:
				c.fail(n.Pos(), "go1.18", "function instantiation")
			case DeclType:
				c.fail(n.Pos(), "go1.18", "type instantiation")
			}
		}
	case *ast.IndexListExpr:
		// F[T, U] is always instantiation; the declared kind picks the
		// wording gc uses.
		feat := "instantiation"
		if id, ok := n.X.(*ast.Ident); ok {
			switch c.declared[id.Name] {
			case DeclType:
				feat = "type instantiation"
			case DeclFunc:
				feat = "function instantiation"
			}
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
	if _, shadowed := c.declared[id.Name]; shadowed {
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
		if _, shadowed := c.declared["any"]; t.Name == "any" && !shadowed {
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
