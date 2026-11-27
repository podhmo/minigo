package minigo

import (
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// Bind registers a host package: scripts importing importPath get symbols
// as its members, without any source. [WithHostPolicy] can hide individual
// symbols. The package is named after the last import-path element unless
// [BindAs] gives its declared name.
//
// Symbols may be runtime values or plain Go values: a Go function is
// wrapped with [WrapFunc] under the name "<pkg>.<symbol>", a named host
// scalar such as time.Duration is kept as is, and any other Go value
// converts with [ValueOf]. A host package is therefore bound by
// listing its members directly:
//
//	e.Bind("github.com/yuin/goldmark-alert", map[string]runtime.Value{
//		"NewParser":       alert.NewParser,
//		"NewHTMLRenderer": alert.NewHTMLRenderer,
//	}, minigo.BindAs("alert"))
//
// Go cannot enumerate a package's members at run time, so the map lists
// what scripts use; `minigo gen-intrinsics` generates the table for every
// exported member of a package instead.
//
// For a type symbol, bind the typedef [HostTypeOf] returns when scripts
// must build values of it: a literal `pkg.T{Field: v}` then mints a real
// host *T through reflection, so bound reflect-driven code —
// html/template, encoding/json — sees its fields. A plain
// *runtime.TypeDef carries no field knowledge: the keyed literal traps
// "has no field". Where a host typedef is unavailable, the boundary is
// to keep the value-producing package source-interpreted
// ([WithPackageModes] ModeSource): script-declared structs marshal into
// bound templates field-by-field.
func (e *Engine) Bind(importPath string, symbols map[string]runtime.Value, opts ...BindOption) {
	p := e.bindTable(importPath, symbols, opts...)
	e.mu.Lock()
	tdKeys, seq := e.bindPublishLocked(importPath, p)
	e.explicitBinds[importPath] = true
	e.mu.Unlock()
	e.bindInternTypedefs(p, tdKeys, seq)
}

// bindTable builds the ready package a Bind publishes. The host policy
// filter and the per-symbol host conversions run outside the engine
// lock: either may read engine state (a policy that queries BoundOf
// would otherwise deadlock).
func (e *Engine) bindTable(importPath string, symbols map[string]runtime.Value, opts ...BindOption) *runtime.Package {
	cfg := bindConfig{name: lastElem(importPath)}
	for _, opt := range opts {
		opt(&cfg)
	}
	p := &runtime.Package{
		Path: importPath,
		Name: cfg.name,
		PackageInstance: runtime.PackageInstance{
			Globals:  runtime.NewEnv(),
			Specials: e.specials,
		},
		// bound paths without a dot in the first element stand in for GOROOT.
		Standard: !strings.Contains(strings.SplitN(importPath, "/", 2)[0], "."),
	}
	p.MarkHostReady()
	for k, v := range symbols {
		if e.hostPolicy != nil && !e.hostPolicy(importPath, k) {
			continue
		}
		v = hostSymbol(cfg.name+"."+k, v)
		p.Globals.Set(k, v)
	}
	return p
}

// bindPublishLocked publishes a bound table: the pending registration
// clears, pkgs answers loads (unless a source package is being
// preserved), names serves the bound queries, and the table's funcs
// and typedefs adopt it as their owner. The engine lock is held;
// adoption runs inside it so a skipped publish never re-points shared
// symbols at an unbound package.
func (e *Engine) bindPublishLocked(importPath string, p *runtime.Package) ([]string, uint64) {
	var tdKeys []string
	for _, k := range p.Globals.Names() {
		v, _ := p.Globals.Get(k)
		if bf, ok := v.(*runtime.BuiltinFunc); ok {
			runtime.AdoptBuiltinOwner(bf, p)
		}
		if td, ok := v.(*runtime.TypeDef); ok {
			if td.Pkg == nil {
				td.Pkg = p
			}
			tdKeys = append(tdKeys, k)
		}
	}
	e.pending.Clear(importPath)
	if _, skip := e.preservePkgs[importPath]; !skip {
		e.pkgs[importPath] = p
	}
	e.names.Set(importPath, p)
	e.bindSeq++
	return tdKeys, e.bindSeq
}

// bindInternTypedefs records each bound typedef as the canonical
// descriptor for its TypeID (M4-3c) — a synthesized twin resolves to it,
// and Store replaces so a re-Bind of the same type redefines it.
// Interning runs outside the engine lock: spelling a TypeID can install
// a lazily-bound package, which re-takes e.mu. Two binds may therefore
// interleave publish and intern: Store is ordered by the publish's seq
// so a straggling older bind cannot roll a TypeID's descriptor back
// past a completed re-Bind.
func (e *Engine) bindInternTypedefs(p *runtime.Package, keys []string, seq uint64) {
	type internEntry struct {
		tid runtime.TypeID
		td  *runtime.TypeDef
	}
	var pending []internEntry
	for _, k := range keys {
		if v, ok := p.Globals.Get(k); ok {
			if td, ok := v.(*runtime.TypeDef); ok {
				if tid := e.typeIDOf(td); tid != "" && tid != "?" {
					pending = append(pending, internEntry{tid, td})
				}
			}
		}
	}
	if len(pending) == 0 || e.typeIntern == nil {
		return
	}
	e.bindInternMu.Lock()
	defer e.bindInternMu.Unlock()
	for _, it := range pending {
		if seq > e.bindInternSeq[it.tid] {
			e.typeIntern.Store(it.tid, it.td)
			e.bindInternSeq[it.tid] = seq
		}
	}
}
