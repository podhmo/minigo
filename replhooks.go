package minigo

import (
	"fmt"

	"github.com/podhmo/minigo/internal/replhooks"
	"github.com/podhmo/minigo/runtime"
)

// ReplHooks returns the session's internals in the shape the REPL
// front-end (github.com/podhmo/minigo/repl) drives. It exists for that
// package: the returned type is internal and unstable — call
// [Engine.NewSession] first when the hooks must not share this engine's
// package state.
func (e *Engine) ReplHooks() *replhooks.Hooks {
	return &replhooks.Hooks{
		Fset:     e.fset,
		Builtins: e.builtins,
		Specials: e.specials,
		Binds:    e.binds,
		Cfg:      e.cfg,
		Resolver: e.resolver,
		Cwd:      e.cwd,

		Fresh: func() *replhooks.Hooks { return e.NewSession().ReplHooks() },

		NewVM:     e.newVM,
		Call:      e.Call,
		EvalExpr:  e.EvalExpr,
		Bootstrap: e.bootstrap,

		LoadPath:        e.loadPath,
		LoadPathFrom:    e.loadPathFrom,
		LoadDir:         e.loadDir,
		PackageNameFrom: e.packageNameFrom,
		BoundPackages:   e.BoundPackages,
		LookupDeclIn:    e.lookupDeclIn,
		Materialize:     e.materialize,

		MethodFunc:     e.methodFunc,
		MethodFuncs:    e.methodFuncs,
		FieldTypes:     e.fieldTypes,
		ElemOf:         e.elemOf,
		PeelAliasTd:    e.peelAliasTd,
		ResolveTypeRef: e.resolveTypeRef,

		BindCompiles: bindCompiles,
		FormatValue: func(c runtime.VMCaller, x runtime.Value) fmt.Formatter {
			return &fmtValue{c: c, x: x, nilSyntax: true}
		},
	}
}
