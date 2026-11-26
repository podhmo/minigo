package compile

import (
	"go/ast"
	"strconv"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// funcLit compiles a function literal into a separate chunk and emits a
// closure creation.
func (c *compiler) funcLit(x *ast.FuncLit) {
	// a synthetic Decl carries the signature so inference can unify a
	// funclit argument against `func(E) R`-shaped parameters.
	c.litCount++
	litName := c.symName + ".func" + strconv.Itoa(c.litCount)
	if c.symName == "" {
		litName = "<funclit>"
	}
	inner := &runtime.Function{Pkg: c.pkg, File: c.file, Name: litName, Decl: &ast.FuncDecl{Type: x.Type, Body: x.Body}}
	ic := &compiler{pkg: c.pkg, file: c.file, fs: newFScope(c.fs), ch: &bytecode.Chunk{Name: litName}, labels: map[string]*labelInfo{}, binds: c.binds, symName: litName, iotaVal: -1}
	ic.fs.pushBlock()
	nparams := 0
	var coerces []paramCoerce
	if x.Type.Params != nil {
		for _, field := range x.Type.Params.List {
			names := field.Names
			if len(names) == 0 {
				// minted on the funclit's own compiler so it stays
				// unique against $arg hoists inside the body.
				names = []*ast.Ident{{Name: ic.fresh("$arg")}}
			}
			for _, n := range names {
				slot := ic.fs.declare(n.Name, n.Pos())
				coerces = append(coerces, paramCoerce{slot: slot, typ: field.Type})
				ic.fs.noteDeclTyp(n.Name, ic.declTypCanon(field.Type), nil)
				nparams++
			}
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				ic.ch.IsVararg = true
			}
		}
	}
	ic.ch.NParams = nparams
	ic.emitParamCoerces(coerces)
	if x.Type.Results != nil {
		for _, field := range x.Type.Results.List {
			for _, n := range field.Names {
				slot := ic.fs.declare(n.Name, n.Pos())
				ic.fs.noteDeclTyp(n.Name, ic.declTypCanon(field.Type), nil)
				ic.ch.NamedSlots = append(ic.ch.NamedSlots, slot)
				ic.emit(bytecode.OpNil, 0, 0, n.Pos())
				ic.emit(bytecode.OpNewLocal, slot, 0, n.Pos())
				ic.emitSigTypeCoerce(slot, field.Type, n.Pos())
			}
		}
		ic.ch.NResults = runtime.FieldListLen(x.Type.Results)
	}
	ic.results = syntax.FieldTypes(x.Type.Results)
	if x.Body != nil {
		// same as Func: the body's outer scope is the signature's block.
		for _, s := range x.Body.List {
			ic.stmt(s)
		}
	}
	ic.resolveGotos()
	ic.emit(bytecode.OpReturn, ic.ch.NResults, 0, x.End())
	inner.Chunk = ic.finishChunk()

	c.emit(bytecode.OpMakeClosure, c.constIdx(inner), 0, x.Pos())
}
