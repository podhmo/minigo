package scanner

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"log/slog"
	"reflect"
	"strings"
	"sync"
)

// Context keys for passing information through the resolution process.
type (
	resolutionPathKey struct{}
	loggerKey         struct{}
	inspectKey        struct{}
)

// Public context keys to be used by packages like goscan.
var (
	ResolutionPathKey = resolutionPathKey{}
	LoggerKey         = loggerKey{}
	InspectKey        = inspectKey{}
)

// TypeParamInfo stores information about a single type parameter.
type TypeParamInfo struct {
	Name       string     `json:"name"`
	Constraint *FieldType `json:"constraint,omitempty"`
}

// Kind defines the category of a type definition.
type Kind int

const (
	StructKind Kind = iota
	AliasKind
	FuncKind
	InterfaceKind
	UnknownKind
)

// PackageResolver is an interface that can resolve an import path to a package definition.
// It is implemented by the top-level typescanner.Scanner to enable lazy, cached lookups.
type PackageResolver interface {
	ScanPackageFromImportPath(ctx context.Context, importPath string) (*PackageInfo, error)
}

// PackageInfo holds all the extracted information from a single package.
type PackageInfo struct {
	// ID is the unique identifier for the package. For most packages, it's the
	// same as the canonical ImportPath. For `main` packages, it includes a
	// ".main" suffix (e.g., "example.com/myapp/cmd/server.main") to
	// disambiguate between multiple main packages in a workspace.
	ID         string
	Name       string
	Path       string
	ImportPath string // Added: Canonical import path of the package
	ModulePath string // The go module path this package belongs to.
	ModuleDir  string // The absolute path to the module's root directory
	Files      []string
	Types      []*TypeInfo
	Constants  []*ConstantInfo
	Variables  []*VariableInfo
	Functions  []*FunctionInfo
	Fset       *token.FileSet       // Added: Fileset for position information
	AstFiles   map[string]*ast.File // Added: Parsed AST for each file

	lookupOnce sync.Once
	lookup     map[string]*TypeInfo
}

// Lookup finds a type by name in the package.
func (p *PackageInfo) Lookup(name string) *TypeInfo {
	p.lookupOnce.Do(func() {
		p.lookup = make(map[string]*TypeInfo, len(p.Types))
		for _, t := range p.Types {
			if t != nil {
				p.lookup[t.Name] = t
			}
		}
	})
	return p.lookup[name]
}

// ExternalTypeOverride defines a mapping from a fully qualified type name
// (e.g., "time.Time") to a pre-defined TypeInfo struct.
// This allows users to provide a "synthetic" type definition for certain types,
// bypassing the need for the scanner to parse them from source. This is particularly
// useful for standard library types that can cause issues when scanned from
// within a test binary, or for any type where manual definition is preferred.
// The key is the fully qualified type name (ImportPath + "." + TypeName).
// The value is a pointer to a scanner.TypeInfo struct that defines the type.
type ExternalTypeOverride map[string]*TypeInfo

// Overlay provides a way to replace the contents of a file with alternative content.
// The key is either a project-relative path (from the module root) or a
// Go package path concatenated with a file name.
type Overlay map[string][]byte

// TypeInfo represents a single type declaration (`type T ...`).
type TypeInfo struct {
	Name       string           `json:"name"`
	PkgPath    string           `json:"pkgPath"`
	FilePath   string           `json:"filePath"`
	Doc        string           `json:"doc,omitempty"`
	Kind       Kind             `json:"kind"`
	TypeParams []*TypeParamInfo `json:"typeParams,omitempty"` // For generic types
	Node       ast.Node         `json:"-"`                    // Avoid cyclic JSON with Node itself.
	Struct     *StructInfo      `json:"struct,omitempty"`
	Func       *FunctionInfo    `json:"func,omitempty"` // For type alias to func type
	Interface  *InterfaceInfo   `json:"interface,omitempty"`
	Underlying *FieldType       `json:"underlying,omitempty"` // For alias types

	// --- Fields for Enum-like patterns ---
	IsEnum      bool            `json:"isEnum,omitempty"`      // True if this type is identified as an enum
	EnumMembers []*ConstantInfo `json:"enumMembers,omitempty"` // List of constants belonging to this enum type

	// --- Fields for inspect mode ---
	Inspect           bool            `json:"-"`                    // Flag to enable inspection logging
	Logger            *slog.Logger    `json:"-"`                    // Logger for inspection
	Fset              *token.FileSet  `json:"-"`                    // Fileset for position information
	ResolutionContext context.Context `json:"-"`                    // Context for resolving nested types
	Unresolved        bool            `json:"unresolved,omitempty"` // True if the type is from a package that was not scanned.
}

// NewUnresolvedTypeInfo creates a new TypeInfo placeholder for a type that is
// intentionally not being scanned or resolved. This is used by higher-level
// tools like symgo to represent types from packages outside a defined scan policy.
func NewUnresolvedTypeInfo(pkgPath, name string) *TypeInfo {
	return &TypeInfo{
		PkgPath:    pkgPath,
		Name:       name,
		Unresolved: true,
		Kind:       UnknownKind,
	}
}

// Annotation extracts the value of a specific annotation from the TypeInfo's Doc string.
// Annotations are expected to be in the format "@<name>[:<value>]" or "@<name> <value>".
// If inspect mode is enabled, it logs the checking process.
func (ti *TypeInfo) Annotation(ctx context.Context, name string) (value string, ok bool) {
	// The core annotation searching logic.
	searchValue, found := ti.searchAnnotation(name)

	// If inspect mode is off, just return the result.
	if !ti.Inspect || ti.Logger == nil {
		return searchValue, found
	}

	// Prepare structured logging fields.
	logArgs := []any{
		slog.String("component", "go-scan"),
		slog.String("type_name", ti.Name),
		slog.String("type_pkg_path", ti.PkgPath),
		slog.String("annotation_name", "@"+name),
	}
	if ti.Node != nil && ti.Fset != nil {
		pos := ti.Fset.Position(ti.Node.Pos())
		logArgs = append(logArgs, slog.String("type_file_path", fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column)))
	}

	// Log the result of the check.
	if found {
		logArgs = append(logArgs, slog.String("annotation_value", searchValue))
		ti.Logger.InfoContext(ctx, "found annotation", logArgs...)
	} else {
		logArgs = append(logArgs, slog.String("result", "miss"))
		ti.Logger.DebugContext(ctx, "checking for annotation", logArgs...)
	}

	return searchValue, found
}

// searchAnnotation is the core logic for finding an annotation, separated to keep the main Annotation method clean.
func (ti *TypeInfo) searchAnnotation(name string) (value string, ok bool) {
	if ti.Doc == "" {
		return "", false
	}
	lines := strings.Split(ti.Doc, "\n")
	prefix := "@" + name
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		trimmedLine = strings.TrimPrefix(trimmedLine, "//")
		trimmedLine = strings.TrimPrefix(trimmedLine, "/*")
		trimmedLine = strings.TrimSuffix(trimmedLine, "*/")
		trimmedLine = strings.TrimSpace(trimmedLine)

		if !strings.HasPrefix(trimmedLine, prefix) {
			continue
		}

		rest := trimmedLine[len(prefix):]

		if rest != "" {
			firstChar := rest[0]
			if firstChar != ':' && firstChar != ' ' && firstChar != '\t' {
				continue
			}
		}

		ok = true
		value = strings.TrimSpace(rest)
		if len(value) > 0 && value[0] == ':' {
			value = strings.TrimSpace(value[1:])
		}
		return value, ok
	}
	return "", false
}

// InterfaceInfo represents an interface type.
type InterfaceInfo struct {
	Methods []*MethodInfo `json:"methods"`
	// Embedded stores the field types for embedded interfaces.
	// The symgo evaluator is responsible for resolving these and collecting
	// their method sets. Resolution is currently supported for interfaces
	// within the same package.
	Embedded []*FieldType `json:"embedded,omitempty"`
	Union    []*FieldType `json:"union,omitempty"` // For union-type interfaces
}

// MethodInfo represents a single method in an interface.
type MethodInfo struct {
	Name       string
	Parameters []*FieldInfo
	Results    []*FieldInfo
	// We might need position or AST node info later if generating code that needs to refer back to the source.
}

// StructInfo represents a struct type.
type StructInfo struct {
	Fields []*FieldInfo
}

// FieldInfo represents a single field in a struct or a parameter/result in a function.
type FieldInfo struct {
	Name       string
	Doc        string
	Type       *FieldType
	Tag        string
	Embedded   bool
	IsExported bool // True if the field is exported (starts with an uppercase letter).
}

// TagValue extracts the value associated with the given tagName from the struct tag.
// If the tag value contains a comma (e.g., "name,omitempty"), only the part before the comma is returned.
// Returns an empty string if the tag is not present or malformed.
func (fi *FieldInfo) TagValue(tagName string) string {
	if fi.Tag == "" {
		return ""
	}
	tag := reflect.StructTag(fi.Tag)
	tagVal := tag.Get(tagName)
	if commaIdx := strings.Index(tagVal, ","); commaIdx != -1 {
		return tagVal[:commaIdx]
	}
	return tagVal
}

// FieldType represents the type of a field.
type FieldType struct {
	Name         string       `json:"name"`
	PkgName      string       `json:"pkgName,omitempty"` // e.g., "json", "models"
	MapKey       *FieldType   `json:"mapKey,omitempty"`  // For map types
	Elem         *FieldType   `json:"elem,omitempty"`    // For slice, map, pointer, array types
	IsPointer    bool         `json:"isPointer,omitempty"`
	IsSlice      bool         `json:"isSlice,omitempty"`
	IsMap        bool         `json:"isMap,omitempty"`
	IsChan       bool         `json:"isChan,omitempty"`
	IsTypeParam  bool         `json:"isTypeParam,omitempty"`  // True if this FieldType refers to a type parameter
	IsConstraint bool         `json:"isConstraint,omitempty"` // True if this FieldType represents a type constraint
	TypeArgs     []*FieldType `json:"typeArgs,omitempty"`     // For instantiated generic types, e.g., T in List[T]

	Definition         *TypeInfo `json:"-"` // Caches the resolved type definition. Avoid cyclic JSON.
	IsResolvedByConfig bool      `json:"isResolvedByConfig,omitempty"`
	IsBuiltin          bool      `json:"isBuiltin,omitempty"`

	// Resolver, FullImportPath, and TypeName are used for on-demand package scanning.
	// They are exported to allow consumers of the library to construct a resolvable
	// FieldType manually, for instance when parsing type information from an
	// annotation rather than from a Go AST node.
	Resolver       PackageResolver `json:"-"` // For lazy-loading the type definition.
	FullImportPath string          `json:"-"` // Full import path of the type, e.g., "example.com/project/models".
	TypeName       string          `json:"-"` // The name of the type within its package, e.g., "User".
	CurrentPkg     *PackageInfo    `json:"-"` // Reference to the package where this type is used.
}

// Resolve finds and returns the full definition of the type.
// It uses the PackageResolver to parse other packages on-demand.
// The result is cached for subsequent calls.

// String returns the Go string representation of the field type.
// e.g., "*pkgname.MyType", "[]string", "map[string]int", "MyType[string]"
func (ft *FieldType) String() string {
	if ft == nil {
		return "<nil_FieldType>"
	}
	var sb strings.Builder

	if ft.IsPointer {
		sb.WriteString("*")
		// For pointers, the current parsing for StarExpr in parseTypeExpr does:
		//   underlyingType := s.parseTypeExpr(t.X, currentTypeParams)
		//   underlyingType.IsPointer = true
		//   return underlyingType
		// This means `underlyingType` (which is `ft` here) *is* the element type, but marked as a pointer.
		// So, we write "*" and then format `ft` as if it's not a pointer for the rest of its structure.
		// ft.Elem is primarily for slice/map element types.
	}

	if ft.IsSlice {
		sb.WriteString("[]")
		if ft.Elem != nil {
			sb.WriteString(ft.Elem.String()) // Recursive call for element type
		} else {
			// This case should ideally not happen for valid Go code.
			sb.WriteString("interface{}") // Fallback
		}
		return sb.String() // Slice representation is complete
	}

	if ft.IsMap {
		sb.WriteString("map[")
		if ft.MapKey != nil {
			sb.WriteString(ft.MapKey.String())
		} else {
			sb.WriteString("interface{}") // Fallback
		}
		sb.WriteString("]")
		if ft.Elem != nil {
			sb.WriteString(ft.Elem.String())
		} else {
			sb.WriteString("interface{}") // Fallback
		}
		return sb.String() // Map representation is complete
	}

	// Named types, primitives, or type parameters
	name := ft.Name
	if ft.PkgName != "" && !ft.IsTypeParam { // Type parameters don't have package names like "pkg.T"
		// For qualified types like "pkg.MyType"
		// ft.Name might already be "pkg.MyType" if parsed from SelectorExpr.
		// Or ft.Name is "MyType" and ft.PkgName is "pkg".
		// Prefer ft.TypeName if available (set by SelectorExpr parsing for the base name).
		actualName := ft.Name
		if ft.TypeName != "" {
			actualName = ft.TypeName
		}
		name = fmt.Sprintf("%s.%s", ft.PkgName, actualName)
	}
	sb.WriteString(name)

	// Append type arguments if any, e.g., MyType[T, U]
	if len(ft.TypeArgs) > 0 {
		sb.WriteString("[")
		for i, typeArg := range ft.TypeArgs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(typeArg.String())
		}
		sb.WriteString("]")
	}

	return sb.String()
}

func (ft *FieldType) Resolve(ctx context.Context) (*TypeInfo, error) {
	// If the definition is already cached (e.g. from an override), return it.
	if ft.Definition != nil {
		return ft.Definition, nil
	}

	// For pointer types, try to resolve the element first. If the element is
	// resolved (e.g., by an override), the pointer itself is considered resolved.
	// This prevents unnecessary package scanning for pointers to overridden types.
	if ft.IsPointer && ft.Elem != nil {
		elemDef, err := ft.Elem.Resolve(ctx)
		if err != nil {
			// Return the error, but wrap it to provide context.
			return nil, fmt.Errorf("could not resolve pointer element for %s: %w", ft.String(), err)
		}
		// If the element's resolution returned a definition, the pointer is resolved.
		// A pointer's definition is its element's definition.
		if elemDef != nil {
			ft.Definition = elemDef // Cache the result
			return elemDef, nil
		}
		// If the element is a built-in type (like *string), it resolves to a nil TypeInfo.
		// In this case, the pointer is also considered resolved.
		if ft.Elem.IsBuiltin {
			return nil, nil
		}
	}

	if ft.IsBuiltin {
		// Built-in types like 'string' do not have a full TypeInfo definition, so we return nil.
		// The caller can inspect ft.IsBuiltin if it needs to differentiate.
		return nil, nil
	}
	if ft.Resolver == nil {
		return nil, fmt.Errorf("type %q cannot be resolved: no resolver available", ft.Name)
	}
	// Check for local types (they have no PkgName) before attempting cross-package resolution.
	if ft.PkgName == "" {
		// This is a type from the same package.
		if ft.CurrentPkg == nil {
			// This can happen if a FieldType is constructed manually without setting the package context.
			return nil, fmt.Errorf("cannot resolve local type %q: current package context is missing", ft.TypeName)
		}
		// Look up the type in the current package's type map.
		typeInfo := ft.CurrentPkg.Lookup(ft.TypeName)
		if typeInfo == nil {
			// Built-in types (like 'string') and type parameters (like 'T' in generics)
			// are parsed as local types with no PkgName, but they don't have a TypeInfo definition.
			// They are not an error.
			if ft.IsBuiltin || ft.IsTypeParam {
				return nil, nil
			}
			// The type was not found in the current package. This is the error we want.
			return nil, fmt.Errorf("could not resolve type %q in package %q", ft.TypeName, ft.CurrentPkg.ImportPath)
		}
		// Type was found locally.
		ft.Definition = typeInfo
		return typeInfo, nil
	}

	// Extract logger, inspect flag, and current resolution path from context.
	logger, _ := ctx.Value(LoggerKey).(*slog.Logger)
	inspect, _ := ctx.Value(InspectKey).(bool)
	path, _ := ctx.Value(ResolutionPathKey).([]string)
	if path == nil {
		path = []string{} // Should not happen if called via designated entry points.
	}

	typeIdentifier := ft.FullImportPath + "." + ft.TypeName

	// --- Cycle Detection ---
	for _, p := range path {
		if p == typeIdentifier {
			return nil, nil // Cycle detected.
		}
	}

	// --- Logging (if inspect mode is on) ---
	if inspect && logger != nil {
		logger.DebugContext(ctx, "resolving type",
			"type", typeIdentifier,
			"resolution_path", path,
		)
	}

	// --- Resolve the package ---
	pkgInfo, err := ft.Resolver.ScanPackageFromImportPath(ctx, ft.FullImportPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan package %q for type %q: %w", ft.FullImportPath, ft.TypeName, err)
	}

	typeInfo := pkgInfo.Lookup(ft.TypeName)
	if typeInfo == nil {
		return nil, fmt.Errorf("type %q not found in package %q", ft.TypeName, ft.FullImportPath)
	}

	// --- Success ---
	if inspect && logger != nil {
		logger.InfoContext(ctx, "resolved type",
			"type", typeIdentifier,
			"resolution_path", path,
		)
	}

	// --- Prepare context for child resolutions ---
	newPath := append(path, typeIdentifier)
	childCtx := context.WithValue(ctx, ResolutionPathKey, newPath)
	// Propagate other necessary values.
	if logger != nil {
		childCtx = context.WithValue(childCtx, LoggerKey, logger)
	}
	childCtx = context.WithValue(childCtx, InspectKey, inspect)

	typeInfo.ResolutionContext = childCtx
	typeInfo.Logger = logger
	typeInfo.Inspect = inspect

	ft.Definition = typeInfo // Cache the result.
	return typeInfo, nil
}

// ConstantInfo represents a single top-level constant declaration.
type ConstantInfo struct {
	Name       string
	FilePath   string // Added: Absolute path to the file where this const is defined
	Doc        string
	Type       *FieldType // Changed from string to *FieldType
	Value      string
	RawValue   string   // The raw, unquoted string value, if the constant is a string.
	IsExported bool     // Added to indicate if the constant is exported
	Node       ast.Node // Added: AST node for position, if needed, though FilePath is primary
	ConstVal   constant.Value
	IotaValue  int // The value of iota for the spec this constant was in.
	ValExpr    ast.Expr
}

// VariableInfo represents a single top-level variable declaration.
type VariableInfo struct {
	Name       string
	FilePath   string
	Doc        string
	Type       *FieldType
	IsExported bool
	Node       ast.Node
	GenDecl    *ast.GenDecl // The *ast.GenDecl node for the var declaration
}

// FunctionInfo represents a single top-level function or method declaration.
type FunctionInfo struct {
	Name       string           `json:"name"`
	PkgPath    string           `json:"pkgPath"`
	FilePath   string           `json:"filePath"`
	Doc        string           `json:"doc,omitempty"`
	Receiver   *FieldInfo       `json:"receiver,omitempty"`
	TypeParams []*TypeParamInfo `json:"typeParams,omitempty"` // For generic functions
	Parameters []*FieldInfo     `json:"parameters,omitempty"`
	Results    []*FieldInfo     `json:"results,omitempty"`
	IsVariadic bool             `json:"isVariadic,omitempty"`
	AstDecl    *ast.FuncDecl    `json:"-"` // Avoid cyclic JSON.
	Pkg        *PackageInfo     `json:"-"` // Back-reference to the containing package.
}

// SetResolver is a test helper to overwrite the internal resolver.
func (ft *FieldType) SetResolver(r PackageResolver) {
	ft.Resolver = r
}

// var _ = strings.Builder{} // This helper is no longer needed as "strings" is directly imported.

// PackageImports holds the minimal information about a package's direct imports.
type PackageImports struct {
	Name        string
	ImportPath  string
	Imports     []string
	FileImports map[string][]string // file path -> import paths
}

// Visitor defines the interface for operations to be performed at each node
// during a dependency graph walk.
type Visitor interface {
	// Visit is called for each package discovered during the walk.
	// It can inspect the package's imports and return the list of
	// imports that the walker should follow next. Returning an empty
	// slice stops the traversal from that node.
	Visit(pkg *PackageImports) (importsToFollow []string, err error)
}

// ModuleInfo holds information about a single Go module in a workspace.
type ModuleInfo struct {
	Path string // The module path (e.g., "github.com/podhmo/minigo/examples/convert-define").
	Dir  string // The absolute path to the module's root directory.
}
