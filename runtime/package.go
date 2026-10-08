package runtime

import (
	"fmt"
	"go/token"
	"sync"
	"sync/atomic"

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

// Env is a name -> Value map for package globals (and builtins). It is
// goroutine-safe: spawned goroutines read and write the same globals.
type Env struct {
	mu sync.RWMutex
	m  map[string]Value
	// gen bumps on every binding change (and on Touch): the VM's
	// per-site global caches stay valid only while it is unchanged.
	gen atomic.Uint64
}

// NewEnv creates an empty Env.
func NewEnv() *Env { return &Env{m: map[string]Value{}} }

// Get returns the value bound to name.
func (e *Env) Get(name string) (Value, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v, ok := e.m[name]
	return v, ok
}

// Set binds name to v.
func (e *Env) Set(name string, v Value) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.m[name] = v
	e.gen.Add(1)
}

// Delete removes the binding for name, if present.
func (e *Env) Delete(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.m, name)
	e.gen.Add(1)
}

// Gen reports the binding generation: it changes whenever a binding is
// set or deleted, or Touch is called.
func (e *Env) Gen() uint64 { return e.gen.Load() }

// Touch invalidates caches keyed on Gen without changing a binding —
// for edits to the name-resolution context around the env (a package's
// file scopes, imports or index).
func (e *Env) Touch() { e.gen.Add(1) }

// Names lists bound names.
func (e *Env) Names() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
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
		defer func() {
			// a panic inside Load still consumes the once — record it as an
			// error so later calls report the failure instead of (nil, nil).
			if pr := recover(); pr != nil {
				r.pkg, r.err = nil, fmt.Errorf("import %s: %v", r.Path, pr)
				panic(pr)
			}
		}()
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

	state atomic.Int32 // a State value — member access can come from any goroutine
	// indexed closes when the package finishes indexing (success or
	// failure): a goroutine racing a cold load waits for it instead of
	// seeing a half-indexed package. Nil on bound host packages, which
	// are born Ready.
	indexed chan struct{}

	initOnce sync.Once
	initErr  error
	// Bootstrap builds and runs the package initializer (var/const decls +
	// init() funcs). Injected by the engine; called exactly once. The run
	// argument executes the __init__ function — supplied by the triggering
	// caller so a spawned goroutine's init runs on ITS VM.
	Bootstrap func(p *Package, run func(*Function) error) error

	constOnce sync.Once
	constErr  error
	// ConstBootstrap builds and runs the const-only initializer (const
	// decls, no var specs, no init() funcs) — injected by the engine.
	// A constant binds without the package's side effects; a failure or
	// a nil hook only means callers fall back to the full Bootstrap.
	ConstBootstrap func(p *Package, run func(*Function) error) error

	// RunInit is the default runner used when EnsureReady fires with no
	// explicit VM (host-side callers): the engine installs a fresh-VM
	// runner so init never shares the caller's interpreter state.
	RunInit func(*Function) error

	// LazyInit answers type/signature queries (functions and type decls)
	// without running initializers — set by the engine's InitMode.
	LazyInit bool

	// Specials is the engine's special-form registry (canonical symbol ->
	// handler); the compiler consults it to emit OpSpecialCall.
	Specials map[SymbolID]SpecialFunc

	matMu sync.Mutex
	matM  map[*index.Decl]Value // materialization dedup: one TypeDef/Function identity per decl
}

// State reports the package lifecycle stage.
func (p *Package) State() State { return State(p.state.Load()) }

// SetState records the package lifecycle stage.
func (p *Package) SetState(s State) { p.state.Store(int32(s)) }

// MarkIndexed arms the indexed gate (called when the package is built).
func (p *Package) MarkIndexed() {
	if p.indexed == nil {
		p.indexed = make(chan struct{})
	}
}

// FinishIndexing closes the indexed gate.
func (p *Package) FinishIndexing() {
	if p.indexed != nil {
		close(p.indexed)
	}
}

// MatCache returns the materialized value for decl d, building it once
// through build when missing. Two goroutines materializing the same decl
// share one TypeDef/Function identity — type asserts depend on it. build
// runs outside the lock (it may resolve other decls, which would
// deadlock on a held lock); the loser of a concurrent build discards
// its value so every caller converges on the first stored identity.
func (p *Package) MatCache(d *index.Decl, build func(*Package, *index.Decl) (Value, error)) (Value, error) {
	p.matMu.Lock()
	v, ok := p.matM[d]
	p.matMu.Unlock()
	if ok {
		return v, nil
	}
	v, err := build(p, d)
	if err != nil || v == nil {
		return v, err
	}
	p.matMu.Lock()
	defer p.matMu.Unlock()
	if p.matM == nil {
		p.matM = map[*index.Decl]Value{}
	}
	if prev, ok := p.matM[d]; ok {
		return prev, nil
	}
	p.matM[d] = v
	return v, nil
}

// EnsureReady advances the package through Initialize to Ready, running
// __init__ through p.RunInit (the engine's default runner).
func (p *Package) EnsureReady() error {
	return p.EnsureReadyRun(nil)
}

// EnsureReadyRun is EnsureReady with an explicit runner: run executes the
// package __init__ on the caller's VM — a spawned goroutine initializing a
// package runs it on ITS VM, never the engine's root VM. run==nil falls
// back to p.RunInit.
func (p *Package) EnsureReadyRun(run func(*Function) error) error {
	p.initOnce.Do(func() {
		if p.State() == Ready {
			return
		}
		p.SetState(Initializing)
		if err := p.runBootstrap(run); err != nil {
			p.initErr = err
			p.SetState(Failed)
			return
		}
		p.SetState(Ready)
	})
	return p.initErr
}

// runBootstrap invokes p.Bootstrap, converting a panic into initErr (and
// Failed state) before re-panicking: sync.Once consumes a panicked Do as
// done, so without recording it later callers would see a successful-but-
// partial init.
func (p *Package) runBootstrap(run func(*Function) error) (err error) {
	if p.Bootstrap == nil {
		return nil
	}
	if run == nil {
		run = p.RunInit
	}
	if run == nil {
		return fmt.Errorf("package %s: no init runner", p.Name)
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("panic: %v", r)
			}
			// the re-panic still consumes initOnce — without recording the
			// failure here the package would stay Initializing forever and
			// later member access would silently see partial state.
			p.initErr = err
			p.SetState(Failed)
			panic(r)
		}
	}()
	return p.Bootstrap(p, run)
}

// Member returns an exported member of the package: globals first, then the
// index (functions/types are materialized on demand via materialize).
// materialize is engine-provided and builds *Function / *TypeDef objects.
func (p *Package) Member(name string, materialize func(*Package, *index.Decl) (Value, error)) (Value, error) {
	return p.MemberV(name, materialize, nil)
}

// EnsureConstsRun binds the package's constants through ConstBootstrap —
// the const-only initializer that runs none of the var initializers or
// init() funcs. It runs at most once: a skipped or failed const init
// (recorded in constErr) leaves the package for the full initializer.
// Unlike runBootstrap the error is not re-panicked; the caller decides
// between the bound value and a full-init retry.
func (p *Package) EnsureConstsRun(run func(*Function) error) error {
	p.constOnce.Do(func() {
		if p.State() == Ready || p.ConstBootstrap == nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				if e, ok := r.(error); ok {
					p.constErr = e
				} else {
					p.constErr = fmt.Errorf("panic: %v", r)
				}
			}
		}()
		if run == nil {
			run = p.RunInit
		}
		if run == nil {
			p.constErr = fmt.Errorf("package %s: no init runner", p.Name)
			return
		}
		p.constErr = p.ConstBootstrap(p, run)
	})
	return p.constErr
}

// MemberV is Member with an explicit init runner (see EnsureReadyRun).
func (p *Package) MemberV(name string, materialize func(*Package, *index.Decl) (Value, error), run func(*Function) error) (Value, error) {
	if p.indexed != nil {
		<-p.indexed
	}
	// A failed initialization must not go unnoticed: globals registered
	// before the failure are partial state, so surface the error instead.
	if p.State() == Failed {
		if err := p.EnsureReadyRun(run); err != nil {
			return nil, err
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
	// constants need no init: a const member binds through the const-only
	// initializer first. When that can't serve the name (var references,
	// a skipped or failed run), the full initializer below covers it.
	if p.Index != nil {
		if d, ok := memberDecl(p.Index, name); ok && d.Kind == index.ConstDecl {
			if err := p.EnsureConstsRun(run); err == nil {
				if v, ok := p.Globals.Get(name); ok {
					return v, nil
				}
			}
		}
	}
	if err := p.EnsureReadyRun(run); err != nil {
		return nil, err
	}
	if v, ok := p.Globals.Get(name); ok {
		return v, nil
	}
	if p.Index != nil {
		if d, ok := memberDecl(p.Index, name); ok {
			if materialize == nil {
				return nil, fmt.Errorf("no materializer for %s.%s", p.Name, name)
			}
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
