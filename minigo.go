// Package minigo is a redesigned Go interpreter on a stack VM.
//
// Three pillars: Lazy Go (packages parse/index on demand, initialize on
// first member touch), Executable Go (functions compile to bytecode on first
// call), Quoted Go (special forms — wired in a later phase).
//
// The central invariant: every syntactically valid Go source parses and
// compiles; unsupported constructs become OpTrap instructions that fail at
// run time with position info instead of at parse/compile time.
package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/compile"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
	"github.com/podhmo/minigo/vm"
)

// Engine is a long-lived interpreter instance: resolver, package cache,
// builtins, and host bindings.
type Engine struct {
	resolver resolve.Resolver
	cfg      resolve.BuildConfig
	fset     *token.FileSet

	builtins   *runtime.Env
	vmm        *vm.VM
	initMode   InitMode
	specials   map[runtime.SymbolID]runtime.SpecialFunc
	hostPolicy func(importPath, symbol string) bool // nil = allow all bound intrinsics
	out        io.Writer                            // print/println/fmt.Print* destination; nil = io.Discard
	cwd        string                               // virtual cwd for os.* path intrinsics (defaults to startDir)

	mu    sync.Mutex
	pkgs  map[string]*runtime.Package // by import path
	byDir map[string]*runtime.Package // synthetic packages by dir
	files map[string]*runtime.Package // single-file packages by abs path
	binds map[string]*runtime.Package // host-bound packages (sessions inherit)
	srcs  map[string]*runtime.Package // source packages behind bound paths (inspect.SourceOf)
}

// Option configures an Engine.
type Option func(*Engine)

// InitMode selects how eagerly packages run their initializers.
type InitMode int

const (
	// GoCompatibleInit runs a package's initializers on first member access
	// (the default): importing or calling into a package behaves like Go —
	// vars/consts and init() have run before the member is used.
	GoCompatibleInit InitMode = iota
	// LazyInit answers member queries without running initializers: types
	// and function signatures materialize while var/const/init side effects
	// stay pending. Useful when a tool wants names/types without execution.
	LazyInit
)

// WithBuildConfig sets GOOS/GOARCH/build tags for file selection.
func WithBuildConfig(cfg resolve.BuildConfig) Option {
	return func(e *Engine) { e.cfg = cfg }
}

// WithInitMode sets how eagerly package initializers run (see InitMode).
func WithInitMode(m InitMode) Option {
	return func(e *Engine) { e.initMode = m }
}

// WithAllowedRoots restricts the directories the resolver may hand out:
// entry points and located packages must live inside one of the roots
// (see resolve.BuildConfig.AllowedRoots).
func WithAllowedRoots(roots ...string) Option {
	return func(e *Engine) { e.cfg.AllowedRoots = roots }
}

// WithHostPolicy gates which bound host symbols are visible to scripts:
// deny (returns false) removes the symbol from the package's environment,
// so a script referencing it gets "undefined". Applied at Bind time for
// every bound package (std intrinsics and user binds alike).
func WithHostPolicy(allow func(importPath, symbol string) bool) Option {
	return func(e *Engine) { e.hostPolicy = allow }
}

// WithOutput sets the writer print/println and fmt.Print/Printf/Println
// write to. The default io.Discard keeps script output silent; a session
// or REPL passes os.Stdout or a buffer to observe it.
func WithOutput(w io.Writer) Option {
	return func(e *Engine) { e.out = w }
}

// WithWorkingDir sets the engine's virtual working directory: relative
// paths in the os.* / path/filepath intrinsics resolve against it, and
// os.Getwd reports it. It deliberately does NOT chdir the host process —
// the real cwd stays observable through host.Getwd. Defaults to startDir.
func WithWorkingDir(dir string) Option {
	return func(e *Engine) {
		if abs, err := filepath.Abs(dir); err == nil {
			e.cwd = abs
		}
	}
}

// WorkingDir reports the engine's virtual working directory — the anchor
// relative paths in os.* / path/filepath intrinsics resolve against (see
// WithWorkingDir). Host integrations use it to default subprocess cwd.
func (e *Engine) WorkingDir() string { return e.cwd }

// fsPath maps a script-visible path to a host path: relative paths anchor
// at the engine's virtual cwd, and the result is checked against
// AllowedRoots (per-call — the restricted-mode policy lives here, not in
// symbol gating, so file APIs are available but confined).
func (e *Engine) fsPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.cwd, p)
	}
	p = filepath.Clean(p)
	if err := e.cfg.CheckPath(p); err != nil {
		return "", err
	}
	return p, nil
}

// NewEngine creates an engine whose default resolver is go-scan
// (locator.WithGoModuleResolver). startDir is used to locate the go.mod /
// go.work anchor for import-path resolution.
func NewEngine(startDir string, opts ...Option) *Engine {
	e := &Engine{
		fset:     token.NewFileSet(),
		pkgs:     map[string]*runtime.Package{},
		byDir:    map[string]*runtime.Package{},
		files:    map[string]*runtime.Package{},
		binds:    map[string]*runtime.Package{},
		srcs:     map[string]*runtime.Package{},
		specials: map[runtime.SymbolID]runtime.SpecialFunc{},
	}
	for _, o := range opts {
		o(e)
	}
	if e.cwd == "" {
		if abs, err := filepath.Abs(startDir); err == nil {
			e.cwd = abs
		}
	}
	e.builtins = builtins(e)
	res, err := resolve.NewGoScanResolver(startDir, e.cfg)
	if err != nil {
		// fall back to GOPATH-less dir resolution only; Locate() will still
		// work for relative/GOROOT paths.
		res = nil
	}
	e.resolver = res
	e.vmm = e.newVM()
	e.installStdlib()
	return e
}

// newVM wires a VM to this engine's hooks.
func (e *Engine) newVM() *vm.VM {
	return &vm.VM{H: vm.Hooks{
		Builtin:           e.builtins.Get,
		Materialize:       e.materialize,
		CompileExpr:       compile.Expr,
		CompileScopedExpr: compile.ExprScoped,
		Special:           func(id runtime.SymbolID) (runtime.SpecialFunc, bool) { h, ok := e.specials[id]; return h, ok },
		MethodsOf:         e.methodsOfValue,
		IfaceReqs:         e.ifaceReqs,
		FindMethod:        e.findMethod,
		ElemOf:            e.elemOf,
		TypeMethods:       e.typeMethods,
		Underlying:        e.underlying,
		AliasOf:           e.aliasOf,
		FieldTypes:        e.fieldTypes,
		ResolveType:       e.resolveTypeRef,
	}}
}

// NewSession returns a fresh engine sharing this engine's resolver, build
// config, specials, host policy and output writer — but with an empty
// package cache and its own VM. Repeated script runs in a REPL each get
// isolated state while keeping one resolution/indexing setup.
func (e *Engine) NewSession() *Engine {
	s := &Engine{
		resolver:   e.resolver,
		cfg:        e.cfg,
		fset:       token.NewFileSet(),
		initMode:   e.initMode,
		specials:   e.specials,
		hostPolicy: e.hostPolicy,
		out:        e.out,
		cwd:        e.cwd,
		pkgs:       map[string]*runtime.Package{},
		byDir:      map[string]*runtime.Package{},
		files:      map[string]*runtime.Package{},
		binds:      map[string]*runtime.Package{},
		srcs:       map[string]*runtime.Package{},
	}
	s.builtins = builtins(s)
	s.vmm = s.newVM()
	s.installStdlib()
	// sessions inherit the parent's host bindings: a script importing a
	// custom bound package must resolve identically in the new session.
	// Intrinsic packages the session already bound itself keep the
	// session version — their builtins close over the session engine
	// and its package cache, so e.g. inspect.PackageOf observes the
	// session's (patched) packages, not the parent's copy.
	e.mu.Lock()
	for path, p := range e.binds {
		if _, installed := s.binds[path]; installed {
			continue
		}
		s.binds[path] = p
		s.pkgs[path] = p
	}
	e.mu.Unlock()
	return s
}

// Result wraps a Run return value with a typed accessor.
type Result struct {
	V runtime.Value
}

// RunResult is Run plus a Result so callers can unmarshal directly.
func (e *Engine) RunResult(ctx context.Context, ref, fnName string, args ...runtime.Value) (*Result, error) {
	v, err := e.Run(ctx, ref, fnName, args...)
	if err != nil {
		return nil, err
	}
	return &Result{V: v}, nil
}

// As unmarshals the result into dst (a pointer). Scalars assign directly
// when the types agree; composite minigo values (structs, slices, maps)
// are re-decoded via reflection, mapping field names and indices.
func (r *Result) As(dst any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("As: dst must be a non-nil pointer")
	}
	return assignReflect(rv.Elem(), r.V)
}

// assignReflect copies a minigo value into a reflect-visible destination.
func assignReflect(dst reflect.Value, v runtime.Value) error {
	if !dst.CanSet() {
		return fmt.Errorf("cannot set %s", dst.Type())
	}
	switch x := v.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		switch dst.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
			dst.SetZero()
			return nil
		}
		return fmt.Errorf("cannot assign nil to %s", dst.Type())
	case *runtime.Cell:
		if dst.Kind() == reflect.Ptr {
			p := reflect.New(dst.Type().Elem())
			if err := assignReflect(p.Elem(), x.Elem); err != nil {
				return err
			}
			dst.Set(p)
			return nil
		}
		return assignReflect(dst, x.Elem)
	case *runtime.Named:
		return assignReflect(dst, x.V)
	case *runtime.Struct:
		if dst.Kind() != reflect.Struct {
			return fmt.Errorf("cannot assign struct to %s", dst.Type())
		}
		for i, name := range x.Def.Fields {
			if name == "" {
				continue
			}
			fld := dst.FieldByName(name)
			if !fld.IsValid() {
				continue
			}
			if err := assignReflect(fld, x.Fields[i]); err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
		}
		return nil
	case *runtime.Slice:
		if dst.Kind() != reflect.Slice {
			return fmt.Errorf("cannot assign slice to %s", dst.Type())
		}
		out := reflect.MakeSlice(dst.Type(), len(x.Elems), len(x.Elems))
		for i, el := range x.Elems {
			if err := assignReflect(out.Index(i), el); err != nil {
				return err
			}
		}
		dst.Set(out)
		return nil
	case *runtime.Map:
		if dst.Kind() != reflect.Map {
			return fmt.Errorf("cannot assign map to %s", dst.Type())
		}
		out := reflect.MakeMap(dst.Type())
		for k, mv := range x.Pairs {
			kv := reflect.New(dst.Type().Key()).Elem()
			if err := assignReflect(kv, k); err != nil {
				return err
			}
			vv := reflect.New(dst.Type().Elem()).Elem()
			if err := assignReflect(vv, mv); err != nil {
				return err
			}
			out.SetMapIndex(kv, vv)
		}
		dst.Set(out)
		return nil
	case *runtime.GoValue:
		gv := reflect.ValueOf(x.V)
		if gv.IsValid() && gv.Type().AssignableTo(dst.Type()) {
			dst.Set(gv)
			return nil
		}
		return fmt.Errorf("cannot assign %T to %s", x.V, dst.Type())
	case *runtime.Tuple:
		return fmt.Errorf("cannot assign tuple to %s", dst.Type())
	default:
		gv := reflect.ValueOf(v)
		if gv.IsValid() && gv.Type().AssignableTo(dst.Type()) {
			dst.Set(gv)
			return nil
		}
		if gv.IsValid() && gv.Type().ConvertibleTo(dst.Type()) &&
			(gv.Kind() >= reflect.Int && gv.Kind() <= reflect.Float64 || gv.Kind() == reflect.String || gv.Kind() == reflect.Bool) {
			dst.Set(gv.Convert(dst.Type()))
			return nil
		}
		return fmt.Errorf("cannot assign %T to %s", v, dst.Type())
	}
}

// RegisterSpecial installs a special-form handler for a canonical symbol
// (import path + member name). Calls shaped `pkgAlias.Name(args...)` whose
// alias resolves to that package compile to OpSpecialCall: the handler
// fires at run time with the call's AST quoted.
func (e *Engine) RegisterSpecial(id runtime.SymbolID, h runtime.SpecialFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.specials[id] = h
}

// WithResolver swaps the resolver (e.g. a testing stub).
func (e *Engine) WithResolver(r resolve.Resolver) *Engine {
	e.resolver = r
	return e
}

// Package returns a loaded (Indexed) package by import path or directory.
func (e *Engine) Package(ctx context.Context, ref string) (*runtime.Package, error) {
	if resolve.LooksLikeDir(ref) {
		return e.loadDir(ctx, ref)
	}
	return e.loadPath(ctx, ref)
}

// Call invokes a named member of a package: fn may be a function or anything
// callable. The package is Initialized first if needed.
func (e *Engine) Call(ctx context.Context, pkg *runtime.Package, name string, args ...runtime.Value) (runtime.Value, error) {
	member, err := pkg.Member(name, e.materialize)
	if err != nil {
		return nil, err
	}
	return e.vmm.Call(member, args)
}

// Run is the high-level entry point: locate ref (dir or import path), ensure
// the package is ready, then call fnName(args...). A bare "" fnName calls
// main.main.
func (e *Engine) Run(ctx context.Context, ref, fnName string, args ...runtime.Value) (runtime.Value, error) {
	if fnName == "" {
		fnName = "main"
	}
	pkg, err := e.Package(ctx, ref)
	if err != nil {
		return nil, err
	}
	return e.Call(ctx, pkg, fnName, args...)
}

// Bind registers a host package: import path -> symbols. The package is
// marked Ready (no source needed). This is the host-extension point.
// WithHostPolicy filters symbols: denied names stay undefined in scripts.
func (e *Engine) Bind(importPath string, symbols map[string]runtime.Value) {
	p := &runtime.Package{
		Path:     importPath,
		Name:     lastElem(importPath),
		State:    runtime.Ready,
		Globals:  runtime.NewEnv(),
		Specials: e.specials,
		// bound paths without a dot in the first element stand in for the
		// GOROOT package of that name ("strings", "fmt", "unsafe").
		Standard: !strings.Contains(strings.SplitN(importPath, "/", 2)[0], "."),
	}
	for k, v := range symbols {
		if e.hostPolicy != nil && !e.hostPolicy(importPath, k) {
			continue
		}
		if bf, ok := v.(*runtime.BuiltinFunc); ok && (bf.Pkg == nil || bf.Pkg.Path == p.Path) {
			// the same BuiltinFunc may be bound under several paths
			// (minigo.dev/x and its module path): the first owner wins,
			// but a re-Bind under the SAME path must re-point at the
			// new live package object, not the stale one
			bf.Pkg = p
		}
		p.Globals.Set(k, v)
	}
	e.mu.Lock()
	e.pkgs[importPath] = p
	e.binds[importPath] = p
	e.mu.Unlock()
}

func lastElem(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// ---- loading ----

func (e *Engine) loadPath(ctx context.Context, path string) (*runtime.Package, error) {
	e.mu.Lock()
	if p, ok := e.pkgs[path]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	if e.resolver == nil {
		return nil, fmt.Errorf("minigo: no resolver configured; cannot import %q", path)
	}
	meta, err := e.resolver.Locate(ctx, "", path)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", path, err)
	}
	return e.buildPackage(meta)
}

func (e *Engine) loadDir(ctx context.Context, dir string) (*runtime.Package, error) {
	e.mu.Lock()
	if p, ok := e.byDir[dir]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	meta, err := e.resolver.LocateDir(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("resolve dir %q: %w", dir, err)
	}
	return e.buildPackage(meta)
}

// SourceOf loads the source behind an import path, bypassing a bound
// shadow (inspect.SourceOf): Bind-registered packages answer member
// lookups through e.pkgs, so PackageOf("strings") yields the bound
// object whose index is nil. The source package is built into a private
// cache — never e.pkgs or e.byDir — so the bound package keeps answering
// real imports. For an unbound path there is nothing to bypass: the
// canonical package is returned.
func (e *Engine) SourceOf(ctx context.Context, path string) (*runtime.Package, error) {
	if _, bound := e.binds[path]; !bound {
		return e.loadPath(ctx, path)
	}
	e.mu.Lock()
	if p, ok := e.srcs[path]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()
	if e.resolver == nil {
		return nil, fmt.Errorf("minigo: no resolver configured; cannot load source of %q", path)
	}
	meta, err := e.resolver.Locate(ctx, "", path)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", path, err)
	}
	p := e.newPackage(meta.ImportPath, meta.Name, meta.Dir)
	p.Standard = meta.Standard
	var files []*syntax.File
	for _, f := range meta.GoFiles {
		sf, err := syntax.ParseFile(e.fset, f, nil)
		if err != nil {
			p.State = runtime.Failed
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		files = append(files, sf)
	}
	if err := e.indexFiles(p, files); err != nil {
		p.State = runtime.Failed
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if q, ok := e.srcs[path]; ok {
		return q, nil
	}
	e.srcs[path] = p
	return p, nil
}

// buildPackage runs the pipeline Locate->Parse->Index for one package.
// Initialize stays lazy (Bootstrap hook).
func (e *Engine) buildPackage(meta *resolve.PackageMeta) (*runtime.Package, error) {
	e.mu.Lock()
	if p, ok := e.pkgs[meta.ImportPath]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	p := e.newPackage(meta.ImportPath, meta.Name, meta.Dir)
	p.Standard = meta.Standard
	// publish before parsing to make import cycles convergent
	e.mu.Lock()
	e.pkgs[meta.ImportPath] = p
	if meta.Dir != "" {
		e.byDir[meta.Dir] = p
	}
	e.mu.Unlock()

	var files []*syntax.File
	for _, f := range meta.GoFiles {
		sf, err := syntax.ParseFile(e.fset, f, nil)
		if err != nil {
			p.State = runtime.Failed
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		files = append(files, sf)
	}
	if err := e.indexFiles(p, files); err != nil {
		p.State = runtime.Failed
		return nil, err
	}
	return p, nil
}

// newPackage builds a Package shell wired to this engine (Parsed state).
func (e *Engine) newPackage(path, name, dir string) *runtime.Package {
	return &runtime.Package{
		Path:     path,
		Name:     name,
		State:    runtime.Parsed,
		Dir:      dir,
		Fset:     e.fset,
		LazyInit: e.initMode == LazyInit,
		Globals:  runtime.NewEnv(),
		Scopes:   map[*syntax.File]map[string]*runtime.ImportRef{},
		Imports:  map[*syntax.File][]*runtime.ImportRef{},
		Specials: e.specials,
	}
}

// indexFiles finishes a package over already-selected files: declaration
// index, file-by-name map, per-file import scopes, and the lazy initializer
// hook. Directory discovery and build-constraint filtering happen upstream.
func (e *Engine) indexFiles(p *runtime.Package, files []*syntax.File) error {
	p.Files = files
	p.FileByName = map[string]*syntax.File{}
	for _, sf := range files {
		p.FileByName[sf.Name] = sf
	}

	ix, err := index.Build(files)
	if err != nil {
		return fmt.Errorf("index %s: %w", p.Path, err)
	}
	p.Index = ix
	p.State = runtime.Indexed

	// file-scope import refs
	for _, sf := range files {
		m := map[string]*runtime.ImportRef{}
		for _, imp := range sf.Imports {
			ref := &runtime.ImportRef{
				Path:  imp.Path,
				Alias: imp.Alias,
				Load:  func(path string) (*runtime.Package, error) { return e.loadPath(context.Background(), path) },
			}
			p.Imports[sf] = append(p.Imports[sf], ref)
			if imp.Alias != "_" && imp.Alias != "." {
				m[imp.LocalName()] = ref
			}
		}
		p.Scopes[sf] = m
	}

	p.Bootstrap = e.bootstrap
	return nil
}

// LoadFile builds a package out of one named file, bypassing directory
// discovery and build-constraint filtering: a DSL file guarded by
// `//go:build codegen` — or a script outside any package — is a first-class
// entry point. The file's own imports still resolve through the resolver.
// The package's import path is synthetic ("<file>" + abs path); nothing can
// import it back. AllowedRoots applies to the file's directory, like any
// other entry point.
func (e *Engine) LoadFile(ctx context.Context, filename string) (*runtime.Package, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if p, ok := e.files[abs]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	if err := e.cfg.CheckDir(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	sf, err := syntax.ParseFile(e.fset, abs, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	p := e.newPackage("<file>"+abs, sf.AST.Name.Name, filepath.Dir(abs))
	if err := e.indexFiles(p, []*syntax.File{sf}); err != nil {
		return nil, err
	}

	// Publish only once the package is fully indexed: file packages cannot
	// be imported back, so nothing needs the early visibility that
	// buildPackage's import cycles do.
	e.mu.Lock()
	if q, ok := e.files[abs]; ok {
		e.mu.Unlock()
		return q, nil
	}
	e.files[abs] = p
	e.mu.Unlock()
	return p, nil
}

// RunFile is Run for a single file: LoadFile + Call. A bare "" fnName calls
// the file's main function.
func (e *Engine) RunFile(ctx context.Context, filename, fnName string, args ...runtime.Value) (runtime.Value, error) {
	if fnName == "" {
		fnName = "main"
	}
	pkg, err := e.LoadFile(ctx, filename)
	if err != nil {
		return nil, err
	}
	return e.Call(ctx, pkg, fnName, args...)
}

// bootstrap runs the synthetic __init__ function of a package.
func (e *Engine) bootstrap(p *runtime.Package) error {
	for _, sf := range p.Files {
		for _, ref := range p.Imports[sf] {
			if ref.Alias != "_" {
				continue
			}
			imported, err := ref.Materialize()
			if err != nil {
				return fmt.Errorf("initialize blank import %q: %w", ref.Path, err)
			}
			if err := imported.EnsureReady(); err != nil {
				return fmt.Errorf("initialize blank import %q: %w", ref.Path, err)
			}
		}
	}
	ch, err := compile.InitFunc(p)
	if err != nil {
		return err
	}
	bindCompiles(p, ch)
	fn := &runtime.Function{Pkg: p, Name: p.Name + ".__init__", Chunk: ch}
	_, err = e.vmm.Call(fn, nil)
	return err
}

// bindCompiles attaches the compile hook to *runtime.Function constants
// that still need lazy compilation. Function-literal protos carry their
// chunk already (eager compile) and have no Decl — hooking them would
// re-run compile.Func on a nil Decl.
func bindCompiles(p *runtime.Package, ch *bytecode.Chunk) {
	for _, cv := range ch.Consts {
		if fn, ok := cv.(*runtime.Function); ok {
			fn.Pkg = p
			if fn.Decl != nil && fn.Chunk == nil {
				fn.Compile = compile.Func
			}
		}
	}
}

// EvalExpr evaluates a single parsed expression in a package's scope — the
// OP_EVAL_AST migration bridge: the fragment travels as AST inside a chunk
// and compiles (compile.Expr) on first execution, so callers that hold
// interpreter-visible AST fragments can run them under the VM without
// committing to bytecode at build time. Names resolve exactly like inside
// a function body of file: package globals, that file's imports, builtins.
func (e *Engine) EvalExpr(ctx context.Context, pkg *runtime.Package, file *syntax.File, expr ast.Expr) (runtime.Value, error) {
	if err := pkg.EnsureReady(); err != nil {
		return nil, err
	}
	ch := &bytecode.Chunk{
		Name:   "<eval>",
		Consts: []any{&bytecode.ASTFragment{Expr: expr, File: file}},
		Code: []bytecode.Instruction{
			{Op: bytecode.OpEvalAST, A: 0, C: -1},
			{Op: bytecode.OpReturn, A: 1, C: -1},
		},
	}
	return e.vmm.Call(&runtime.Function{Pkg: pkg, File: file, Name: "<eval>", Chunk: ch}, nil)
}

// materialize builds the runtime value for one decl on first access.
func (e *Engine) materialize(pkg *runtime.Package, d *index.Decl) (runtime.Value, error) {
	switch d.Kind {
	case index.FuncDecl:
		return &runtime.Function{Pkg: pkg, File: d.File, Decl: d.Func, Name: d.Name,
			TParams:      typeParamNames(d.Func.Type.TypeParams),
			TConstraints: typeParamConstraints(d.Func.Type.TypeParams),
			Compile:      compile.Func}, nil
	case index.TypeDecl:
		return e.typeDefOf(pkg, d)
	case index.ConstDecl, index.VarDecl:
		// values appear in Globals at package init; reaching here means the
		// name was never bound (e.g. blank var) — return nil rather than fail.
		return runtime.NIL, nil
	default:
		return nil, fmt.Errorf("cannot materialize %s", d.Name)
	}
}

func (e *Engine) typeDefOf(pkg *runtime.Package, d *index.Decl) (runtime.Value, error) {
	ts := d.Spec.(*ast.TypeSpec)
	td := &runtime.TypeDef{Pkg: pkg, Name: d.Name, File: d.File, Spec: ts,
		TParams:      typeParamNames(ts.TypeParams),
		TConstraints: typeParamConstraints(ts.TypeParams),
		Anon:         ts.Type}
	switch t := ts.Type.(type) {
	case *ast.StructType:
		td.Kind = runtime.KindStruct
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				// embedded field: the field name is the base type's name
				td.EmbedSpecs = append(td.EmbedSpecs, f.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, embedBaseName(f.Type))
				continue
			}
			for _, n := range f.Names {
				td.Fields = append(td.Fields, n.Name)
			}
		}
	case *ast.InterfaceType:
		td.Kind = runtime.KindInterface
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				// embedded element: io.Reader, ~int unions, constraints
				td.IEmbeds = append(td.IEmbeds, m.Type)
				continue
			}
			for _, n := range m.Names {
				td.MReqs = append(td.MReqs, n.Name)
			}
		}
	case *ast.ArrayType:
		td.Kind = runtime.KindSlice
	case *ast.MapType:
		td.Kind = runtime.KindMap
	case *ast.FuncType:
		td.Kind = runtime.KindFunc
	case *ast.StarExpr:
		td.Kind = runtime.KindPointer
	case *ast.ChanType:
		td.Kind = runtime.KindChan
	case *ast.Ident:
		td.Kind = runtime.KindNamedBasic
	default:
		td.Kind = runtime.KindNamedBasic
	}
	if ts.Assign.IsValid() {
		td.Kind = runtime.KindAlias
	}
	// methods
	if info, ok := pkg.Index.Types[d.Name]; ok && len(info.Methods) > 0 {
		td.Methods = map[string]*runtime.Function{}
		for name, md := range info.Methods {
			td.Methods[name] = e.methodFunc(pkg, d.Name, md)
		}
	}
	return td, nil
}

// methodFunc builds the runtime.Function for one method decl — shared by
// typeDefOf (index materialization) and the REPL's :pin method grafts.
func (e *Engine) methodFunc(pkg *runtime.Package, recv string, md *index.Decl) *runtime.Function {
	_, ptrRecv := md.Func.Recv.List[0].Type.(*ast.StarExpr)
	return &runtime.Function{
		Pkg: pkg, File: md.File, Decl: md.Func, Name: recv + "." + md.Name,
		Recv: recv, PtrRecv: ptrRecv, Compile: compile.Func,
		// Go 1.27 generic methods: `func (l List[E]) Map[R any](...)`
		// — the method's own type params ride alongside the receiver's.
		TParams:      typeParamNames(md.Func.Type.TypeParams),
		TConstraints: typeParamConstraints(md.Func.Type.TypeParams),
	}
}
