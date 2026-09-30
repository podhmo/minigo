// Package scanner is a slimmed-down vendored copy of go-scan's internal
// scanner. It parses Go source files into PackageInfo/TypeInfo/FunctionInfo
// structures and resolves field types lazily through a PackageResolver.
// Only what convert-define needs is implemented: type and function
// declarations (constants and variables are not collected).
package scanner

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// resolutionCacheKey is used to pass a map for tracking in-progress type resolutions.
type resolutionCacheKey struct{}

// fileParseResult holds the result of parsing a single Go source file.
type fileParseResult struct {
	filePath string
	fileAst  *ast.File
	err      error
}

// Scanner parses Go source files within a package.
type Scanner struct {
	fset                  *token.FileSet
	resolver              PackageResolver
	ExternalTypeOverrides ExternalTypeOverride
	modulePath            string
	moduleRootDir         string
	mu                    sync.Mutex
}

// New creates a new Scanner.
func New(fset *token.FileSet, overrides ExternalTypeOverride, modulePath string, moduleRootDir string, resolver PackageResolver) (*Scanner, error) {
	if fset == nil {
		return nil, fmt.Errorf("fset cannot be nil")
	}
	if overrides == nil {
		overrides = make(ExternalTypeOverride)
	}
	if modulePath == "" || moduleRootDir == "" {
		return nil, fmt.Errorf("modulePath and moduleRootDir must be provided")
	}
	if resolver == nil {
		return nil, fmt.Errorf("resolver cannot be nil")
	}

	return &Scanner{
		fset:                  fset,
		ExternalTypeOverrides: overrides,
		modulePath:            modulePath,
		moduleRootDir:         moduleRootDir,
		resolver:              resolver,
	}, nil
}

// ResolveType starts the type resolution process for a given field type.
func (s *Scanner) ResolveType(ctx context.Context, fieldType *FieldType) (*TypeInfo, error) {
	// This is the start of a resolution chain. Initialize the path in the context.
	ctxWithPath := context.WithValue(ctx, ResolutionPathKey, []string{})
	return fieldType.Resolve(ctxWithPath)
}

// ScanFiles parses a specific list of .go files and returns PackageInfo.
func (s *Scanner) ScanFiles(ctx context.Context, filePaths []string, pkgDirPath string) (*PackageInfo, error) {
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("no files provided to scan for package at %s", pkgDirPath)
	}

	relPath, err := filepath.Rel(s.moduleRootDir, pkgDirPath)
	if err != nil {
		relPath = "."
	}
	importPath := filepath.ToSlash(filepath.Join(s.modulePath, relPath))
	if strings.HasSuffix(importPath, "/.") {
		importPath = importPath[:len(importPath)-2]
	}

	return s.scanGoFiles(ctx, filePaths, pkgDirPath, importPath)
}

// ScanFilesWithKnownImportPath parses files with a predefined import path.
func (s *Scanner) ScanFilesWithKnownImportPath(ctx context.Context, filePaths []string, pkgDirPath string, canonicalImportPath string) (*PackageInfo, error) {
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("no files provided to scan for package at %s", pkgDirPath)
	}
	return s.scanGoFiles(ctx, filePaths, pkgDirPath, canonicalImportPath)
}

func (s *Scanner) scanGoFiles(ctx context.Context, filePaths []string, pkgDirPath string, canonicalImportPath string) (*PackageInfo, error) {
	info := &PackageInfo{
		Path:       pkgDirPath,
		ImportPath: canonicalImportPath,
		ModulePath: s.modulePath,
		ModuleDir:  s.moduleRootDir,
	}
	astFiles := make(map[string]*ast.File)

	// Stage 1: Parallel Parsing
	results := make(chan fileParseResult, len(filePaths))
	g, gCtx := errgroup.WithContext(ctx)

	for _, filePath := range filePaths {
		fp := filePath // create a new variable for the closure
		g.Go(func() error {
			content, err := os.ReadFile(fp)
			if err != nil {
				results <- fileParseResult{filePath: fp, err: fmt.Errorf("reading file: %w", err)}
				return nil
			}

			s.mu.Lock()
			fileAst, err := parser.ParseFile(s.fset, fp, content, parser.ParseComments)
			s.mu.Unlock()

			select {
			case results <- fileParseResult{filePath: fp, fileAst: fileAst, err: err}:
				return nil
			case <-gCtx.Done():
				return gCtx.Err()
			}
		})
	}

	if err := g.Wait(); err != nil {
		close(results)
		return nil, err
	}
	close(results)

	// Stage 2: Collect Results
	parsedFileResults := make([]fileParseResult, 0, len(filePaths))
	for result := range results {
		if result.err != nil {
			return nil, fmt.Errorf("failed to parse file %s: %w", result.filePath, result.err)
		}
		if result.fileAst.Name == nil {
			continue // Skip files with no package name
		}
		parsedFileResults = append(parsedFileResults, result)
	}

	// Stage 3: Filter files by dominant package name
	var dominantPackageName string
	var parsedFiles []*ast.File
	var filePathsForDominantPkg []string

	for _, result := range parsedFileResults {
		currentPackageName := result.fileAst.Name.Name

		if dominantPackageName == "" {
			dominantPackageName = currentPackageName
			parsedFiles = append(parsedFiles, result.fileAst)
			filePathsForDominantPkg = append(filePathsForDominantPkg, result.filePath)
		} else if currentPackageName != dominantPackageName {
			baseDominant := strings.TrimSuffix(dominantPackageName, "_test")
			baseCurrent := strings.TrimSuffix(currentPackageName, "_test")

			if dominantPackageName == "main" && currentPackageName != "main" {
				dominantPackageName = currentPackageName
				var newParsedFiles []*ast.File
				var newFilePaths []string
				for i, p := range parsedFiles {
					if strings.TrimSuffix(p.Name.Name, "_test") != baseDominant {
						newParsedFiles = append(newParsedFiles, p)
						newFilePaths = append(newFilePaths, filePathsForDominantPkg[i])
					}
				}
				parsedFiles = newParsedFiles
				filePathsForDominantPkg = newFilePaths
				parsedFiles = append(parsedFiles, result.fileAst)
				filePathsForDominantPkg = append(filePathsForDominantPkg, result.filePath)
			} else if dominantPackageName != "main" && currentPackageName == "main" {
				continue
			} else if baseDominant == baseCurrent {
				if strings.HasSuffix(dominantPackageName, "_test") && !strings.HasSuffix(currentPackageName, "_test") {
					dominantPackageName = currentPackageName
				}
				parsedFiles = append(parsedFiles, result.fileAst)
				filePathsForDominantPkg = append(filePathsForDominantPkg, result.filePath)
			} else {
				return nil, fmt.Errorf("mismatched package names: %s and %s in directory %s", dominantPackageName, currentPackageName, pkgDirPath)
			}
		} else {
			parsedFiles = append(parsedFiles, result.fileAst)
			filePathsForDominantPkg = append(filePathsForDominantPkg, result.filePath)
		}
	}

	info.Name = dominantPackageName
	info.Files = filePathsForDominantPkg

	// Pass 1: Create placeholders for all type declarations from the filtered files.
	for i, fileAst := range parsedFiles {
		filePath := info.Files[i]
		astFiles[filePath] = fileAst
		for _, decl := range fileAst.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						typeInfo := &TypeInfo{
							Name:     ts.Name.Name,
							PkgPath:  info.ImportPath,
							FilePath: filePath,
							Doc:      commentText(ts.Doc),
							Node:     ts,
						}
						if typeInfo.Doc == "" && d.Doc != nil {
							typeInfo.Doc = commentText(d.Doc)
						}
						info.Types = append(info.Types, typeInfo)
					}
				}
			}
		}
	}

	// Pass 2: Fill in the details for all collected types.
	for _, typeInfo := range info.Types {
		if ts, ok := typeInfo.Node.(*ast.TypeSpec); ok {
			importLookup := s.BuildImportLookup(astFiles[typeInfo.FilePath])
			s.fillTypeInfoFromSpec(ctx, typeInfo, ts, info, importLookup)
		}
	}

	// Pass 3: Process function declarations.
	for i, fileAst := range parsedFiles {
		filePath := info.Files[i]
		importLookup := s.BuildImportLookup(fileAst)
		for _, decl := range fileAst.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok {
				info.Functions = append(info.Functions, s.parseFuncDecl(ctx, f, filePath, info, importLookup))
			}
		}
	}

	if info.Name == "" && len(filePaths) > 0 {
		return nil, fmt.Errorf("could not determine package name from scanned files in %s", pkgDirPath)
	}

	return info, nil
}

// BuildImportLookup creates a map of local import names to their full package paths.
func (s *Scanner) BuildImportLookup(file *ast.File) map[string]string {
	importLookup := make(map[string]string)
	for _, i := range file.Imports {
		path := strings.Trim(i.Path.Value, `"`)
		if i.Name != nil {
			importLookup[i.Name.Name] = path
		} else {
			parts := strings.Split(path, "/")
			importLookup[parts[len(parts)-1]] = path
		}
	}
	return importLookup
}

func (s *Scanner) parseTypeSpec(ctx context.Context, sp *ast.TypeSpec, info *PackageInfo, absFilePath string, importLookup map[string]string) *TypeInfo {
	typeInfo := &TypeInfo{
		Name:     sp.Name.Name,
		PkgPath:  info.ImportPath,
		FilePath: absFilePath,
		Doc:      commentText(sp.Doc),
		Node:     sp,
	}
	s.fillTypeInfoFromSpec(ctx, typeInfo, sp, info, importLookup)
	return typeInfo
}

func (s *Scanner) fillTypeInfoFromSpec(ctx context.Context, typeInfo *TypeInfo, sp *ast.TypeSpec, info *PackageInfo, importLookup map[string]string) {
	// Seed the resolution path with this type's identifier so that field
	// types referring back to it are detected as cycles.
	typeIdentifier := info.ImportPath + "." + sp.Name.Name
	childCtx := context.WithValue(ctx, ResolutionPathKey, []string{typeIdentifier})

	if sp.TypeParams != nil {
		typeInfo.TypeParams = s.parseTypeParamList(childCtx, sp.TypeParams.List, info, importLookup)
	}

	switch t := sp.Type.(type) {
	case *ast.StructType:
		typeInfo.Kind = StructKind
		typeInfo.Struct = s.parseStructType(childCtx, t, typeInfo.TypeParams, info, importLookup)
	case *ast.InterfaceType:
		typeInfo.Kind = InterfaceKind
		typeInfo.Interface = s.parseInterfaceType(childCtx, t, typeInfo.TypeParams, info, importLookup)
	case *ast.FuncType:
		typeInfo.Kind = FuncKind
		funcInfo := s.parseFuncType(childCtx, t, typeInfo.TypeParams, info, importLookup)
		// Propagate the alias's name and package to the underlying FunctionInfo.
		// This is crucial for resolving the TypeInfo of function aliases.
		funcInfo.Name = typeInfo.Name
		funcInfo.PkgPath = typeInfo.PkgPath
		typeInfo.Func = funcInfo
	default:
		typeInfo.Kind = AliasKind
		typeInfo.Underlying = s.TypeInfoFromExpr(childCtx, sp.Type, typeInfo.TypeParams, info, importLookup)
	}
}

func (s *Scanner) parseTypeParamList(ctx context.Context, typeParamFields []*ast.Field, info *PackageInfo, importLookup map[string]string) []*TypeParamInfo {
	var params []*TypeParamInfo
	if typeParamFields == nil {
		return nil
	}
	for _, typeParamField := range typeParamFields {
		var constraintFieldType *FieldType
		if constraintExpr := typeParamField.Type; constraintExpr != nil {
			constraintFieldType = s.TypeInfoFromExpr(ctx, constraintExpr, nil, info, importLookup)
			if constraintFieldType != nil {
				constraintFieldType.IsConstraint = true
			}
		}
		for _, nameIdent := range typeParamField.Names {
			params = append(params, &TypeParamInfo{
				Name:       nameIdent.Name,
				Constraint: constraintFieldType,
			})
		}
	}
	return params
}

// collectUnionTypes recursively traverses a binary expression representing a type union
// (e.g., *Foo | *Bar) and collects all constituent types.
func (s *Scanner) collectUnionTypes(ctx context.Context, expr ast.Expr, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) []*FieldType {
	if binExpr, ok := expr.(*ast.BinaryExpr); ok && binExpr.Op == token.OR {
		// This is a union type, e.g., `A | B`. Recursively collect from both sides.
		leftTypes := s.collectUnionTypes(ctx, binExpr.X, currentTypeParams, info, importLookup)
		rightTypes := s.collectUnionTypes(ctx, binExpr.Y, currentTypeParams, info, importLookup)
		return append(leftTypes, rightTypes...)
	}
	// This is a single type (a leaf in the union expression tree).
	return []*FieldType{s.TypeInfoFromExpr(ctx, expr, currentTypeParams, info, importLookup)}
}

func (s *Scanner) parseInterfaceType(ctx context.Context, it *ast.InterfaceType, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) *InterfaceInfo {
	if it.Methods == nil {
		return &InterfaceInfo{}
	}
	interfaceInfo := &InterfaceInfo{
		Methods:  make([]*MethodInfo, 0),
		Embedded: make([]*FieldType, 0),
		Union:    make([]*FieldType, 0),
	}

	// First pass: determine if this interface uses union syntax at all.
	// The presence of '|' makes it a type set.
	isUnionInterface := false
	for _, field := range it.Methods.List {
		if len(field.Names) == 0 {
			if _, ok := field.Type.(*ast.BinaryExpr); ok {
				isUnionInterface = true
				break
			}
		}
	}

	for _, field := range it.Methods.List {
		if len(field.Names) > 0 { // This is a method definition
			methodName := field.Names[0].Name
			funcType, ok := field.Type.(*ast.FuncType)
			if !ok {
				continue // Should not happen in a valid interface
			}
			methodInfo := &MethodInfo{Name: methodName}
			parsedFuncDetails := s.parseFuncType(ctx, funcType, currentTypeParams, info, importLookup)
			methodInfo.Parameters = parsedFuncDetails.Parameters
			methodInfo.Results = parsedFuncDetails.Results
			interfaceInfo.Methods = append(interfaceInfo.Methods, methodInfo)
		} else { // This is an embedded type or a union term
			if isUnionInterface {
				// If we determined this is a union interface, all non-method fields are terms.
				terms := s.collectUnionTypes(ctx, field.Type, currentTypeParams, info, importLookup)
				interfaceInfo.Union = append(interfaceInfo.Union, terms...)
			} else {
				// Otherwise, it's a regular embedded interface.
				embeddedType := s.TypeInfoFromExpr(ctx, field.Type, currentTypeParams, info, importLookup)
				interfaceInfo.Embedded = append(interfaceInfo.Embedded, embeddedType)
			}
		}
	}
	return interfaceInfo
}

func (s *Scanner) parseStructType(ctx context.Context, st *ast.StructType, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) *StructInfo {
	structInfo := &StructInfo{}
	for _, field := range st.Fields.List {
		fieldType := s.TypeInfoFromExpr(ctx, field.Type, currentTypeParams, info, importLookup)
		var tag string
		if field.Tag != nil {
			tag = strings.Trim(field.Tag.Value, "`")
		}
		doc := commentText(field.Doc)
		if doc == "" {
			doc = commentText(field.Comment)
		}
		if len(field.Names) > 0 {
			for _, name := range field.Names {
				structInfo.Fields = append(structInfo.Fields, &FieldInfo{
					Name:       name.Name,
					Doc:        doc,
					Type:       fieldType,
					Tag:        tag,
					IsExported: name.IsExported(),
				})
			}
		} else {
			structInfo.Fields = append(structInfo.Fields, &FieldInfo{
				Name:     fieldType.Name,
				Doc:      doc,
				Type:     fieldType,
				Tag:      tag,
				Embedded: true,
			})
		}
	}
	return structInfo
}

func (s *Scanner) parseFuncDecl(ctx context.Context, f *ast.FuncDecl, absFilePath string, pkgInfo *PackageInfo, importLookup map[string]string) *FunctionInfo {
	var funcOwnTypeParams []*TypeParamInfo
	if f.Type.TypeParams != nil {
		funcOwnTypeParams = s.parseTypeParamList(ctx, f.Type.TypeParams.List, pkgInfo, importLookup)
	}

	funcInfo := s.parseFuncType(ctx, f.Type, funcOwnTypeParams, pkgInfo, importLookup)
	funcInfo.Name = f.Name.Name
	funcInfo.PkgPath = pkgInfo.ImportPath
	funcInfo.FilePath = absFilePath
	funcInfo.Doc = commentText(f.Doc)
	funcInfo.TypeParams = funcOwnTypeParams

	// After parsing the function signature, walk its body to find and resolve local type declarations.
	if f.Body != nil {
		ast.Inspect(f.Body, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				return true // Continue traversal for non-type declarations.
			}

			// We found a local type declaration block (e.g., `type (...)`).
			// Process it now and perform the special local-alias resolution.
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					typeInfo := s.parseTypeSpec(ctx, ts, pkgInfo, absFilePath, importLookup)
					if typeInfo.Doc == "" && gd.Doc != nil {
						typeInfo.Doc = commentText(gd.Doc)
					}

					// This is the special logic that only runs for local types.
					// Try to link the underlying type's definition immediately.
					if typeInfo.Kind == AliasKind && typeInfo.Underlying != nil && typeInfo.Underlying.PkgName == "" {
						underlyingName := typeInfo.Underlying.TypeName
						if def := pkgInfo.Lookup(underlyingName); def != nil {
							typeInfo.Underlying.Definition = def
							// Also link the element's definition for pointers
							if typeInfo.Underlying.IsPointer && typeInfo.Underlying.Elem != nil {
								if elemDef := pkgInfo.Lookup(typeInfo.Underlying.Elem.TypeName); elemDef != nil {
									typeInfo.Underlying.Elem.Definition = elemDef
								}
							}
						}
					}
					pkgInfo.Types = append(pkgInfo.Types, typeInfo)
				}
			}
			return false // Stop traversal within this GenDecl, as we've processed it.
		})
	}

	if f.Recv != nil && len(f.Recv.List) > 0 {
		recvField := f.Recv.List[0]
		var recvName string
		if len(recvField.Names) > 0 {
			recvName = recvField.Names[0].Name
		}

		var receiverBaseTypeParams []*TypeParamInfo
		parsedRecvFieldType := s.TypeInfoFromExpr(ctx, recvField.Type, funcOwnTypeParams, pkgInfo, importLookup)

		if parsedRecvFieldType != nil {
			baseRecvTypeName := parsedRecvFieldType.Name
			if parsedRecvFieldType.IsPointer && parsedRecvFieldType.Elem != nil {
				baseRecvTypeName = parsedRecvFieldType.Elem.Name
			}
			if parts := strings.Split(baseRecvTypeName, "."); len(parts) > 1 {
				baseRecvTypeName = parts[len(parts)-1]
			}

			if pkgInfo != nil {
				for _, ti := range pkgInfo.Types {
					if ti.Name == baseRecvTypeName {
						receiverBaseTypeParams = ti.TypeParams
						parsedRecvFieldType = s.TypeInfoFromExpr(ctx, recvField.Type, receiverBaseTypeParams, pkgInfo, importLookup)
						break
					}
				}
			}
		}

		funcInfo.Receiver = &FieldInfo{
			Name: recvName,
			Type: parsedRecvFieldType,
		}

		methodScopeTypeParams := append([]*TypeParamInfo{}, receiverBaseTypeParams...)
		methodScopeTypeParams = append(methodScopeTypeParams, funcOwnTypeParams...)

		reparsedFuncSignature := s.parseFuncType(ctx, f.Type, methodScopeTypeParams, pkgInfo, importLookup)
		funcInfo.Parameters = reparsedFuncSignature.Parameters
		funcInfo.Results = reparsedFuncSignature.Results
		funcInfo.TypeParams = methodScopeTypeParams
	}
	return funcInfo
}

func (s *Scanner) parseFuncType(ctx context.Context, ft *ast.FuncType, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) *FunctionInfo {
	funcInfo := &FunctionInfo{}
	if ft.Params != nil {
		funcInfo.Parameters = s.parseFieldList(ctx, ft.Params.List, currentTypeParams, info, importLookup)
		if len(ft.Params.List) > 0 {
			if _, ok := ft.Params.List[len(ft.Params.List)-1].Type.(*ast.Ellipsis); ok {
				funcInfo.IsVariadic = true
			}
		}
	}
	if ft.Results != nil {
		funcInfo.Results = s.parseFieldList(ctx, ft.Results.List, currentTypeParams, info, importLookup)
	}
	return funcInfo
}

func (s *Scanner) parseFieldList(ctx context.Context, fields []*ast.Field, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) []*FieldInfo {
	var result []*FieldInfo
	for _, field := range fields {
		fieldType := s.TypeInfoFromExpr(ctx, field.Type, currentTypeParams, info, importLookup)
		if len(field.Names) > 0 {
			for _, name := range field.Names {
				result = append(result, &FieldInfo{Name: name.Name, Type: fieldType, Doc: commentText(field.Doc)})
			}
		} else {
			result = append(result, &FieldInfo{Type: fieldType, Doc: commentText(field.Doc)})
		}
	}
	return result
}

// buildKey creates a canonical string key for a type expression to detect recursion.
func (s *Scanner) buildKey(expr ast.Expr, pkg *PackageInfo, importLookup map[string]string, currentTypeParams []*TypeParamInfo) string {
	switch n := expr.(type) {
	case *ast.Ident:
		// Check if it's a generic type parameter first.
		for _, p := range currentTypeParams {
			if p.Name == n.Name {
				return n.Name // Type parameters are unique by name within their scope.
			}
		}
		// Assume it's a type in the current package.
		if pkg != nil {
			return pkg.ImportPath + "." + n.Name
		}
		return n.Name
	case *ast.SelectorExpr:
		// Add a nil check for n.X before proceeding.
		if n.X == nil {
			return "." + n.Sel.Name
		}
		if pkgIdent, ok := n.X.(*ast.Ident); ok {
			if pkgPath, ok := importLookup[pkgIdent.Name]; ok {
				return pkgPath + "." + n.Sel.Name
			}
		}
		// Fallback for complex selectors, though less common for types.
		return s.buildKey(n.X, pkg, importLookup, currentTypeParams) + "." + n.Sel.Name
	case *ast.IndexExpr: // G[T]
		base := s.buildKey(n.X, pkg, importLookup, currentTypeParams)
		arg := s.buildKey(n.Index, pkg, importLookup, currentTypeParams)
		return fmt.Sprintf("%s[%s]", base, arg)
	case *ast.IndexListExpr: // G[T, U]
		base := s.buildKey(n.X, pkg, importLookup, currentTypeParams)
		args := make([]string, len(n.Indices))
		for i, idx := range n.Indices {
			args[i] = s.buildKey(idx, pkg, importLookup, currentTypeParams)
		}
		return fmt.Sprintf("%s[%s]", base, strings.Join(args, ","))
	case *ast.StarExpr:
		return "*" + s.buildKey(n.X, pkg, importLookup, currentTypeParams)
	case *ast.ArrayType:
		return "[]" + s.buildKey(n.Elt, pkg, importLookup, currentTypeParams)
	case *ast.MapType:
		k := s.buildKey(n.Key, pkg, importLookup, currentTypeParams)
		v := s.buildKey(n.Value, pkg, importLookup, currentTypeParams)
		return fmt.Sprintf("map[%s]%s", k, v)
	case *ast.ChanType:
		v := s.buildKey(n.Value, pkg, importLookup, currentTypeParams)
		return "chan " + v
	case *ast.FuncType:
		var params []string
		if n.Params != nil {
			params = make([]string, len(n.Params.List))
			for i, p := range n.Params.List {
				params[i] = s.buildKey(p.Type, pkg, importLookup, currentTypeParams)
			}
		}
		var results []string
		if n.Results != nil {
			results = make([]string, len(n.Results.List))
			for i, r := range n.Results.List {
				results[i] = s.buildKey(r.Type, pkg, importLookup, currentTypeParams)
			}
		}
		return fmt.Sprintf("func(%s)(%s)", strings.Join(params, ","), strings.Join(results, ","))
	case *ast.InterfaceType:
		parts := make([]string, len(n.Methods.List))
		for i, method := range n.Methods.List {
			methodName := ""
			if len(method.Names) > 0 {
				methodName = method.Names[0].Name
			}
			parts[i] = methodName + s.buildKey(method.Type, pkg, importLookup, currentTypeParams)
		}
		return "interface{" + strings.Join(parts, ";") + "}"
	case *ast.StructType:
		parts := make([]string, len(n.Fields.List))
		for i, field := range n.Fields.List {
			var names []string
			for _, name := range field.Names {
				names = append(names, name.Name)
			}
			parts[i] = strings.Join(names, ",") + ":" + s.buildKey(field.Type, pkg, importLookup, currentTypeParams)
		}
		return "struct{" + strings.Join(parts, ";") + "}"
	default:
		// Fallback to position for any other unhandled types.
		return fmt.Sprintf("pos:%d", expr.Pos())
	}
}

// TypeInfoFromExpr resolves an AST expression that represents a type into a FieldType.
// This is the core type-parsing logic, exposed for tools that need to resolve
// type information dynamically.
func (s *Scanner) TypeInfoFromExpr(ctx context.Context, expr ast.Expr, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) *FieldType {
	if expr == nil {
		return &FieldType{Name: "untyped_nil_expr"}
	}

	// Get or create the resolution cache from the context.
	v := ctx.Value(resolutionCacheKey{})
	var cache map[string]*FieldType
	if v == nil {
		cache = make(map[string]*FieldType)
		ctx = context.WithValue(ctx, resolutionCacheKey{}, cache)
	} else {
		cache = v.(map[string]*FieldType)
	}

	// Generate a canonical key for the expression to robustly detect recursion.
	key := s.buildKey(expr, info, importLookup, currentTypeParams)

	// Check for recursion.
	if placeholder, ok := cache[key]; ok {
		return placeholder
	}

	// No recursion detected yet. Create a new placeholder for the result and cache it.
	placeholder := &FieldType{Resolver: s.resolver, CurrentPkg: info}
	cache[key] = placeholder

	// Resolve the actual type.
	realType := s.resolveFieldType(ctx, expr, currentTypeParams, info, importLookup)

	// Now that we have the real type, populate the placeholder that we created earlier.
	*placeholder = *realType

	// Return the original placeholder pointer.
	return placeholder
}

// resolveFieldType is the inner logic of TypeInfoFromExpr, without the recursion guard.
func (s *Scanner) resolveFieldType(ctx context.Context, expr ast.Expr, currentTypeParams []*TypeParamInfo, info *PackageInfo, importLookup map[string]string) *FieldType {
	ft := &FieldType{Resolver: s.resolver, CurrentPkg: info}
	switch t := expr.(type) {
	case *ast.Ident:
		ft.Name = t.Name
		isTypeParam := false
		if currentTypeParams != nil {
			for _, tp := range currentTypeParams {
				if tp.Name == t.Name {
					isTypeParam = true
					break
				}
			}
		}
		if isTypeParam {
			ft.IsTypeParam = true
		} else {
			switch t.Name {
			case "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
				"int", "int8", "int16", "int32", "int64", "rune", "string",
				"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
				"any", "comparable":
				ft.IsBuiltin = true
				if t.Name == "any" || t.Name == "comparable" {
					ft.IsConstraint = true
				}
			default:
				if info != nil {
					ft.FullImportPath = info.ImportPath
					ft.TypeName = t.Name
				}
			}
		}
	case *ast.StarExpr:
		elemType := s.TypeInfoFromExpr(ctx, t.X, currentTypeParams, info, importLookup)
		return &FieldType{
			Resolver:           s.resolver,
			Name:               elemType.Name,
			IsPointer:          true,
			Elem:               elemType,
			FullImportPath:     elemType.FullImportPath,
			TypeName:           elemType.TypeName,
			PkgName:            elemType.PkgName,
			TypeArgs:           elemType.TypeArgs,
			IsResolvedByConfig: elemType.IsResolvedByConfig, // Propagate from element
			CurrentPkg:         info,                        // Ensure current package context is passed
		}
	case *ast.SelectorExpr:
		pkgIdent, ok := t.X.(*ast.Ident)
		if !ok {
			return &FieldType{Name: fmt.Sprintf("unsupported_selector_expr.%s", t.Sel.Name)}
		}
		pkgImportPath, _ := importLookup[pkgIdent.Name]
		qualifiedName := fmt.Sprintf("%s.%s", pkgImportPath, t.Sel.Name)

		// Check for external type overrides first.
		if overrideInfo, ok := s.ExternalTypeOverrides[qualifiedName]; ok {
			// If an override is found, create a FieldType from the synthetic TypeInfo.
			return &FieldType{
				Name:               overrideInfo.Name,
				PkgName:            overrideInfo.PkgPath,
				FullImportPath:     overrideInfo.PkgPath,
				TypeName:           overrideInfo.Name,
				IsResolvedByConfig: true,
				Definition:         overrideInfo, // Link to the synthetic definition
				Resolver:           s.resolver,
			}
		}

		// If no override, proceed with normal parsing.
		ft.PkgName = pkgIdent.Name
		ft.TypeName = t.Sel.Name
		ft.FullImportPath = pkgImportPath
		ft.Name = t.Sel.Name
	case *ast.IndexExpr:
		genericType := s.TypeInfoFromExpr(ctx, t.X, currentTypeParams, info, importLookup)
		typeArg := s.TypeInfoFromExpr(ctx, t.Index, currentTypeParams, info, importLookup)
		genericType.TypeArgs = append(genericType.TypeArgs, typeArg)
		return genericType
	case *ast.IndexListExpr:
		genericType := s.TypeInfoFromExpr(ctx, t.X, currentTypeParams, info, importLookup)
		for _, indexExpr := range t.Indices {
			typeArg := s.TypeInfoFromExpr(ctx, indexExpr, currentTypeParams, info, importLookup)
			genericType.TypeArgs = append(genericType.TypeArgs, typeArg)
		}
		return genericType
	case *ast.ArrayType:
		ft.IsSlice = true
		ft.Name = "slice"
		ft.Elem = s.TypeInfoFromExpr(ctx, t.Elt, currentTypeParams, info, importLookup)
	case *ast.MapType:
		ft.IsMap = true
		ft.Name = "map"
		ft.MapKey = s.TypeInfoFromExpr(ctx, t.Key, currentTypeParams, info, importLookup)
		ft.Elem = s.TypeInfoFromExpr(ctx, t.Value, currentTypeParams, info, importLookup)
	case *ast.ChanType:
		ft.IsChan = true
		ft.Name = "chan"
		ft.Elem = s.TypeInfoFromExpr(ctx, t.Value, currentTypeParams, info, importLookup)
	case *ast.InterfaceType:
		// Parse the anonymous interface to get its structure.
		interfaceInfo := s.parseInterfaceType(ctx, t, currentTypeParams, info, importLookup)
		// Create a synthetic TypeInfo to hold this structure.
		// This TypeInfo is "anonymous" (has no name).
		anonymousTypeInfo := &TypeInfo{
			Name:      "", // Anonymous
			Kind:      InterfaceKind,
			Interface: interfaceInfo,
			PkgPath:   info.ImportPath, // Belongs to the package where it's defined.
		}
		// The FieldType's Definition points to our synthetic TypeInfo.
		ft.Definition = anonymousTypeInfo
		ft.Name = "interface{...}" // A more descriptive name for debugging.
		return ft
	case *ast.StructType:
		// This case was missing. Handle anonymous structs similarly.
		structInfo := s.parseStructType(ctx, t, currentTypeParams, info, importLookup)
		anonymousTypeInfo := &TypeInfo{
			Name:    "", // Anonymous
			Kind:    StructKind,
			Struct:  structInfo,
			PkgPath: info.ImportPath,
		}
		ft.Definition = anonymousTypeInfo
		ft.Name = "struct{...}"
		return ft
	case *ast.Ellipsis:
		ft.IsSlice = true
		ft.Name = "slice"
		ft.Elem = s.TypeInfoFromExpr(ctx, t.Elt, currentTypeParams, info, importLookup)
	default:
		ft.Name = fmt.Sprintf("unhandled_type_%T", t)
	}
	return ft
}

func commentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}
