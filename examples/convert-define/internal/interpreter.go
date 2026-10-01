// Package internal contains the core logic for the convert-define tool.
package internal

import (
	"context"
	"fmt"
	"go/ast"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/examples/convert-define/model"
	xinspect "github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

const definePkgPath = "github.com/podhmo/minigo/examples/convert-define/define"

// Runner manages the execution of a minigo script for conversion definitions.
// The engine is a minigo stack-VM interpreter; the define calls arrive as
// quoted special-form calls, so the DSL file's body is inspected as AST,
// never evaluated. Type information comes from the engine's own lazy
// package loading (engine.Package) viewed through the inspect layer —
// no separate scanner.
type Runner struct {
	engine *minigo.Engine
	pkg    *runtime.Package // the loaded define file's package

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
	}

	// Walk the function body to find Map/Convert/Compute calls
	walker := &mappingWalker{
		pair:    &pair,
		srcInfo: r.Info.Structs[srcType.Name],
	}
	if walker.srcInfo == nil {
		return nil, ctx.Errorf(call.Call, "source type %s must be a struct", srcType.Name)
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
// copying the inspect field views (names + TypeExpr) — including the
// struct tags the vendored model accepted but never populated.
func (r *Runner) ensureStructInfo(d *xinspect.Decl) {
	if d == nil || !model.IsStructDecl(d) {
		return
	}
	if _, exists := r.Info.Structs[d.Name]; exists {
		return
	}
	fields, err := xinspect.FieldsOf(d)
	if err != nil {
		return
	}

	slog.Debug("creating new model.StructInfo", "name", d.Name)
	structInfo := &model.StructInfo{
		Name: d.Name,
		Type: d,
	}
	for _, f := range fields {
		names := f.Names
		if len(names) == 0 {
			names = []string{embeddedFieldName(f.Type)}
		}
		jsonTag := ""
		if f.Tag != "" {
			jsonTag = strings.Split(reflect.StructTag("`"+f.Tag+"`").Get("json"), ",")[0]
		}
		for _, name := range names {
			fieldInfo := model.FieldInfo{
				Name:         name,
				OriginalName: name,
				JSONTag:      jsonTag,
				FieldType:    f.Type,
				ParentStruct: structInfo,
			}
			structInfo.Fields = append(structInfo.Fields, fieldInfo)
		}
	}
	r.Info.Structs[d.Name] = structInfo
}

// embeddedFieldName derives the field name of an embedded member from
// its type's leaf name (e.g. `pkg.Base` -> `Base`, `*pkg.Base` -> `Base`).
func embeddedFieldName(te *xinspect.TypeExpr) string {
	if sid, ok := te.Unref().SymbolID(); ok {
		return sid.Name
	}
	return te.Text
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
	params, results := sigFields(sig.Params), sigFields(sig.Results)
	// A valid rule function has at least one parameter and exactly one result.
	// The source type is the last parameter.
	if len(params) == 0 || len(results) != 1 {
		return nil, ctx.Errorf(call.Call, "rule function %s must have at least one parameter and exactly one result", funcName)
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
		SrcTypeName: model.TypeKey(srcTE),
		DstTypeName: model.TypeKey(dstTE),
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

// sigFields unboxes the inspect Sig's runtime slices back into fields —
// each element arrives as a *runtime.GoValue wrapping the *xinspect.Field.
func sigFields(s *runtime.Slice) []*xinspect.Field {
	if s == nil {
		return nil
	}
	out := make([]*xinspect.Field, len(s.Elems))
	for i, e := range s.Elems {
		if gv, ok := e.(*runtime.GoValue); ok {
			out[i], _ = gv.V.(*xinspect.Field)
		}
	}
	return out
}

// isBuiltinType reports whether the type expr names a predeclared type.
func isBuiltinType(te *xinspect.TypeExpr) bool {
	sid, ok := te.Unref().SymbolID()
	return ok && sid.PackagePath == xinspect.BuiltinPackagePath
}

type mappingWalker struct {
	pair    *model.ConversionPair
	srcInfo *model.StructInfo
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
	sel, ok := call.Fun.(*ast.SelectorExpr)
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
	dst, err := w.exprToString(call.Args[0])
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Map(): %w", err)
	}
	src, err := w.exprToString(call.Args[1])
	if err != nil {
		return fmt.Errorf("could not parse src in c.Map(): %w", err)
	}

	dstName := strings.SplitN(dst, ".", 2)[1]
	srcName := strings.SplitN(src, ".", 2)[1]

	return w.setFieldTag(srcName, dstName, "")
}

func (w *mappingWalker) parseConvertCall(call *ast.CallExpr) error {
	if len(call.Args) != 3 {
		return fmt.Errorf("c.Convert() expects 3 arguments, got %d", len(call.Args))
	}
	dst, err := w.exprToString(call.Args[0])
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Convert(): %w", err)
	}
	src, err := w.exprToString(call.Args[1])
	if err != nil {
		return fmt.Errorf("could not parse src in c.Convert(): %w", err)
	}
	converter, err := w.exprToString(call.Args[2])
	if err != nil {
		return fmt.Errorf("could not parse converter in c.Convert(): %w", err)
	}

	dstName := strings.SplitN(dst, ".", 2)[1]
	srcName := strings.SplitN(src, ".", 2)[1]

	return w.setFieldTag(srcName, dstName, converter)
}

func (w *mappingWalker) setFieldTag(srcFieldName, dstFieldName, converter string) error {
	for i := range w.srcInfo.Fields {
		if w.srcInfo.Fields[i].Name == srcFieldName {
			w.srcInfo.Fields[i].Tag.DstFieldName = dstFieldName
			w.srcInfo.Fields[i].Tag.UsingFunc = converter
			slog.Debug("updated field tag", "src", srcFieldName, "dst", dstFieldName, "converter", converter)
			return nil
		}
	}
	return fmt.Errorf("source field %q not found in struct %s", srcFieldName, w.srcInfo.Name)
}

func (w *mappingWalker) parseComputeCall(call *ast.CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("c.Compute() expects 2 arguments, got %d", len(call.Args))
	}
	dst, err := w.exprToString(call.Args[0])
	if err != nil {
		return fmt.Errorf("could not parse dst in c.Compute(): %w", err)
	}
	expr, err := w.exprToString(call.Args[1])
	if err != nil {
		return fmt.Errorf("could not parse expression in c.Compute(): %w", err)
	}

	dstName := strings.SplitN(dst, ".", 2)[1]
	computed := model.ComputedField{
		DstName: dstName,
		Expr:    expr,
	}
	w.pair.Computed = append(w.pair.Computed, computed)
	slog.Debug("added computed field", "dst", dstName, "expr", expr)
	return nil
}

func (w *mappingWalker) exprToString(expr ast.Expr) (string, error) {
	switch n := expr.(type) {
	case *ast.SelectorExpr:
		x, err := w.exprToString(n.X)
		if err != nil {
			return "", err
		}
		return x + "." + n.Sel.Name, nil
	case *ast.Ident:
		return n.Name, nil
	case *ast.CallExpr:
		fun, err := w.exprToString(n.Fun)
		if err != nil {
			return "", err
		}
		var args []string
		for _, arg := range n.Args {
			argStr, err := w.exprToString(arg)
			if err != nil {
				return "", err
			}
			args = append(args, argStr)
		}
		return fmt.Sprintf("%s(%s)", fun, strings.Join(args, ", ")), nil
	default:
		return "", fmt.Errorf("unsupported expression type: %T", expr)
	}
}
