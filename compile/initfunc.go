package compile

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/spike"
	"github.com/podhmo/minigo/runtime"
)

// InitFunc compiles the package initializer: const and var declarations in
// dependency order, as Go initializes them, then the init functions. Use
// [ConstInitFunc] to bind only the constants, without running var
// initializers or init functions.
func InitFunc(pkg *runtime.Package) (*bytecode.Chunk, error) {
	return InitFuncStrict(pkg, false)
}

// InitFuncStrict is [InitFunc] under strict lazy compilation.
func InitFuncStrict(pkg *runtime.Package, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	c := &compiler{pkg: pkg, fs: newFScope(nil), ch: &bytecode.Chunk{Name: pkg.Name + ".__init__"}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()

	// iota is a real identifier in const specs; bind it as a hidden local
	// (declared last wins — it shadows nothing here).
	iotaSlot := c.fs.declare("iota", token.NoPos)
	c.fs.iota = iotaSlot
	c.emit(bytecode.OpConst, c.constIdx(int64(0)), 0, 0)
	c.emit(bytecode.OpNewLocal, iotaSlot, 0, 0)

	// Go initializes vars/consts in dependency order, not textual order.
	for _, d := range orderSpecs(pkg.Index, valueSpecReps(pkg.Index.Decls, false)) {
		c.file = d.File // per-file: import aliases and scope lookups
		if d.Kind == index.ConstDecl {
			c.emit(bytecode.OpConst, c.constIdx(int64(d.Idx)), 0, d.Pos)
			c.emit(bytecode.OpSetLocal, iotaSlot, 0, d.Pos)
			c.iotaVal = d.Idx
		}
		c.valueSpec(d.Spec.(*ast.ValueSpec), d)
		c.iotaVal = -1
	}
	for _, d := range pkg.Index.Inits {
		c.file = d.File
		fv := &runtime.Function{Pkg: pkg, File: d.File, Decl: d.Func, Name: "init"}
		c.emit(bytecode.OpConst, c.constIdx(fv), 0, d.Pos)
		c.emit3(bytecode.OpCall, 0, 0, c.callSiteArgs(nil, d.Pos), d.Pos)
		c.emit(bytecode.OpPop, 0, 0, d.Pos)
	}
	c.emit(bytecode.OpReturn, 0, 0, 0)
	return c.finishChunk(), nil
}

// valueSpecReps returns one representative declaration per value spec, in
// source order. constOnly narrows the representatives to const declarations.
func valueSpecReps(decls []*index.Decl, constOnly bool) []*index.Decl {
	var out []*index.Decl
	seen := map[*ast.ValueSpec]bool{}
	for _, d := range decls {
		if d.Kind != index.ConstDecl && (constOnly || d.Kind != index.VarDecl) {
			continue
		}
		vs := d.Spec.(*ast.ValueSpec)
		if seen[vs] {
			continue
		}
		seen[vs] = true
		out = append(out, d)
	}
	return out
}

// ConstInitFunc compiles an initializer for the package's constants only,
// in dependency order, without var declarations or init functions, so
// constants bind without the package's initialization side effects. It
// returns a nil chunk and nil error when a const initializer references a
// package-level var or function; use [InitFunc] then.
func ConstInitFunc(pkg *runtime.Package) (*bytecode.Chunk, error) {
	return ConstInitFuncStrict(pkg, false)
}

// ConstInitFuncStrict is [ConstInitFunc] under strict lazy compilation.
func ConstInitFuncStrict(pkg *runtime.Package, strict bool) (*bytecode.Chunk, error) {
	spike.EnterCompile()
	defer spike.LeaveCompile()
	specReps := valueSpecReps(pkg.Index.Decls, true)
	if constRefsVarOrFunc(pkg.Index, specReps) {
		// such a program needs the full initializer: either the code is
		// not really constant, or a builtin operand like len/Sizeof still
		// evaluates the var.
		return nil, nil
	}

	c := &compiler{pkg: pkg, fs: newFScope(nil), ch: &bytecode.Chunk{Name: pkg.Name + ".__consts__"}, iotaVal: -1, strictLazy: strict}
	c.fs.pushBlock()

	// same iota plumbing as InitFunc: a hidden local backs the `iota`
	// builtin inside const specs.
	iotaSlot := c.fs.declare("iota", token.NoPos)
	c.fs.iota = iotaSlot
	c.emit(bytecode.OpConst, c.constIdx(int64(0)), 0, 0)
	c.emit(bytecode.OpNewLocal, iotaSlot, 0, 0)

	for _, d := range orderSpecs(pkg.Index, specReps) {
		c.file = d.File
		c.emit(bytecode.OpConst, c.constIdx(int64(d.Idx)), 0, d.Pos)
		c.emit(bytecode.OpSetLocal, iotaSlot, 0, d.Pos)
		c.iotaVal = d.Idx
		c.valueSpec(d.Spec.(*ast.ValueSpec), d)
		c.iotaVal = -1
	}
	c.emit(bytecode.OpReturn, 0, 0, 0)
	return c.finishChunk(), nil
}
