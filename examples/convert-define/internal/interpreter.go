// Package internal contains the core logic for the convert-define tool.
package internal

import (
	"context"
	"fmt"
	"go/ast"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	goscan "github.com/podhmo/go-scan"
	"github.com/podhmo/go-scan/examples/convert/model"
	"github.com/podhmo/go-scan/scanner"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

const definePkgPath = "github.com/podhmo/minigo/examples/convert-define/define"

// Runner manages the execution of a minigo script for conversion definitions.
// The engine is a minigo stack-VM interpreter; the define calls arrive as
// quoted special-form calls, so the DSL file's body is inspected as AST,
// never evaluated.
type Runner struct {
	scanner *goscan.Scanner
	pkg     *runtime.Package // the loaded define file's package

	// resolver, when non-nil, is installed on the minigo engine — a test
	// hook for observing (or stubbing) package resolution.
	resolver resolve.Resolver

	Info *model.ParsedInfo
}

// NewRunner creates a new interpreter runner.
func NewRunner(scannerOpts ...goscan.ScannerOption) (*Runner, error) {
	scanner, err := goscan.New(scannerOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating scanner: %w", err)
	}

	r := &Runner{
		scanner: scanner,
		Info: &model.ParsedInfo{
			Imports:           make(map[string]string),
			Structs:           make(map[string]*model.StructInfo),
			ConversionPairs:   []model.ConversionPair{},
			GlobalRules:       []model.TypeRule{},
			ProcessedPackages: make(map[string]bool),
		},
	}
	return r, nil
}

// Scanner returns the scanner instance.
func (r *Runner) Scanner() *goscan.Scanner {
	return r.scanner
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
	// Param 1 is dst, Param 2 is src (after skipping config)
	dstTypeExpr := fnLit.Type.Params.List[1].Type
	srcTypeExpr := fnLit.Type.Params.List[2].Type

	// The types will be *ast.StarExpr, we need to get the underlying type expr.
	if star, ok := dstTypeExpr.(*ast.StarExpr); ok {
		dstTypeExpr = star.X
	} else {
		return nil, ctx.Errorf(call.Call, "destination type in mapping function must be a pointer")
	}
	if star, ok := srcTypeExpr.(*ast.StarExpr); ok {
		srcTypeExpr = star.X
	} else {
		return nil, ctx.Errorf(call.Call, "source type in mapping function must be a pointer")
	}

	srcType, err := r.resolveTypeFromExpr(ctx, srcTypeExpr)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve source type from mapping function: %v", err)
	}
	r.ensureStructInfo(srcType)

	dstType, err := r.resolveTypeFromExpr(ctx, dstTypeExpr)
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

	ast.Walk(walker, fnLit.Body)
	if walker.err != nil {
		return nil, ctx.Errorf(call.Call, "error while parsing mapping function: %v", walker.err)
	}

	r.Info.ConversionPairs = append(r.Info.ConversionPairs, pair)
	slog.Info("registered conversion pair", "src", pair.SrcTypeName, "dst", pair.DstTypeName)

	return runtime.NIL, nil
}

// ensureStructInfo checks if a model.StructInfo exists for the given scanner.TypeInfo,
// creating it from the scanner info if it doesn't.
func (r *Runner) ensureStructInfo(typeInfo *scanner.TypeInfo) {
	if _, exists := r.Info.Structs[typeInfo.Name]; exists {
		return
	}
	if typeInfo.Struct == nil {
		return
	}

	slog.Debug("creating new model.StructInfo", "name", typeInfo.Name)
	structInfo := &model.StructInfo{
		Name: typeInfo.Name,
		Type: typeInfo,
	}
	for _, f := range typeInfo.Struct.Fields {
		fieldInfo := model.FieldInfo{
			Name:         f.Name,
			OriginalName: f.Name,
			FieldType:    f.Type,
			ParentStruct: structInfo,
		}
		structInfo.Fields = append(structInfo.Fields, fieldInfo)
	}
	r.Info.Structs[typeInfo.Name] = structInfo
}

// resolveTypeFromExpr resolves a type expression to a scanner.TypeInfo.
func (r *Runner) resolveTypeFromExpr(ctx runtime.SpecialContext, expr ast.Expr) (*scanner.TypeInfo, error) {
	if cl, ok := expr.(*ast.CompositeLit); ok {
		expr = cl.Type
	}
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil, fmt.Errorf("expected a selector expression (pkg.Type), but got %T", expr)
	}
	sym, err := ctx.ResolveSymbol(selector)
	if err != nil {
		return nil, err
	}
	pkgPath, typeName := sym.PackagePath, sym.Name

	pkgInfo, err := r.scanner.ScanPackageFromImportPath(context.Background(), pkgPath)
	if err != nil {
		return nil, fmt.Errorf("could not scan package %q: %w", pkgPath, err)
	}

	for _, t := range pkgInfo.Types {
		if t.Name == typeName {
			return t, nil
		}
	}
	return nil, fmt.Errorf("type %q not found in package %q", typeName, pkgPath)
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
	pkgInfo, err := r.scanner.ScanPackageFromImportPath(gctx, pkgPath)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not scan package %q: %v", pkgPath, err)
	}
	var foundFunc *scanner.FunctionInfo
	for _, f := range pkgInfo.Functions {
		if f.Name == funcName {
			foundFunc = f
			break
		}
	}
	if foundFunc == nil {
		return nil, ctx.Errorf(call.Call, "function %q not found in package %q", funcName, pkgPath)
	}
	// A valid rule function has at least one parameter and exactly one result.
	// The source type is the last parameter.
	if len(foundFunc.Parameters) == 0 || len(foundFunc.Results) != 1 {
		return nil, ctx.Errorf(call.Call, "rule function %s must have at least one parameter and exactly one result", foundFunc.Name)
	}

	srcField := foundFunc.Parameters[len(foundFunc.Parameters)-1]
	dstField := foundFunc.Results[0]
	srcTypeInfo, err := srcField.Type.Resolve(gctx)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve source type for rule: %v", err)
	}
	dstTypeInfo, err := dstField.Type.Resolve(gctx)
	if err != nil {
		return nil, ctx.Errorf(call.Call, "could not resolve destination type for rule: %v", err)
	}
	if srcTypeInfo == nil && !srcField.Type.IsBuiltin {
		return nil, ctx.Errorf(call.Call, "could not resolve source type definition for rule: %s", srcField.Type.String())
	}
	if dstTypeInfo == nil && !dstField.Type.IsBuiltin {
		return nil, ctx.Errorf(call.Call, "could not resolve destination type definition for rule: %s", dstField.Type.String())
	}

	usingFunc := fmt.Sprintf("%s.%s", pkgIdent.Name, funcName)
	rule := model.TypeRule{
		SrcTypeName: srcField.Type.String(),
		DstTypeName: dstField.Type.String(),
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
