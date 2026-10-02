// Package internal contains the core logic for the convert-define tool.
package internal

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/examples/convert-define/model"
	xinspect "github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

const definePkgPath = "github.com/podhmo/minigo/examples/convert-define/define"
const modelPkgPath = "github.com/podhmo/minigo/examples/convert-define/model"

// Runner manages the execution of a minigo script for conversion definitions.
// The engine is a minigo stack-VM interpreter; the define calls arrive as
// quoted special-form calls, so the DSL file's body is inspected as AST,
// never evaluated. Type information comes from the engine's own lazy
// package loading (engine.Package) viewed through the inspect layer —
// no separate scanner.
type Runner struct {
	engine *minigo.Engine
	pkg    *runtime.Package // the loaded define file's package

	// dirPkg is the package of the define file's directory — bare type
	// names in the DSL resolve here, because the file-loaded package
	// (synthetic "<file>..." path) only holds the define file's decls
	// while the directory package indexes every sibling file.
	dirPkg *runtime.Package

	// resolver, when non-nil, is installed on the minigo engine — a test
	// hook for observing (or stubbing) package resolution.
	resolver resolve.Resolver

	Info *model.ParsedInfo
}

// NewRunner creates a new interpreter runner.
func NewRunner() (*Runner, error) {
	r := &Runner{
		Info: &model.ParsedInfo{
			Imports:         make(map[string]string),
			Structs:         make(map[string]*model.StructInfo),
			ConversionPairs: []model.ConversionPair{},
			GlobalRules:     []model.TypeRule{},
		},
	}
	return r, nil
}

// TypeResolver exposes the engine's lazy package loading as an
// inspect.Resolver so downstream stages (the generator) can chase
// declarations by SymbolID.
func (r *Runner) TypeResolver() xinspect.Resolver {
	return func(sid runtime.SymbolID) (*xinspect.Decl, error) {
		return r.lookupDecl(context.Background(), sid)
	}
}

// lookupDecl resolves a SymbolID to a decl view through the engine:
// source packages report the parsed decl; bound packages (the
// interpreter's compiled-in stdlib) carry no index, so a known member
// is reported as an opaque host decl — the role
// scanner.ExternalTypeOverride played in the vendored pipeline.
//
// Engine.SourceOf could reach the real decl behind the shadow, but this
// consumer deliberately keeps bound stdlib types opaque: a real
// time.Time decl would parse GOROOT source (laziness), answer
// IsStructDecl=true and route same-type field copies into a nonexistent
// convertTimeToTime call, and introspect internals generated code could
// never assign anyway.
func (r *Runner) lookupDecl(gctx context.Context, sid runtime.SymbolID) (*xinspect.Decl, error) {
	if sid.PackagePath == "" || sid.PackagePath == xinspect.BuiltinPackagePath {
		return nil, nil
	}
	p, err := r.engine.Package(gctx, sid.PackagePath)
	if err != nil {
		return nil, err
	}
	if p.Index != nil {
		if td, ok := p.Index.Types[sid.Name]; ok && td.Decl != nil {
			return xinspect.NewDecl(p, td.Decl), nil
		}
		return nil, nil
	}
	if p.Standard {
		return xinspect.NewHostDecl(p, sid.Name, nil, nil), nil
	}
	return nil, nil
}

// PackageName returns the package name of the loaded define file.
func (r *Runner) PackageName() string {
	if r.pkg == nil || len(r.pkg.Files) == 0 {
		return ""
	}
	return r.pkg.Files[0].AST.Name.Name
}

// Run loads and executes the definition script.
func (r *Runner) Run(ctx context.Context, filename string) error {
	slog.InfoContext(ctx, "Executing define script", "filename", filename)
	abs, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("resolving define file %q: %w", filename, err)
	}

	// The engine is anchored at the file's directory so that its module
	// context (go.mod/replace/workspaces) governs lazy import resolution.
	// LoadFile takes the named file as the whole package — DSL files guarded
	// by //go:build codegen do not need their tag mirrored anywhere.
	engine := minigo.NewEngine(filepath.Dir(abs), minigo.WithOutput(os.Stdout))
	if r.resolver != nil {
		engine.WithResolver(r.resolver)
	}
	engine.RegisterSpecial(runtime.SymbolID{PackagePath: definePkgPath, Name: "Convert"}, r.handleConvert)
	engine.RegisterSpecial(runtime.SymbolID{PackagePath: definePkgPath, Name: "Rule"}, r.handleRule)
	r.engine = engine

	pkg, err := engine.LoadFile(ctx, abs)
	if err != nil {
		return fmt.Errorf("loading define file into interpreter: %w", err)
	}
	r.pkg = pkg
	// The generated file lives in the define file's directory, so types
	// of that directory's package are emitted unqualified (and never
	// self-imported). LoadFile's synthetic "<file>..." path is not the
	// import path — resolving the directory itself yields the real one.
	if dirPkg, err := engine.Package(ctx, filepath.Dir(abs)); err == nil && dirPkg != nil {
		r.Info.PackagePath = dirPkg.Path
		r.dirPkg = dirPkg
	}
	if _, err := engine.Call(ctx, pkg, "main"); err != nil {
		return fmt.Errorf("evaluating define file: %w", err)
	}
	return nil
}

func (r *Runner) handleConvert(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
	args := call.Call.Args
	if len(args) != 1 {
		return nil, ctx.Errorf(call.Call, "Convert() expects 1 argument (the mapping function), got %d", len(args))
	}
	fnLit, ok := args[0].(*ast.FuncLit)
	if !ok {
		return nil, ctx.Errorf(call.Call, "argument to Convert() must be a function literal")
	}
	if fnLit.Type == nil || fnLit.Type.Params == nil || len(fnLit.Type.Params.List) != 3 {
		return nil, ctx.Errorf(call.Call, "mapping function must have the signature func(c *Config, dst *DstType, src *SrcType)")
	}

	// Infer types from function signature: func(c *Config, dst *Dst, src *Src)
	// Param 1 is dst, Param 2 is src (after skipping config). The param
	// types are wrapped as inspect TypeExprs in the caller file's context,
	// so `*dst.T` unwraps to a SymbolID through the file's import table.
	dstTE := xinspect.NewTypeExpr(fnLit.Type.Params.List[1].Type, ctx.File(), ctx.Package())
	srcTE := xinspect.NewTypeExpr(fnLit.Type.Params.List[2].Type, ctx.File(), ctx.Package())

	if dstTE.Kind != "StarExpr" {
		return nil, ctx.Errorf(call.Call, "destination type in mapping function must be a pointer")
	}
	if srcTE.Kind != "StarExpr" {
		return nil, ctx.Errorf(call.Call, "source type in mapping function must be a pointer")
	}

	srcType, err := r.resolveTypeExpr(ctx, srcTE)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve source type from mapping function: %v", err)
	}
	r.ensureStructInfo(srcType)

	dstType, err := r.resolveTypeExpr(ctx, dstTE)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve destination type from mapping function: %v", err)
	}
	r.ensureStructInfo(dstType)

	slog.Info("found conversion pair", "src", srcType.Name, "dst", dstType.Name)

	pair := model.ConversionPair{
		SrcTypeName: srcType.Name,
		DstTypeName: dstType.Name,
		SrcTypeInfo: srcType,
		DstTypeInfo: dstType,
		Mapping:     &model.MappingInfo{},
	}

	// Walk the function body to find Map/Convert/Compute calls
	walker := &mappingWalker{
		r:       r,
		ctx:     ctx,
		pair:    &pair,
		srcInfo: r.Info.Structs[model.DeclKey(srcType)],
		dstInfo: r.Info.Structs[model.DeclKey(dstType)],
		dstName: paramName(fnLit.Type.Params.List[1], "dst"),
		srcName: paramName(fnLit.Type.Params.List[2], "src"),
	}
	if walker.srcInfo == nil {
		return nil, ctx.Errorf(call.Call, "source type %s must be a struct", srcType.Name)
	}
	if walker.dstInfo == nil {
		return nil, ctx.Errorf(call.Call, "destination type %s must be a struct", dstType.Name)
	}

	ast.Walk(walker, fnLit.Body)
	if walker.err != nil {
		return nil, ctx.Errorf(call.Call, "error while parsing mapping function: %v", walker.err)
	}

	r.Info.ConversionPairs = append(r.Info.ConversionPairs, pair)
	slog.Info("registered conversion pair", "src", pair.SrcTypeName, "dst", pair.DstTypeName)

	return runtime.NIL, nil
}

// ensureStructInfo materializes a model.StructInfo for a struct decl,
// keyed by the decl's canonical package-qualified identity so that
// same-named types in different packages do not overwrite each other.
func (r *Runner) ensureStructInfo(d *xinspect.Decl) {
	if d == nil || !model.IsStructDecl(d) {
		return
	}
	key := model.DeclKey(d)
	if _, exists := r.Info.Structs[key]; exists {
		return
	}
	structInfo, err := model.StructInfoFromDecl(d)
	if err != nil {
		return
	}
	slog.Debug("creating new model.StructInfo", "name", d.Name)
	r.Info.Structs[key] = structInfo
}

// paramName returns the declared name of a signature parameter,
// defaulting to fallback when the parameter is unnamed.
func paramName(fl *ast.Field, fallback string) string {
	if fl != nil && len(fl.Names) > 0 {
		return fl.Names[0].Name
	}
	return fallback
}

// resolveTypeExpr resolves a type expr to its decl view through the
// engine's package loader: Unref peels any pointer layer, SymbolID
// maps the leaf to {import path, name} without loading the package,
// and only then is the declaring package located.
func (r *Runner) resolveTypeExpr(ctx runtime.SpecialContext, te *xinspect.TypeExpr) (*xinspect.Decl, error) {
	sid, ok := te.Unref().SymbolID()
	if !ok {
		return nil, fmt.Errorf("expected a package-qualified type (pkg.Type), but got %q", te.Text)
	}

	// An unqualified type name resolves in the file-loaded package,
	// whose "<file>..." path is synthetic — chase the declaration in
	// the define file's directory package instead (the same package
	// the generated file joins).
	if r.dirPkg != nil && r.pkg != nil && sid.PackagePath == r.pkg.Path {
		if r.dirPkg.Index != nil {
			if td, ok := r.dirPkg.Index.Types[sid.Name]; ok && td.Decl != nil {
				return xinspect.NewDecl(r.dirPkg, td.Decl), nil
			}
		}
		return nil, fmt.Errorf("type %q not found in package %q", sid.Name, r.dirPkg.Path)
	}

	d, err := r.lookupDecl(context.Background(), sid)
	if err != nil {
		return nil, fmt.Errorf("could not load package %q: %w", sid.PackagePath, err)
	}
	if d == nil {
		return nil, fmt.Errorf("type %q not found in package %q", sid.Name, sid.PackagePath)
	}
	return d, nil
}

func (r *Runner) handleRule(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
	args := call.Call.Args
	if len(args) != 1 {
		return nil, ctx.Errorf(call.Call, "Rule() expects 1 argument, got %d", len(args))
	}
	funcExpr, ok := args[0].(*ast.SelectorExpr)
	if !ok {
		return nil, ctx.Errorf(call.Call, "argument to Rule() must be a function selector (e.g., pkg.Func)")
	}
	pkgIdent, ok := funcExpr.X.(*ast.Ident)
	if !ok {
		return nil, ctx.Errorf(call.Call, "receiver of function selector must be a package identifier")
	}
	sym, err := ctx.ResolveSymbol(funcExpr)
	if err != nil {
		return nil, err
	}
	pkgPath, funcName := sym.PackagePath, sym.Name

	gctx := context.Background()
	p, err := r.engine.Package(gctx, pkgPath)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not load package %q: %v", pkgPath, err)
	}
	var fnDecl *xinspect.Decl
	if p.Index != nil {
		if fd := p.Index.Funcs[funcName]; fd != nil {
			fnDecl = xinspect.NewDecl(p, fd)
		}
	}
	if fnDecl == nil {
		return nil, ctx.Errorf(call.Call, "function %q not found in package %q", funcName, pkgPath)
	}
	sig, err := xinspect.SignatureOf(fnDecl)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "rule function %s has no readable signature: %v", funcName, err)
	}
	params, results := sig.ParamFields(), sig.ResultFields()
	// The generated call site is f(ctx, ec, src): enforce the contract —
	// exactly func(ctx context.Context, ec *model.ErrorCollector, src SrcType) DstType.
	if len(params) != 3 || len(results) != 1 ||
		params[0].Type.CanonicalName() != "context.Context" ||
		params[1].Type.CanonicalName() != "*"+modelPkgPath+".ErrorCollector" {
		return nil, ctx.Errorf(call.Call, "rule function %s must have signature func(ctx context.Context, ec *model.ErrorCollector, src SrcType) DstType", funcName)
	}

	srcTE := params[len(params)-1].Type
	dstTE := results[0].Type
	res := r.TypeResolver()
	srcTypeInfo, err := model.ResolveNamed(res, srcTE)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve source type for rule: %v", err)
	}
	dstTypeInfo, err := model.ResolveNamed(res, dstTE)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve destination type for rule: %v", err)
	}
	if srcTypeInfo == nil && !isBuiltinType(srcTE) {
		return nil, ctx.Errorf(call.Call, "could not resolve source type definition for rule: %s", srcTE.Text)
	}
	if dstTypeInfo == nil && !isBuiltinType(dstTE) {
		return nil, ctx.Errorf(call.Call, "could not resolve destination type definition for rule: %s", dstTE.Text)
	}

	usingFunc := fmt.Sprintf("%s.%s", pkgIdent.Name, funcName)
	rule := model.TypeRule{
		SrcTypeName: srcTE.CanonicalName(),
		DstTypeName: dstTE.CanonicalName(),
		SrcTypeInfo: srcTypeInfo,
		DstTypeInfo: dstTypeInfo,
		UsingFunc:   usingFunc,
	}
	r.Info.GlobalRules = append(r.Info.GlobalRules, rule)
	if _, ok := r.Info.Imports[pkgIdent.Name]; !ok {
		r.Info.Imports[pkgIdent.Name] = pkgPath
	}
	return runtime.NIL, nil
}

// isBuiltinType reports whether the type expr names a predeclared type.
func isBuiltinType(te *xinspect.TypeExpr) bool {
	sid, ok := te.Unref().SymbolID()
	return ok && sid.PackagePath == xinspect.BuiltinPackagePath
}

type mappingWalker struct {
	r       *Runner
	ctx     runtime.SpecialContext
	pair    *model.ConversionPair
	srcInfo *model.StructInfo
	dstInfo *model.StructInfo
	dstName string // name of the dst parameter in the mapping func
	srcName string // name of the src parameter in the mapping func
	err     error
}

func (w *mappingWalker) Visit(node ast.Node) ast.Visitor {
	if w.err != nil || node == nil {
		return nil
	}
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return w
	}
	// Unwrap explicit generic instantiation — c.Map[T](...) and
	// c.Convert[D, S](...) are legal call shapes for the generic
	// methods the go1.27 define API exposes.
	fun := call.Fun
	for {
		switch ix := fun.(type) {
		case *ast.IndexExpr:
			fun = ix.X
		case *ast.IndexListExpr:
			fun = ix.X
		case *ast.ParenExpr:
			fun = ix.X
		default:
			goto unwrapped
		}
	}
unwrapped:
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return w
	}

	switch sel.Sel.Name {
	case "Map":
		w.err = w.parseMapCall(call)
	case "Convert":
		w.err = w.parseConvertCall(call)
	case "Compute":
		w.err = w.parseComputeCall(call)
	}
	return w
}

func (w *mappingWalker) parseMapCall(call *ast.CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("c.Map() expects 2 arguments, got %d", len(call.Args))
	}
	dstName, err := w.fieldAccess(call.Args[0], w.dstName)
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Map(): %w", err)
	}
	srcName, err := w.fieldAccess(call.Args[1], w.srcName)
	if err != nil {
		return fmt.Errorf("could not parse src in c.Map(): %w", err)
	}

	return w.addMap(srcName, dstName, "")
}

func (w *mappingWalker) parseConvertCall(call *ast.CallExpr) error {
	if len(call.Args) != 3 {
		return fmt.Errorf("c.Convert() expects 3 arguments, got %d", len(call.Args))
	}
	dstName, err := w.fieldAccess(call.Args[0], w.dstName)
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Convert(): %w", err)
	}
	srcName, err := w.fieldAccess(call.Args[1], w.srcName)
	if err != nil {
		return fmt.Errorf("could not parse src in c.Convert(): %w", err)
	}
	converter, err := w.exprToString(call.Args[2])
	if err != nil {
		return fmt.Errorf("could not parse converter in c.Convert(): %w", err)
	}
	// The converter must be a function — anything else (a call like
	// define.Rule(fn), a literal, an index expression) would be emitted
	// verbatim and only fail inside the generated code.
	switch call.Args[2].(type) {
	case *ast.SelectorExpr, *ast.Ident, *ast.FuncLit:
	default:
		return fmt.Errorf("converter in c.Convert() must be a function (pkg.Fn, Fn, or a func literal), got %q", converter)
	}

	// Record the converter's package so the generator can qualify it,
	// and check the (ctx, ec, src) contract when its decl resolves.
	if sym, err := w.ctx.ResolveSymbol(call.Args[2]); err == nil && sym.PackagePath != "" {
		if alias, ok := selectorRoot(call.Args[2]); ok {
			if _, exists := w.r.Info.Imports[alias]; !exists {
				w.r.Info.Imports[alias] = sym.PackagePath
			}
		}
		if err := w.checkConverterSig(call.Args[2], sym); err != nil {
			return err
		}
	}

	return w.addMap(srcName, dstName, converter)
}

// checkConverterSig verifies a field converter's signature contract when
// its declaration resolves: exactly func(ctx context.Context,
// ec *model.ErrorCollector, src T) R — the shape the generated call uses.
func (w *mappingWalker) checkConverterSig(expr ast.Expr, sym runtime.SymbolID) error {
	p, err := w.r.engine.Package(context.Background(), sym.PackagePath)
	if err != nil || p == nil || p.Index == nil {
		return nil // unresolvable (host/stdlib) — leave to the compiler
	}
	fd := p.Index.Funcs[sym.Name]
	if fd == nil {
		return nil
	}
	sig, err := xinspect.SignatureOf(xinspect.NewDecl(p, fd))
	if err != nil {
		return nil
	}
	params, results := sig.ParamFields(), sig.ResultFields()
	if len(params) != 3 || len(results) != 1 ||
		params[0].Type.CanonicalName() != "context.Context" ||
		params[1].Type.CanonicalName() != "*"+modelPkgPath+".ErrorCollector" {
		return fmt.Errorf("converter function %s must have signature func(ctx context.Context, ec *model.ErrorCollector, src SrcType) DstType", sym.Name)
	}
	return nil
}

// selectorRoot returns the root identifier of a selector chain
// ("a.b.c" -> "a"), or "" when the expression is not selector-rooted.
func selectorRoot(expr ast.Expr) (string, bool) {
	for {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			break
		}
		expr = sel.X
	}
	id, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// addMap records an explicit field mapping on the pair. Both names are
// field paths relative to the src/dst parameters — a top-level field
// ("ID") or a dotted path into nested structs ("Inner.ID"); nested
// paths are validated here so a bad segment reports at the DSL call
// site. The generator resolves the paths again to type the leaves.
func (w *mappingWalker) addMap(srcPath, dstPath, converter string) error {
	if _, err := model.ResolveFieldPath(w.r.Info, w.r.TypeResolver(), w.srcInfo, srcPath); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if _, err := model.ResolveFieldPath(w.r.Info, w.r.TypeResolver(), w.dstInfo, dstPath); err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	w.pair.Mapping.Maps = append(w.pair.Mapping.Maps, model.FieldMap{
		SrcName:   srcPath,
		DstName:   dstPath,
		Converter: converter,
	})
	slog.Debug("added explicit field map", "src", srcPath, "dst", dstPath, "converter", converter)
	return nil
}

func (w *mappingWalker) parseComputeCall(call *ast.CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("c.Compute() expects 2 arguments, got %d", len(call.Args))
	}
	dstName, err := w.fieldAccess(call.Args[0], w.dstName)
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Compute(): %w", err)
	}
	expr, err := w.exprToString(call.Args[1])
	if err != nil {
		return fmt.Errorf("could not parse expression in c.Compute(): %w", err)
	}

	// A nested destination path ("Inner.X") is validated eagerly so a
	// bad segment reports at the DSL call site.
	if _, err := model.ResolveFieldPath(w.r.Info, w.r.TypeResolver(), w.dstInfo, dstName); err != nil {
		return fmt.Errorf("destination: %w", err)
	}

	computed := model.ComputedField{
		DstName:  dstName,
		Expr:     expr,
		ExprType: w.exprType(call.Args[1]),
	}
	w.registerExprImports(call.Args[1])
	w.pair.Computed = append(w.pair.Computed, computed)
	slog.Debug("added computed field", "dst", dstName, "expr", expr)
	return nil
}

// registerExprImports records the package of every pkg.Name reference
// in a c.Compute expression. The expression is emitted verbatim, so
// without this its packages are imported only if something else
// happened to register them.
func (w *mappingWalker) registerExprImports(e ast.Expr) {
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		alias, ok := selectorRoot(sel)
		if !ok || alias == w.srcName || alias == w.dstName {
			return true
		}
		if sym, err := w.ctx.ResolveSymbol(sel); err == nil && sym.PackagePath != "" {
			if _, exists := w.r.Info.Imports[alias]; !exists {
				w.r.Info.Imports[alias] = sym.PackagePath
			}
		}
		return true
	})
}

// exprType infers a c.Compute expression's type for the shapes whose
// type is knowable without a type checker: a field path rooted at the
// src param (src.A.B), and a call of a non-generic package-level func
// with exactly one result (pkg.F(...)). Anything else returns nil —
// unknown, which never produces a warning.
func (w *mappingWalker) exprType(e ast.Expr) *xinspect.TypeExpr {
	switch e := ast.Unparen(e).(type) {
	case *ast.SelectorExpr:
		path, err := w.fieldAccess(e, w.srcName)
		if err != nil {
			return nil
		}
		chain, err := model.ResolveFieldPath(w.r.Info, w.r.TypeResolver(), w.srcInfo, path)
		if err != nil || len(chain) == 0 {
			return nil
		}
		return chain[len(chain)-1].FieldType
	case *ast.CallExpr:
		sym, err := w.ctx.ResolveSymbol(e.Fun)
		if err != nil {
			return nil
		}
		p, err := w.r.engine.Package(context.Background(), sym.PackagePath)
		if err != nil || p.Index == nil {
			return nil
		}
		fd := p.Index.Funcs[sym.Name]
		if fd == nil || fd.Func == nil || fd.Func.Type.TypeParams != nil {
			return nil // unknown, or a generic result that depends on inference
		}
		sig, err := xinspect.SignatureOf(xinspect.NewDecl(p, fd))
		if err != nil {
			return nil
		}
		if results := sig.ResultFields(); len(results) == 1 {
			return results[0].Type
		}
	}
	return nil
}

// fieldAccess validates that expr is a field selection rooted at the
// given parameter (<root>.<Field> or deeper) and returns the field
// path relative to the root ("Inner.ID" for <root>.Inner.ID).
func (w *mappingWalker) fieldAccess(expr ast.Expr, root string) (string, error) {
	s, err := w.exprToString(expr)
	if err != nil {
		return "", err
	}
	if _, isSel := expr.(*ast.SelectorExpr); !isSel {
		return "", fmt.Errorf("expected a field access like %s.<Field>, got %q", root, s)
	}
	rootIdent, ok := selectorRoot(expr)
	if !ok || rootIdent != root {
		return "", fmt.Errorf("expected a field access like %s.<Field>, got %q", root, s)
	}
	return s[len(root)+1:], nil
}

// exprToString renders any expression back to its source spelling via
// go/printer — c.Compute accepts arbitrary expressions (binary ops,
// literals, index expressions, address-of, ...).
func (w *mappingWalker) exprToString(expr ast.Expr) (string, error) {
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if p := w.ctx.Package(); p != nil && p.Fset != nil {
		fset = p.Fset
	}
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return "", fmt.Errorf("cannot render expression: %w", err)
	}
	return buf.String(), nil
}
