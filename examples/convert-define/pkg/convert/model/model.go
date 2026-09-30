package model

import (
	"fmt"
	"strings"

	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

// ParsedInfo holds all parsed conversion rules and type information.
type ParsedInfo struct {
	PackageName       string
	PackagePath       string // Import path of the package being parsed
	ConversionPairs   []ConversionPair
	GlobalRules       []TypeRule
	Imports           map[string]string // alias -> import path
	Structs           map[string]*StructInfo
	NamedTypes        map[string]*scanner.TypeInfo
	ProcessedPackages map[string]bool // Tracks import paths that have been parsed
}

// Variable defines a variable to be declared in the converter function.
type Variable struct {
	Name string
	Type string
}

// ComputedField defines a field that is computed from an expression.
type ComputedField struct {
	DstName string
	Expr    string
}

// FieldMap defines a mapping between a source and destination field,
// optionally with a custom conversion function.
type FieldMap struct {
	DstName   string
	SrcName   string
	Converter string // e.g. "funcs.UserIDToString"
}

// MappingInfo holds the set of explicit mapping rules defined within a define.Mapping call.
type MappingInfo struct {
	Maps     []FieldMap
	Computes []ComputedField
}

// ConversionPair defines a top-level conversion between two types.
type ConversionPair struct {
	SrcTypeName string
	DstTypeName string
	SrcTypeInfo *scanner.TypeInfo
	DstTypeInfo *scanner.TypeInfo
	Mapping     *MappingInfo // Explicit mapping rules from define.Mapping
	MaxErrors   int
	Variables   []Variable
	Computed    []ComputedField // TODO: This might be deprecated in favor of Mapping.Computes
}

// TypeRule defines a global rule for converting between types or validating a type.
type TypeRule struct {
	SrcTypeName   string
	DstTypeName   string
	SrcTypeInfo   *scanner.TypeInfo
	DstTypeInfo   *scanner.TypeInfo
	UsingFunc     string
	ValidatorFunc string
}

// StructInfo holds information about a parsed struct.
type StructInfo struct {
	Name            string
	Fields          []FieldInfo
	Type            *scanner.TypeInfo
	IsAlias         bool
	UnderlyingAlias *scanner.TypeInfo
}

// FieldInfo holds information about a field within a struct.
type FieldInfo struct {
	Name         string
	OriginalName string
	JSONTag      string
	TypeInfo     *scanner.TypeInfo  // The resolved TypeInfo for the field's type
	FieldType    *scanner.FieldType // The detailed FieldType
	Tag          ConvertTag
	ParentStruct *StructInfo
}

// ConvertTag holds parsed values from a `convert` struct tag.
type ConvertTag struct {
	DstFieldName string
	UsingFunc    string
	Required     bool
	RawValue     string
}

type TypeKind int

const (
	KindUnknown TypeKind = iota
	KindBasic
	KindIdent // Identifier, could be a struct, named type, etc.
	KindPointer
	KindSlice
	KindArray
	KindMap
	KindInterface
	KindStruct // Specifically a struct type definition
	KindNamed  // A named type (type MyInt int)
	KindFunc
)

// ErrorCollector accumulates errors during a conversion process.
type ErrorCollector struct {
	errors    []error
	MaxErrors int
	path      []string
}

// NewErrorCollector creates a new ErrorCollector.
func NewErrorCollector(maxErrors int) *ErrorCollector {
	return &ErrorCollector{
		MaxErrors: maxErrors,
	}
}

// Add records a new error with the current field path.
func (ec *ErrorCollector) Add(err error) {
	if ec.MaxErrorsReached() {
		return
	}
	path := strings.Join(ec.path, ".")
	ec.errors = append(ec.errors, fmt.Errorf("%s: %w", path, err))
}

// Enter steps into a nested field.
func (ec *ErrorCollector) Enter(field string) {
	ec.path = append(ec.path, field)
}

// Leave steps out of a nested field.
func (ec *ErrorCollector) Leave() {
	if len(ec.path) > 0 {
		ec.path = ec.path[:len(ec.path)-1]
	}
}

// MaxErrorsReached returns true if the number of collected errors has reached the maximum limit.
func (ec *ErrorCollector) MaxErrorsReached() bool {
	return ec.MaxErrors > 0 && len(ec.errors) >= ec.MaxErrors
}

// HasErrors returns true if any errors have been collected.
func (ec *ErrorCollector) HasErrors() bool {
	return len(ec.errors) > 0
}

// Errors returns the collected errors.
func (ec *ErrorCollector) Errors() []error {
	return ec.errors
}
