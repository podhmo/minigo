// Package model holds convert-define rules and inspected type information.
//
// [ParsedInfo] groups [ConversionPair] and [TypeRule] values for generation.
// [StructInfoFromDecl] reads struct fields; [ResolveFieldPath] resolves dotted
// field mappings. Generated converters collect failures in [ErrorCollector].
package model
