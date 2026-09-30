package runtime

import (
	"fmt"
	"go/token"
	"sync"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// State is the package lifecycle stage.
type State int

const (
	Unseen State = iota
	Located
	Parsed
	Indexed
	Initializing
	Ready
	Failed
)

// Env is a name -> Value map for package globals (and builtins).
type Env struct {
	m map[string]Value
}

// NewEnv creates an empty Env.
func NewEnv() *Env { return &Env{m: map[string]Value{}} }

// Get returns the value bound to name.
func (e *Env) Get(name string) (Value, bool) {
	v, ok := e.m[name]
	return v, ok
}

// Set binds name to v.
func (e *Env) Set(name string, v Value) { e.m[name] = v }

// Delete removes the binding for name, if present.
func (e *Env) Delete(name string) { delete(e.m, name) }

// Names lists bound names.
func (e *Env) Names() []string {
	out := make([]string, 0, len(e.m))
	for k := range e.m {
		out = append(out, k)
	}
	return out
}

// ImportRef is the file-scope handle for one import. It materializes the
// target package (to Indexed) on first Lookup, and fully initializes it
// (Ready) on first member access.
type ImportRef struct {
	Path  string
	Alias string // "", "_", ".", or an identifier

	// AllNames bypasses the exported-name gate on member selection — the
	// REPL's :cd uses it so a pseudo dot-import can see unexported decls.
	AllNames bool

	// Load materializes the package to Indexed state (injected by loader).
	Load func(path string) (*Package, error)

	once sync.Once
	pkg  *Package
	err  error
}

// Materialize loads the package to Indexed (parses + indexes, no init).
func (r *ImportRef) Materialize() (*Package, error) {
	r.once.Do(func() {
		if r.Load == nil {
			r.err = fmt.Errorf("no loader for import %q", r.Path)
			return
		}
		r.pkg, r.err = r.Load(r.Path)
	})
	return r.pkg, r.err
}

// Package is a lazily materialized package.
type Package struct {
	Path     string
	Name     string
	State    State
	Dir      string
	Standard bool // inside GOROOT (or a bound stdlib stub)

	Fset  *token.FileSet
	Files []*syntax.File
	Index *index.Index

	Globals *Env // values populated at Initialize / on member access

	// Scopes maps each parsed file's named import refs by local name.
	Scopes map[*syntax.File]map[string]*ImportRef
	// Imports retains every import in source order, including dot and blank imports.
	Imports map[*syntax.File][]*ImportRef

	// FileByName maps an absolute source path to its file — used to recover
	// per-declaration file context from instruction positions (the synthetic
	// __init__ chunk mixes decls from several files).
	FileByName map[string]*syntax.File

	initOnce sync.Once
	initErr  error
	// Bootstrap builds and runs the package initializer (var/const decls +
	// init() funcs). Injected by the engine; called exactly once.
	Bootstrap func(*Package) error

	// LazyInit answers type/signature queries (functions and type decls)
	// without running initializers — set by the engine's InitMode.
	LazyInit bool

	// Specials is the engine's special-form registry (canonical symbol ->
	// handler); the compiler consults it to emit OpSpecialCall.
	Specials map[SymbolID]SpecialFunc
}

// EnsureReady advances the package through Initialize to Ready.
func (p *Package) EnsureReady() error {
	p.initOnce.Do(func() {
		if p.State == Ready {
			return
		}
		p.State = Initializing
		if p.Bootstrap != nil {
			if err := p.Bootstrap(p); err != nil {
				p.initErr = err
				p.State = Failed
				return
			}
		}
		p.State = Ready
	})
	return p.initErr
}

// Member returns an exported member of the package: globals first, then the
// index (functions/types are materialized on demand via materialize).
// materialize is engine-provided and builds *Function / *TypeDef objects.
func (p *Package) Member(name string, materialize func(*Package, *index.Decl) (Value, error)) (Value, error) {
	// A failed initialization must not go unnoticed: globals registered
	// before the failure are partial state, so surface the error instead.
	if p.State == Failed {
		if p.initErr != nil {
			return nil, p.initErr
		}
		return nil, fmt.Errorf("package %s failed to load", p.Name)
	}
	if v, ok := p.Globals.Get(name); ok {
		return v, nil
	}
	if p.Index != nil && p.LazyInit {
		// functions and type decls are queryable without running
		// initializers; var/const values require the package to be Ready
		if d, ok := memberDecl(p.Index, name); ok && (d.Kind == index.FuncDecl || d.Kind == index.TypeDecl) {
			if materialize == nil {
				return nil, fmt.Errorf("no materializer for %s.%s", p.Name, name)
			}
			return materialize(p, d)
		}
	}
	if err := p.EnsureReady(); err != nil {
		return nil, err
	}
	if v, ok := p.Globals.Get(name); ok {
		return v, nil
	}
	if p.Index != nil {
		if d, ok := memberDecl(p.Index, name); ok {
			return materialize(p, d)
		}
	}
	return nil, fmt.Errorf("undefined: %s.%s", p.Name, name)
}

func memberDecl(ix *index.Index, name string) (*index.Decl, bool) {
	if d, ok := ix.Funcs[name]; ok {
		return d, true
	}
	if d, ok := ix.Types[name]; ok && d.Decl != nil {
		return d.Decl, true
	}
	if d, ok := ix.Consts[name]; ok {
		return d, true
	}
	if d, ok := ix.Vars[name]; ok {
		return d, true
	}
	return nil, false
}
