// Package minigo embeds an interpreter for a large subset of Go.
//
// Create an [Engine] and run a function from a directory, import path or
// file:
//
//	e := minigo.NewEngine(".", minigo.WithOutput(os.Stdout))
//	v, err := e.Run(ctx, "./script", "Main")
//
// Packages are loaded lazily: imports are located and indexed on demand,
// initialized when a member is first touched, and functions are compiled
// when first called. Any syntactically valid Go source loads; a construct
// the interpreter does not support fails at run time, with its position,
// only if execution reaches it.
//
// An import is answered either by interpreting Go source or by a
// host-bound package: Go values registered with [Engine.Bind], or one of
// the built-in std intrinsics. A bound package shadows the source of the
// same path;
// [PackageMode] changes that per path. A special form, installed with
// [Engine.RegisterSpecial], is a host handler that receives a call's
// syntax instead of its evaluated arguments.
//
// Script values have type [runtime.Value]; see
// [github.com/podhmo/minigo/runtime] for the value model.
package minigo

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"reflect"
	"slices"
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

// Engine is a long-lived interpreter instance. It caches loaded packages,
// so values and package state persist across calls on the same Engine;
// use [Engine.NewSession] for isolated state. Create one with [NewEngine].
type Engine struct {
	// mainPkg is the package Run entered first — runtime/debug's build
	// info names it.
	mainPkg *runtime.Package

	resolver resolve.Resolver
	cfg      resolve.BuildConfig
	fset     *token.FileSet

	builtins   *runtime.Env
	initMode   InitMode
	specials   map[runtime.SymbolID]runtime.SpecialFunc
	hostPolicy func(importPath, symbol string) bool // nil = allow all bound intrinsics
	pkgModes   map[string]PackageMode               // per-path import mode; nil = ModeAuto everywhere
	out        io.Writer                            // print/println/fmt.Print* destination; nil = io.Discard
	cwd        string                               // virtual cwd for os.* path intrinsics (defaults to startDir)
	args       []string                             // script-visible os.Args; nil = host process argv

	mu    sync.Mutex
	pkgs  map[string]*runtime.Package // by import path
	byDir map[string]*runtime.Package // synthetic packages by dir
	files map[string]*runtime.Package // single-file packages by abs path
	binds map[string]*runtime.Package // host-bound packages (sessions inherit)
	srcs  map[string]*runtime.Package // source packages SourceOf builds past a bound shadow or a deny mode
	// lenient builds (inspect.DirOf/FileOf/PackageOf) are privately
	// cached — a lenient package in e.pkgs would hide a redeclare error
	// from a later strict load, and a strict package in e.byDir would
	// break the lenient view.
	pkgsLenient  map[string]*runtime.Package
	byDirLenient map[string]*runtime.Package
	filesLenient map[string]*runtime.Package
	links        map[*runtime.Function]linkEntry // //go:linkname resolutions, cached per decl

	// buildMu serializes package construction (locate/parse/index): two
	// goroutines cold-loading the same package converge on one build.
	buildMu sync.Mutex

	// sigMemo remembers interface-satisfaction signature comparisons
	// for every VM of this engine (vm.Hooks.SigMemo).
	sigMemo runtime.SigMemo
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

// WithInitMode sets the [InitMode]. The default is [GoCompatibleInit].
func WithInitMode(m InitMode) Option {
	return func(e *Engine) { e.initMode = m }
}

// PackageMode selects how an import path is satisfied.
type PackageMode int

const (
	// ModeAuto answers an import with a bound host package when one is
	// registered, else falls back to lazy source interpretation.
	ModeAuto PackageMode = iota
	// ModeSource forces source interpretation, bypassing a bound shadow:
	// the package is located, parsed, and indexed as if it were unbound.
	ModeSource
	// ModeDeny rejects the import outright — the script's import fails
	// with a clear error instead of half-working or trapping later.
	ModeDeny
)

// WithPackageModes sets the [PackageMode] for specific import paths.
// Paths not listed use [ModeAuto].
func WithPackageModes(modes map[string]PackageMode) Option {
	return func(e *Engine) { e.pkgModes = modes }
}

// WithAllowedRoots restricts scripts to the given directories: entry points
// and imported source packages must live inside one of the roots (see
// [resolve.BuildConfig.AllowedRoots]). Script file access is confined to
// the roots, and host capabilities that escape them (environment, argv,
// os/exec) are not bound.
func WithAllowedRoots(roots ...string) Option {
	return func(e *Engine) { e.cfg.AllowedRoots = roots }
}

// WithHostPolicy gates which bound host symbols are visible to scripts.
// When allow returns false the symbol is left out, so a script referencing
// it gets "undefined". It applies to every bound package, both std
// intrinsics and packages registered with [Engine.Bind].
func WithHostPolicy(allow func(importPath, symbol string) bool) Option {
	return func(e *Engine) { e.hostPolicy = allow }
}

// WithOutput sets the writer that print/println and fmt.Print/Printf/Println
// write to. The default is [io.Discard], so script output is silent unless
// an output is set.
func WithOutput(w io.Writer) Option {
	return func(e *Engine) { e.out = w }
}

// WithArgs sets the script-visible os.Args, so flag.Parse inside a script
// sees only its own flags. argv[0] is the program name, as in Go. Nil (the
// default) keeps the host process argv.
func WithArgs(argv []string) Option {
	return func(e *Engine) { e.args = argv }
}

// WithWorkingDir sets the engine's virtual working directory: relative
// paths in a script's os and path/filepath calls resolve against it, and
// os.Getwd reports it. The host process does not chdir; scripts still see
// the real directory through host.Getwd. The default is the startDir given
// to [NewEngine].
func WithWorkingDir(dir string) Option {
	return func(e *Engine) {
		if abs, err := filepath.Abs(dir); err == nil {
			e.cwd = abs
		}
	}
}

// WorkingDir reports the virtual working directory set by [WithWorkingDir].
// Host integrations use it as the default directory for subprocesses.
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

// NewEngine creates an engine. Import paths resolve against the go.mod or
// go.work found from startDir; [Engine.WithResolver] replaces the resolver.
func NewEngine(startDir string, opts ...Option) *Engine {
	e := &Engine{
		fset:         token.NewFileSet(),
		pkgs:         map[string]*runtime.Package{},
		byDir:        map[string]*runtime.Package{},
		files:        map[string]*runtime.Package{},
		filesLenient: map[string]*runtime.Package{},
		binds:        map[string]*runtime.Package{},
		srcs:         map[string]*runtime.Package{},
		pkgsLenient:  map[string]*runtime.Package{},
		byDirLenient: map[string]*runtime.Package{},
		specials:     map[runtime.SymbolID]runtime.SpecialFunc{},
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
		Special: func(id runtime.SymbolID) (runtime.SpecialFunc, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			h, ok := e.specials[id]
			return h, ok
		},
		MethodsOf:       e.methodsOfValue,
		MethodSetOf:     e.methodSetOfValue,
		IfaceReqs:       e.ifaceReqs,
		IfaceSigs:       e.ifaceSigReqs,
		MethodFuncsOf:   e.methodFuncsOfValue,
		TypeMethodFuncs: e.methodSet,
		SigMemo:         &e.sigMemo,

		ElemOf:      e.elemOf,
		TypeMethods: e.typeMethods,
		Underlying:  e.underlying,
		AliasOf:     e.aliasOf,
		FieldTypes:  e.fieldTypes,
		ResolveType: e.resolveTypeRef,
		HostTwin:    e.hostTwin,
		Linkname:    e.linknameTarget,
		HostErr:     hostErrOf,
	}}
}

// hostTwin implements the VM's HostTwin hook: the typedef this engine
// holds for the named host type rt. A source-built package (go/ast,
// io/fs) answers with its own declaration of the same name; a bound one
// with its bound typedef when that boxes rt. Packages nothing has
// imported yet answer nil: a value of a type from a package no script
// imports needs no twin, and a type check must not load one.
func (e *Engine) hostTwin(rt reflect.Type) *runtime.TypeDef {
	if rt == nil || rt.Name() == "" || rt.PkgPath() == "" {
		return nil
	}
	e.mu.Lock()
	p := e.pkgs[rt.PkgPath()]
	e.mu.Unlock()
	if p == nil {
		return nil
	}
	// published before it is parsed (import cycles): another goroutine
	// may still be filling Index and the scopes
	p.WaitIndexed()
	if p.Index != nil {
		td, err := e.resolveTypeRef(&runtime.TypeDef{Pkg: p}, ast.NewIdent(rt.Name()))
		if err != nil || !runtime.HostNamedIs(rt, td) {
			return nil
		}
		return td
	}
	if p.Globals == nil {
		return nil
	}
	v, ok := p.Globals.Get(rt.Name())
	if !ok {
		return nil
	}
	td, ok := v.(*runtime.TypeDef)
	if !ok || td.HostNew == nil {
		return nil
	}
	if ht := td.HostType(); ht == rt || (ht.Kind() == reflect.Pointer && ht.Elem() == rt) {
		return td
	}
	return nil
}

type linkEntry struct {
	v   runtime.Value
	ok  bool
	err error
}

// linknameTarget implements the VM's Linkname hook: a bodiless
// declaration carrying `//go:linkname local importpath.symbol` is wired
// to that target's real implementation — stdlib internals like
// net/http's readMIMEHeader (which links to net/textproto's) otherwise
// compile to the zero-return shim and silently lose parsed data. A
// one-arg directive exports the local name only, so it resolves nothing.
func (e *Engine) linknameTarget(vc runtime.VMCaller, fn *runtime.Function) (runtime.Value, bool, error) {
	if fn == nil || fn.Decl == nil || fn.Decl.Body != nil || fn.Decl.Doc == nil {
		return nil, false, nil
	}
	e.mu.Lock()
	if e.links == nil {
		e.links = map[*runtime.Function]linkEntry{}
	}
	if ent, found := e.links[fn]; found {
		e.mu.Unlock()
		return ent.v, ent.ok, ent.err
	}
	e.mu.Unlock()

	var spec string
	for _, c := range fn.Decl.Doc.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if !strings.HasPrefix(line, "go:linkname") {
			continue
		}
		if f := strings.Fields(line); len(f) == 3 {
			spec = f[2]
		}
	}
	var v runtime.Value
	var lerr error
	ok := false
	if i := strings.LastIndex(spec, "."); i > 0 && strings.Contains(spec[:i], "/") {
		p, err := e.Package(context.Background(), spec[:i])
		if err != nil {
			lerr = err
		} else {
			v, lerr = p.MemberV(spec[i+1:], e.materialize, func(f *runtime.Function) error {
				_, rerr := vc.Call(f, nil)
				return rerr
			})
			ok = lerr == nil && v != nil
		}
	}
	e.mu.Lock()
	e.links[fn] = linkEntry{v: v, ok: ok, err: lerr}
	e.mu.Unlock()
	return v, ok, lerr
}

// NewSession returns an engine with the same configuration (resolver,
// options, bindings and special forms) but an empty package cache, so
// repeated runs get isolated package state. Bindings are copied: a later
// [Engine.Bind] on e does not reach the session. Special forms are
// shared: a later [Engine.RegisterSpecial] on either engine affects both.
func (e *Engine) NewSession() *Engine {
	s := &Engine{
		resolver:     e.resolver,
		cfg:          e.cfg,
		fset:         token.NewFileSet(),
		initMode:     e.initMode,
		specials:     e.specials,
		hostPolicy:   e.hostPolicy,
		pkgModes:     e.pkgModes,
		out:          e.out,
		cwd:          e.cwd,
		args:         e.args,
		pkgs:         map[string]*runtime.Package{},
		byDir:        map[string]*runtime.Package{},
		files:        map[string]*runtime.Package{},
		binds:        map[string]*runtime.Package{},
		srcs:         map[string]*runtime.Package{},
		pkgsLenient:  map[string]*runtime.Package{},
		byDirLenient: map[string]*runtime.Package{},
		filesLenient: map[string]*runtime.Package{},
	}
	s.builtins = builtins(s)
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

// Result holds a script value that [Result.As] decodes into a Go value.
type Result struct {
	V runtime.Value
}

// RunResult differs from [Engine.Run] only in wrapping the value in a
// [Result], for decoding into a Go value with [Result.As].
func (e *Engine) RunResult(ctx context.Context, ref, fnName string, args ...runtime.Value) (*Result, error) {
	v, err := e.Run(ctx, ref, fnName, args...)
	if err != nil {
		return nil, err
	}
	return &Result{V: v}, nil
}

// As stores the result into dst, which must be a pointer. Scalars assign
// when the types agree; script structs, slices and maps are decoded by
// field name and index.
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

// RegisterSpecial makes id a special form: a call `pkgAlias.Name(args...)`
// that resolves to id does not evaluate its arguments; h runs instead, at
// run time, with the call's syntax. Register before running scripts that
// call it.
func (e *Engine) RegisterSpecial(id runtime.SymbolID, h runtime.SpecialFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.specials[id] = h
}

// WithResolver replaces the resolver in place and returns e.
func (e *Engine) WithResolver(r resolve.Resolver) *Engine {
	e.resolver = r
	return e
}

// Package loads ref, an import path, directory or .go file, without
// initializing it, and caches it on e. ref resolves as a script import
// would: a host-bound path yields the bound package, while
// [Engine.SourceOf] loads its source. Pass the package to [Engine.Call]
// to run a member.
func (e *Engine) Package(ctx context.Context, ref string) (*runtime.Package, error) {
	if resolve.LooksLikeDir(ref) || isGoFile(ref) {
		return e.loadDir(ctx, ref)
	}
	return e.loadPath(ctx, ref)
}

// isGoFile reports whether ref names a .go file: like the go command, a
// ref ending in ".go" is a file, never an import path, so a bare
// `minigo run main.go` works and a missing one reports a missing file.
func isGoFile(ref string) bool {
	return strings.HasSuffix(ref, ".go")
}

// Call calls the member name of pkg, initializing the package first if
// needed. Goroutines started by package init or by the call are torn down
// when Call returns.
//
// Arguments are copied into script values — an int becomes a script int,
// a []string a *runtime.Slice, an unnamed map a *runtime.Map — so script
// writes do not reach the host value. A named map or struct stays boxed so
// its methods remain callable.
func (e *Engine) Call(ctx context.Context, pkg *runtime.Package, name string, args ...runtime.Value) (runtime.Value, error) {
	// one Call = one process = one root VM: concurrent Calls never share
	// interpreter state, and lazy inits triggered inside the run join this
	// run's process scope through the per-call runner below.
	vmm := e.newVM()
	vmm.EnsureProc()
	defer vmm.ReleaseProc()
	member, err := pkg.MemberV(name, e.materialize, func(fn *runtime.Function) error {
		_, err := vmm.Call(fn, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	// host arguments cross as snapshots: a []string arrives as a
	// *runtime.Slice instead of trapping "cannot use []string as
	// []string" in the callee's param coerce.
	sargs := make([]runtime.Value, len(args))
	for i, a := range args {
		sargs[i] = vm.SnapshotOf(a)
	}
	// the host boundary reports the payload: a Named int64 leaves the
	// interpreter as a plain int64, like fmt's %v inside the script.
	r, err := vmm.Call(member, sargs)
	return runtime.Unwrap(r), err
}

// Run loads ref with [Engine.Package] and calls fnName(args...) with
// [Engine.Call]. An empty fnName calls main. The first package Run
// loads becomes the main package that runtime/debug.ReadBuildInfo reports.
func (e *Engine) Run(ctx context.Context, ref, fnName string, args ...runtime.Value) (runtime.Value, error) {
	if fnName == "" {
		fnName = "main"
	}
	pkg, err := e.Package(ctx, ref)
	if err != nil {
		return nil, err
	}
	if e.mainPkg == nil {
		e.mainPkg = pkg
	}
	return e.Call(ctx, pkg, fnName, args...)
}

// modinfo answers runtime/debug's linker-provided build info the way a
// `go run` binary carries it: the main package path and its module at
// version (devel), framed by the 16-byte sentinels ReadBuildInfo strips.
// Dependencies are not listed. Outside Run (no main package) it reports
// "" — ReadBuildInfo then answers ok=false, like a test binary.
func (e *Engine) modinfo() string {
	if e.mainPkg == nil {
		return ""
	}
	var b strings.Builder
	if strings.HasPrefix(e.mainPkg.Path, "<file>") {
		// `go run file.go` builds command-line-arguments with no main
		// module
		b.WriteString("path\tcommand-line-arguments\n")
	} else {
		b.WriteString("path\t" + e.mainPkg.Path + "\n")
	}
	if mod := resolve.ModulePath(e.mainPkg.Dir); mod != "" && !strings.HasPrefix(e.mainPkg.Path, "<file>") {
		// a main package outside the cwd's module (`go run dep/cmd/x`)
		// is stamped with the version the cwd module requires
		version := "(devel)"
		if resolve.ModulePath(e.cwd) != mod {
			if v, ok := resolve.RequiredVersion(e.cwd, mod); ok {
				version = v
			}
		}
		b.WriteString("mod\t" + mod + "\t" + version + "\t\n")
	}
	const sentinel = "0123456789abcdef" // any 16 bytes: only the length matters
	return sentinel + b.String() + sentinel
}

// Bind registers a host package: scripts importing importPath get symbols
// as its members, without any source. Use [WrapFunc] and [ValueOf] to build
// symbols from Go values. [WithHostPolicy] can hide individual symbols.
func (e *Engine) Bind(importPath string, symbols map[string]runtime.Value) {
	p := &runtime.Package{
		Path:     importPath,
		Name:     lastElem(importPath),
		Globals:  runtime.NewEnv(),
		Specials: e.specials,
		// bound paths without a dot in the first element stand in for the
		// GOROOT package of that name ("strings", "fmt", "unsafe").
		Standard: !strings.Contains(strings.SplitN(importPath, "/", 2)[0], "."),
	}
	p.SetState(runtime.Ready)
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
		if td, ok := v.(*runtime.TypeDef); ok && td.Pkg == nil {
			td.Pkg = p
		}
		p.Globals.Set(k, v)
	}
	e.mu.Lock()
	e.pkgs[importPath] = p
	e.binds[importPath] = p
	e.mu.Unlock()
}

// BoundPackages returns the sorted import paths of every host-bound
// package: std intrinsics and packages registered with [Engine.Bind].
func (e *Engine) BoundPackages() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	paths := make([]string, 0, len(e.binds))
	for path := range e.binds {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// BoundOf returns the host-bound package registered for path, or nil when
// path is not bound. Unlike [Engine.Package] and [Engine.SourceOf] it
// never loads source, and it ignores [PackageMode]: a [ModeDeny] path
// still reports its registration.
func (e *Engine) BoundOf(path string) *runtime.Package {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.binds[path]
}

// BoundSymbols returns the sorted symbol names a bound package exposes to
// scripts, after [WithHostPolicy] filtering; ok is false when importPath
// is not bound.
func (e *Engine) BoundSymbols(importPath string) (names []string, ok bool) {
	e.mu.Lock()
	p, ok := e.binds[importPath]
	e.mu.Unlock()
	if !ok {
		return nil, false
	}
	for _, name := range p.Globals.Names() {
		if !strings.HasPrefix(name, "__") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, true
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
	return e.loadPathMode(ctx, "", path, false)
}

// loadPathLenient is inspect.PackageOf's view: like loadDirLenient it
// must read source that would not compile (redeclared decls stay
// visible), so misses build into the lenient caches — a canonical
// package already in e.pkgs still answers, as the object scripts see.
func (e *Engine) loadPathLenient(ctx context.Context, path string) (*runtime.Package, error) {
	return e.loadPathMode(ctx, "", path, true)
}

// loadPathFrom resolves path as imported from the package in fromDir —
// resolver vendoring (e.g. GOROOT's src/vendor) keys off the importer's
// location, so interpreted packages pass their own Dir through.
func (e *Engine) loadPathFrom(ctx context.Context, fromDir, path string) (*runtime.Package, error) {
	return e.loadPathMode(ctx, fromDir, path, false)
}

func (e *Engine) loadPathMode(ctx context.Context, fromDir, path string, lenient bool) (*runtime.Package, error) {
	mode := e.pkgModes[path]
	if mode == ModeDeny {
		return nil, fmt.Errorf("minigo: import of %q denied by package policy", path)
	}
	e.mu.Lock()
	if p, ok := e.pkgs[path]; ok && (mode != ModeSource || p.Index != nil) {
		// a ModeSource path answers only from a source-built package —
		// a bound shadow (unindexed) falls through to the build.
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	if e.resolver == nil {
		return nil, fmt.Errorf("minigo: no resolver configured; cannot import %q", path)
	}
	meta, err := e.resolver.Locate(ctx, fromDir, path)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", path, err)
	}
	return e.buildPackage(meta, lenient)
}

// packageNameFrom reports the name in the package clause of path as
// imported from fromDir, without building the package: a package already
// built from source answers from its Name; anything else is located,
// which reads only the files' headers. A bound package with no source to
// locate is an error, so callers fall back to the path-tail guess.
func (e *Engine) packageNameFrom(ctx context.Context, fromDir, path string) (string, error) {
	if e.pkgModes[path] == ModeDeny {
		return "", fmt.Errorf("minigo: import of %q denied by package policy", path)
	}
	e.mu.Lock()
	p, ok := e.pkgs[path]
	e.mu.Unlock()
	if ok && p.Index != nil && p.Name != "" {
		return p.Name, nil
	}
	if e.resolver == nil {
		return "", fmt.Errorf("minigo: no resolver configured; cannot locate %q", path)
	}
	meta, err := e.resolver.Locate(ctx, fromDir, path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	return meta.Name, nil
}

func (e *Engine) loadDir(ctx context.Context, dir string) (*runtime.Package, error) {
	return e.loadDirMode(ctx, dir, false)
}

// loadDirLenient is inspect.DirOf's view: it must enumerate decls that
// would not compile, like duplicate methods, so its package indexes
// without the redeclaration check.
func (e *Engine) loadDirLenient(ctx context.Context, dir string) (*runtime.Package, error) {
	return e.loadDirMode(ctx, dir, true)
}

func (e *Engine) loadDirMode(ctx context.Context, dir string, lenient bool) (*runtime.Package, error) {
	e.mu.Lock()
	byDir := e.byDir
	if lenient {
		byDir = e.byDirLenient
	}
	if p, ok := byDir[dir]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	meta, err := e.resolver.LocateDir(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("resolve dir %q: %w", dir, err)
	}
	return e.buildPackage(meta, lenient)
}

// SourceOf differs from [Engine.Package] only for a host-bound path: it
// loads the Go source instead of returning the bound package. Script
// imports keep getting the bound package. It is meant for reading code,
// so a [ModeDeny] package mode, which stops imports, does not stop
// SourceOf from loading a path's source.
func (e *Engine) SourceOf(ctx context.Context, path string) (*runtime.Package, error) {
	// Bound packages answer lookups through e.pkgs (with a nil index), so
	// the source package is built into a private cache — never e.pkgs or
	// e.byDir — to keep the bound package answering real imports. A denied
	// path takes the same private build: deny gates imports, not reading,
	// and the private cache keeps the denied package out of the stores
	// [Engine.Package] and DirOf answer from.
	if _, bound := e.binds[path]; !bound && e.pkgModes[path] != ModeDeny {
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
	e.buildMu.Lock()
	defer e.buildMu.Unlock()
	p := e.newPackage(meta.ImportPath, meta.Name, meta.Dir)
	p.Standard = meta.Standard
	files, failed, err := parsePackageFiles(e.fset, meta.GoFiles)
	if err != nil {
		p.SetState(runtime.Failed)
		return nil, fmt.Errorf("parse %s: %w", failed, err)
	}
	for _, sf := range files {
		sf.LangMod = meta.Lang
	}
	if err := e.indexFiles(p, files, true); err != nil {
		p.SetState(runtime.Failed)
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
// Initialize stays lazy (Bootstrap hook). lenient relaxes the index's
// redeclaration check for inspect callers that must see duplicates.
func (e *Engine) buildPackage(meta *resolve.PackageMeta, lenient bool) (*runtime.Package, error) {
	// serialize whole builds so two goroutines cold-loading the same
	// package converge on one object (the indexed gate closes in
	// indexFiles for concurrent member access)
	e.buildMu.Lock()
	defer e.buildMu.Unlock()

	// lenient builds live in their own caches: publishing a lenient
	// package into e.pkgs would hide a redeclare error from a later
	// strict load, and a strict package under e.byDir would make
	// inspect.DirOf's lenient view answer the wrong object.
	pkgs, byDir := e.pkgs, e.byDir
	if lenient {
		pkgs, byDir = e.pkgsLenient, e.byDirLenient
	}

	e.mu.Lock()
	if p, ok := pkgs[meta.ImportPath]; ok && (e.pkgModes[meta.ImportPath] != ModeSource || p.Index != nil) {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	if e.pkgModes[meta.ImportPath] == ModeDeny {
		return nil, fmt.Errorf("minigo: import of %q denied by package policy", meta.ImportPath)
	}

	p := e.newPackage(meta.ImportPath, meta.Name, meta.Dir)
	p.Standard = meta.Standard
	// publish before parsing to make import cycles convergent
	e.mu.Lock()
	pkgs[meta.ImportPath] = p
	if meta.Dir != "" && !strings.HasPrefix(meta.ImportPath, resolve.FileImportPrefix) {
		byDir[meta.Dir] = p
	}
	e.mu.Unlock()

	files, failed, err := parsePackageFiles(e.fset, meta.GoFiles)
	if err != nil {
		p.SetState(runtime.Failed)
		p.FinishIndexing()
		return nil, fmt.Errorf("parse %s: %w", failed, err)
	}
	for _, sf := range files {
		sf.LangMod = meta.Lang
	}
	if err := e.indexFiles(p, files, lenient); err != nil {
		p.SetState(runtime.Failed)
		return nil, err
	}
	return p, nil
}

// newPackage builds a Package shell wired to this engine (Parsed state).
func (e *Engine) newPackage(path, name, dir string) *runtime.Package {
	p := &runtime.Package{
		Path:     path,
		Name:     name,
		Dir:      dir,
		Fset:     e.fset,
		LazyInit: e.initMode == LazyInit,
		Globals:  runtime.NewEnv(),
		HostVars: hostVars[path],
		Scopes:   map[*syntax.File]map[string]*runtime.ImportRef{},
		Imports:  map[*syntax.File][]*runtime.ImportRef{},
		Specials: e.specials,
		// host-side init runs on a fresh VM on the caller's goroutine —
		// sharing a VM between goroutines is not safe
		RunInit: func(fn *runtime.Function) error {
			_, err := e.newVM().Call(fn, nil)
			return err
		},
	}
	p.SetState(runtime.Parsed)
	p.MarkIndexed()
	return p
}

// indexFiles finishes a package over already-selected files: declaration
// index, file-by-name map, per-file import scopes, and the lazy initializer
// hook. Directory discovery and build-constraint filtering happen upstream.
func (e *Engine) indexFiles(p *runtime.Package, files []*syntax.File, lenient bool) error {
	// the indexed gate closes however this returns: a failed package is
	// finished too (its State reports Failed) rather than hanging waiters
	defer p.FinishIndexing()
	p.Files = files
	p.FileByName = map[string]*syntax.File{}
	for _, sf := range files {
		p.FileByName[sf.Name] = sf
	}

	// language-version gate: files under a versioned module may only use
	// features their effective -lang reaches (see syntax.CheckLang)
	declared := syntax.DeclaredKinds(files)
	for _, sf := range files {
		if err := syntax.CheckLang(e.fset, sf, declared); err != nil {
			return err
		}
	}

	buildIx := index.Build
	if lenient {
		buildIx = index.BuildLenient
	}
	ix, err := buildIx(files)
	if err != nil {
		var re *index.RedeclError
		if errors.As(err, &re) {
			if re.Method {
				// gc: `method T.M already declared at <file>:<line>:<col>`
				return fmt.Errorf("%s: method %s already declared at %s", p.Fset.Position(re.Pos), re.Name, p.Fset.Position(re.PrevPos))
			}
			return fmt.Errorf("%s: %s\n\t%s: other declaration of %s", p.Fset.Position(re.Pos), re, p.Fset.Position(re.PrevPos), re.Name)
		}
		return fmt.Errorf("index %s: %w", p.Path, err)
	}
	p.Index = ix
	p.SetState(runtime.Indexed)

	// file-scope import refs
	for _, sf := range files {
		m := map[string]*runtime.ImportRef{}
		for _, imp := range sf.Imports {
			if imp.Path == "C" {
				// the cgo pseudo-package: the interpreter cannot link
				// native code, so fail the load up front instead of
				// trapping later on an unresolvable `C.foo` reference.
				// Directory walks never reach here — the resolver drops
				// cgo files from GoFiles like _test.go files; this fires
				// only for a file named explicitly (LoadFile, :load, run).
				return fmt.Errorf("%s: cgo is not supported: import %q", p.Fset.Position(imp.Pos), imp.Path)
			}
			ref := &runtime.ImportRef{
				Path:  imp.Path,
				Alias: imp.Alias,
				Load:  func(path string) (*runtime.Package, error) { return e.loadPathFrom(context.Background(), p.Dir, path) },
				LocateName: func(path string) (string, error) {
					return e.packageNameFrom(context.Background(), p.Dir, path)
				},
			}
			p.Imports[sf] = append(p.Imports[sf], ref)
			if imp.Alias != "_" && imp.Alias != "." {
				m[imp.LocalName()] = ref
			}
		}
		p.Scopes[sf] = m
	}

	p.Bootstrap = e.bootstrap
	p.ConstBootstrap = e.bootstrapConsts
	return nil
}

// LoadFile loads a single file as a package, ignoring its siblings and
// build constraints, so a file guarded by `//go:build codegen` or a script
// outside any package can be an entry point; [Engine.LoadDirScope] also
// loads the siblings. Its imports resolve normally, but nothing can
// import the file's package. [WithAllowedRoots] applies to the file's
// directory.
func (e *Engine) LoadFile(ctx context.Context, filename string) (*runtime.Package, error) {
	return e.loadFileMode(ctx, filename, false)
}

// loadFileLenient is inspect.FileOf's view: like DirOf it must show a
// file whose decls would not compile, so it builds leniently into its
// own cache — a strict `minigo run file.go` still rejects redeclares.
func (e *Engine) loadFileLenient(ctx context.Context, filename string) (*runtime.Package, error) {
	return e.loadFileMode(ctx, filename, true)
}

func (e *Engine) loadFileMode(ctx context.Context, filename string, lenient bool) (*runtime.Package, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	files := e.files
	if lenient {
		files = e.filesLenient
	}
	if p, ok := files[abs]; ok {
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()

	if err := e.cfg.CheckDir(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	e.buildMu.Lock()
	defer e.buildMu.Unlock()
	sf, err := syntax.ParseFile(e.fset, abs, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	sf.LangMod = resolve.ModuleLang(filepath.Dir(abs))

	p := e.newPackage("<file>"+filepath.ToSlash(abs), sf.AST.Name.Name, filepath.Dir(abs))
	if err := e.indexFiles(p, []*syntax.File{sf}, lenient); err != nil {
		return nil, err
	}

	// Publish only once the package is fully indexed: file packages cannot
	// be imported back, so nothing needs the early visibility that
	// buildPackage's import cycles do.
	e.mu.Lock()
	if q, ok := files[abs]; ok {
		e.mu.Unlock()
		return q, nil
	}
	files[abs] = p
	e.mu.Unlock()
	return p, nil
}

// LoadDirScope differs from [Engine.LoadFile] in also loading the file's
// non-test sibling .go files of the same package, ignoring build
// constraints, so a file guarded by `//go:build task` still sees the rest
// of its package. Unlike LoadFile and [Engine.Package] the result is not
// cached; each call reloads the directory.
func (e *Engine) LoadDirScope(ctx context.Context, filename string) (*runtime.Package, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := e.cfg.CheckDir(dir); err != nil {
		return nil, err
	}
	e.buildMu.Lock()
	defer e.buildMu.Unlock()

	// the named file's package clause defines the scope: siblings keep
	// only files declaring the same package name. Filename invisibility
	// (_GOOS/_GOARCH suffixes, _test.go, _/.-prefixed) applies like a
	// real package load; //go:build constraints do not.
	gofiles, err := resolve.FileScopeFiles(dir, abs, e.cfg)
	if err != nil {
		return nil, fmt.Errorf("reading package dir %s: %w", dir, err)
	}
	pkgName, err := packageName(e.fset, abs)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	files, failed, err := parsePackageFiles(e.fset, gofiles)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", failed, err)
	}
	for _, sf := range files {
		sf.LangMod = resolve.ModuleLang(dir)
	}
	p := e.newPackage("<dir>"+filepath.ToSlash(dir), pkgName, dir)
	if err := e.indexFiles(p, files, false); err != nil {
		return nil, err
	}
	return p, nil
}

// packageName reads just a file's package clause.
func packageName(fset *token.FileSet, abs string) (string, error) {
	f, err := parser.ParseFile(fset, abs, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return f.Name.Name, nil
}

// RunFile is [Engine.LoadFile] followed by [Engine.Call]. An empty fnName
// calls main. Unlike [Engine.Run], which also accepts a .go file, it does
// not set the main package that runtime/debug.ReadBuildInfo reports.
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

// bootstrap runs the synthetic __init__ function of a package via run —
// the runner supplied by whoever triggered initialization, so init code
// executes on the triggering VM (a spawned goroutine's member access
// runs init on that goroutine's VM, joining its process).
func (e *Engine) bootstrap(p *runtime.Package, run func(*runtime.Function) error) error {
	for _, sf := range p.Files {
		for _, ref := range p.Imports[sf] {
			if ref.Alias != "_" {
				continue
			}
			imported, err := ref.Materialize()
			if err != nil {
				return fmt.Errorf("initialize blank import %q: %w", ref.Path, err)
			}
			if err := imported.EnsureReadyRun(run); err != nil {
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
	return run(fn)
}

// bootstrapConsts runs the const-only initializer of a package via run —
// constants bind without the var initializers or init() funcs (a constant
// needs no init). A package whose const specs reference vars or funcs has
// no const-only path: ConstInitFunc reports nil and the caller falls back
// to the full initializer.
func (e *Engine) bootstrapConsts(p *runtime.Package, run func(*runtime.Function) error) error {
	ch, err := compile.ConstInitFunc(p)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("package %s: const specs need the full initializer", p.Name)
	}
	bindCompiles(p, ch)
	fn := &runtime.Function{Pkg: p, Name: p.Name + ".__consts__", Chunk: ch}
	return run(fn)
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

// EvalExpr evaluates expr in pkg's scope, resolving names as a function
// body in file would: file's imports, package globals and builtins.
func (e *Engine) EvalExpr(ctx context.Context, pkg *runtime.Package, file *syntax.File, expr ast.Expr) (runtime.Value, error) {
	// The fragment travels as AST inside a chunk (OP_EVAL_AST) and compiles
	// via compile.Expr on first execution.
	vmm := e.newVM()
	vmm.EnsureProc()
	defer vmm.ReleaseProc()
	if err := pkg.EnsureReadyRun(func(fn *runtime.Function) error {
		_, err := vmm.Call(fn, nil)
		return err
	}); err != nil {
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
	return vmm.Call(&runtime.Function{Pkg: pkg, File: file, Name: "<eval>", Chunk: ch}, nil)
}

// materialize builds the runtime value for one decl on first access.
// The per-package dedup cache keeps one TypeDef/Function identity per
// decl when two goroutines resolve the same member concurrently.
func (e *Engine) materialize(pkg *runtime.Package, d *index.Decl) (runtime.Value, error) {
	return pkg.MatCache(d, e.materializeOne)
}

func (e *Engine) materializeOne(pkg *runtime.Package, d *index.Decl) (runtime.Value, error) {
	switch d.Kind {
	case index.FuncDecl:
		// a bodiless declaration (//go:noescape assembly stub) with a
		// registered host impl materializes as the impl itself — the
		// zero-return shim would silently drop every result.
		if d.Func.Body == nil {
			if v, ok := asmImpl(pkg, d.Name); ok {
				return v, nil
			}
			if pkg.Path == "runtime/debug" && d.Name == "modinfo" {
				return &runtime.BuiltinFunc{Name: "runtime/debug.modinfo", Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
					return e.modinfo(), nil
				}}, nil
			}
		}
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
		td.FTags = runtime.StructFieldTags(t)
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
