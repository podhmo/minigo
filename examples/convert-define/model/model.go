package model

import (
	"fmt"
	"strings"

	xinspect "github.com/podhmo/minigo/inspect"
)

// ParsedInfo holds all parsed conversion rules and type information.
type ParsedInfo struct {
	PackageName     string
	PackagePath     string // Import path of the package being parsed
	ConversionPairs []ConversionPair
	GlobalRules     []TypeRule
	Imports         map[string]string // alias -> import path
	Structs         map[string]*StructInfo
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
	// Prelude holds statements to emit before the assignment — the
	// nil-pointer initialisation needed when DstName is a nested path
	// ("Inner.X") whose intermediate fields are pointers. Filled by the
	// generator's emit pass; "" for a top-level field.
	Prelude string
}

// FieldMap defines a mapping between a source and destination field,
// optionally with a custom conversion function. SrcName/DstName are
// field paths relative to the src/dst variables — a single field name
// ("ID") or a dotted path through nested structs ("Inner.ID").
type FieldMap struct {
	DstName   string
	SrcName   string
	Converter string // e.g. "funcs.UserIDToString"
}

// MappingInfo holds the set of explicit mapping rules defined by the
// c.Map/c.Convert calls in a define.Convert mapping function.
type MappingInfo struct {
	Maps     []FieldMap
	Computes []ComputedField
}

// ConversionPair defines a top-level conversion between two types.
type ConversionPair struct {
	SrcTypeName string
	DstTypeName string
	SrcTypeInfo *xinspect.Decl
	DstTypeInfo *xinspect.Decl
	Mapping     *MappingInfo // Explicit mapping rules from the mapping function body
	MaxErrors   int
	Variables   []Variable
	Computed    []ComputedField // TODO: This might be deprecated in favor of Mapping.Computes
}

// TypeRule defines a global rule for converting between types.
type TypeRule struct {
	SrcTypeName string
	DstTypeName string
	SrcTypeInfo *xinspect.Decl
	DstTypeInfo *xinspect.Decl
	UsingFunc   string
}

// StructInfo holds information about a parsed struct.
type StructInfo struct {
	Name   string
	Fields []FieldInfo
	Type   *xinspect.Decl
}

// FieldInfo holds information about a field within a struct.
type FieldInfo struct {
	Name         string
	OriginalName string
	JSONTag      string
	FieldType    *xinspect.TypeExpr // The declared field type, as an inspect view
	ParentStruct *StructInfo
}

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
