// Package scanner holds the data model produced by scanning Go packages.
// This is a slimmed-down vendored copy of go-scan's models, limited to the
// declarations convert-define consumes (types, structs, functions).
package scanner

import (
	"context"
	"fmt"
	"go/ast"
	"strings"
	"sync"
)

// Context keys for passing information through the resolution process.
type resolutionPathKey struct{}

// ResolutionPathKey carries the in-progress type resolution chain, used for
// cross-package cycle detection during lazy type resolution.
var ResolutionPathKey = resolutionPathKey{}

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
// It is implemented by the top-level goscan.Scanner to enable lazy, cached lookups.
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
	ImportPath string // Canonical import path of the package
	ModulePath string // The go module path this package belongs to.
	ModuleDir  string // The absolute path to the module's root directory
	Files      []string
	Types      []*TypeInfo
	Functions  []*FunctionInfo

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
// bypassing the need for the scanner to parse them from source.
// The key is the fully qualified type name (ImportPath + "." + TypeName).
type ExternalTypeOverride map[string]*TypeInfo

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
}

// InterfaceInfo represents an interface type.
type InterfaceInfo struct {
	Methods []*MethodInfo `json:"methods"`
	// Embedded stores the field types for embedded interfaces.
	Embedded []*FieldType `json:"embedded,omitempty"`
	Union    []*FieldType `json:"union,omitempty"` // For union-type interfaces
}

// MethodInfo represents a single method in an interface.
type MethodInfo struct {
	Name       string
	Parameters []*FieldInfo
	Results    []*FieldInfo
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

// String returns the Go string representation of the field type.
// e.g., "*pkgname.MyType", "[]string", "map[string]int", "MyType[string]"
func (ft *FieldType) String() string {
	if ft == nil {
		return "<nil_FieldType>"
	}
	var sb strings.Builder

	if ft.IsPointer {
		// The FieldType itself is the element type marked as a pointer;
		// render the "*" prefix and continue formatting the element.
		sb.WriteString("*")
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

// Resolve finds and returns the full definition of the type.
// It uses the PackageResolver to parse other packages on-demand.
// The result is cached for subsequent calls.
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

	// --- Resolve the package ---
	pkgInfo, err := ft.Resolver.ScanPackageFromImportPath(ctx, ft.FullImportPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan package %q for type %q: %w", ft.FullImportPath, ft.TypeName, err)
	}

	typeInfo := pkgInfo.Lookup(ft.TypeName)
	if typeInfo == nil {
		return nil, fmt.Errorf("type %q not found in package %q", ft.TypeName, ft.FullImportPath)
	}

	ft.Definition = typeInfo // Cache the result.
	return typeInfo, nil
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
}
