// Package semantics answers questions about declared types from
// indexes alone — the "what does this declaration spell" half of type
// reasoning, split out per the rearchitecture plan's M2 milestone.
//
// It never imports runtime, vm or compile (archcheck enforces this),
// never runs script code, and never sees a runtime.Value: answers are
// the AST a declaration spells, not a materialized type.
package semantics

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// Policy is how far a query may go to get its answer.
type Policy int

const (
	// IndexedOnly answers from indexes that already exist. A package
	// that is not indexed yet yields Deferred — never a load.
	IndexedOnly Policy = iota
	// AllowIndex may locate, parse and index a package. Initialization
	// is still never triggered.
	AllowIndex
)

// ResultKind classifies how a query resolved.
type ResultKind int

const (
	// Found means the query answered successfully; Val is set.
	Found ResultKind = iota
	// Missing means the package was consulted and the name is not
	// declared there.
	Missing
	// Deferred means there is no answer yet — the package is not
	// indexed and the policy did not allow indexing. It is not an
	// error.
	Deferred
	// Failed means the lookup itself failed; Err carries the cause.
	Failed
)

func (k ResultKind) String() string {
	switch k {
	case Found:
		return "Found"
	case Missing:
		return "Missing"
	case Deferred:
		return "Deferred"
	case Failed:
		return "Failed"
	}
	return fmt.Sprintf("ResultKind(%d)", int(k))
}

// Result is the outcome of one query. Val is meaningful only when
// Kind is Found; Err only when Kind is Failed.
type Result[T any] struct {
	Kind ResultKind
	Val  T
	Err  error
}

// DeclContext is the context a name is resolved in: the declaring
// package, the file the expression sits in, and the type-argument
// bindings in force there.
type DeclContext struct {
	// Pkg is the import path of the package the expression belongs to.
	Pkg string
	// File is the file holding Expr — the import table a selector or
	// qualified identifier resolves against.
	File *syntax.File
	// Binds binds type parameter names to the type expressions they
	// were instantiated with inside a generic declaration.
	Binds map[string]ast.Expr
}

// TypeRef is a type expression in its declaration context — the shape
// every semantics query takes as input and (where the answer is a
// type) returns.
type TypeRef struct {
	Expr ast.Expr
	Ctx  DeclContext
}

// Source is how semantics sees packages: index lookups only, with an
// indexed-state load behind the AllowIndex gate. M2-2's internal/source
// adapter implements it over the runtime loader; unit tests use a fake.
// It deliberately offers no way to reach package initialization.
type Source interface {
	// Indexed returns the package's index when it is already built,
	// nil otherwise. It never triggers loading.
	Indexed(path string) *index.Index
	// Load indexes the package (locate/parse/index, no init). A nil
	// result with nil error means the loader declined; callers treat
	// it as Deferred.
	Load(path string) (*index.Index, error)
}

// Service answers declaration questions over a Source.
type Service struct {
	src Source
}

// New builds a Service reading packages through src.
func New(src Source) *Service {
	return &Service{src: src}
}

// TypeDeclOf answers the first semantics question — "what type
// expression does the name declare?" — for the name in ctx.Pkg.
// Found carries the declared TypeSpec's type expression in the same
// context; Missing means the name declares no type; Deferred means the
// package was not indexed and the policy forbade indexing; Failed
// means the load errored or the decl is malformed.
func (s *Service) TypeDeclOf(ctx DeclContext, name string, p Policy) Result[TypeRef] {
	ix := s.src.Indexed(ctx.Pkg)
	if ix == nil {
		if p == IndexedOnly {
			return Result[TypeRef]{Kind: Deferred}
		}
		var err error
		ix, err = s.src.Load(ctx.Pkg)
		if err != nil {
			return Result[TypeRef]{Kind: Failed, Err: err}
		}
		if ix == nil {
			return Result[TypeRef]{Kind: Deferred}
		}
	}
	ti, ok := ix.Types[name]
	if !ok || ti.Decl == nil {
		return Result[TypeRef]{Kind: Missing}
	}
	ts, ok := ti.Decl.Spec.(*ast.TypeSpec)
	if !ok {
		return Result[TypeRef]{Kind: Failed, Err: fmt.Errorf("%s: %q declares %T, not *ast.TypeSpec", ctx.Pkg, name, ti.Decl.Spec)}
	}
	// The answer's context is the declaring file's, not the caller's:
	// qualified names inside the type expr resolve against the import
	// table of the file that declares them.
	return Result[TypeRef]{Kind: Found, Val: TypeRef{Expr: ts.Type, Ctx: DeclContext{Pkg: ctx.Pkg, File: ti.Decl.File, Binds: ctx.Binds}}}
}
