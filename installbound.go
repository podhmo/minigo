package minigo

// The lazy-install seam for bound (host/registered) packages: the
// per-path installers register into e.pending at engine/session
// construction instead of running eagerly (P2-12), and the read
// paths — hostTwin's typedef lookup, imports, BoundOf/BoundSymbols and
// the vet bound-symbol check — run a path's installer on first use
// through Install.

import (
	"go/ast"
	"slices"

	"github.com/podhmo/minigo/runtime"
)

// hostPackageOf is the typedef lookup's install entry: for a
// registered (bound) path it guarantees the package's install before
// returning it, while an unregistered path only answers the packages
// already present.
func (e *Engine) hostPackageOf(path string) *runtime.Package {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pkgs[path]
}

// installBound installs a registered path's bound package on first
// use (concurrent first uses wait on each other); unregistered and
// already-installed paths are a no-op.
func (e *Engine) installBound(path string) {
	e.pending.Install(path)
}

// registered reports whether path is bound — installed already or
// registered for lazy install.
func (e *Engine) registered(path string) bool {
	return e.names.Bound(path) || e.pending.Registered(path)
}

// BoundPackages returns the sorted import paths of every host-bound
// package: std intrinsics and packages registered with [Engine.Bind],
// installed or still pending.
func (e *Engine) BoundPackages() []string {
	e.mu.Lock()
	paths := e.names.Paths()
	e.mu.Unlock()
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
	}
	for _, p := range e.pending.Paths() {
		if !seen[p] {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}

// BoundOf returns the host-bound package registered for path, or nil when
// path is not bound. Unlike [Engine.Package] and [Engine.SourceOf] it
// never loads source, and it ignores [PackageMode]: a [ModeDeny] path
// still reports its registration.
func (e *Engine) BoundOf(path string) *runtime.Package {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.names.Lookup(path)
}

// BoundSymbols returns the sorted symbol names a bound package exposes to
// scripts, after [WithHostPolicy] filtering; ok is false when importPath
// is not bound.
func (e *Engine) BoundSymbols(importPath string) (names []string, ok bool) {
	e.installBound(importPath)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.names.Symbols(importPath)
}

// registeredVetSymbol reports whether path.Name is already intercepted:
// a special form the compiler emits OpSpecialCall for, or a symbol in a
// host-bound package.
func (e *Engine) registeredVetSymbol(path, name string) bool {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.specials[runtime.SymbolID{PackagePath: path, Name: name}]; ok {
		return true
	}
	if b := e.names.Lookup(path); b != nil {
		if _, ok := b.Globals.Get(name); ok {
			return true
		}
	}
	return false
}

// registerStdlib registers the intrinsic per-path installers without
// running them: bound packages build on first use (P2-12). Bound
// packages still win over source resolution, since every lookup routes
// through the install entry first.
func (e *Engine) registerStdlib() {
	h := &hostHelpers{env: bindEnv{e}}
	e.pending.Register("fmt", func() { e.installFmt(h) })
	e.pending.Register("errors", func() { e.installErrors(h) })
	e.pending.Register("strings", func() { e.installStrings(h) })
	e.pending.Register("strconv", func() { e.installStrconv(h) })
	e.pending.Register("bytes", func() { e.installBytes(h) })
	e.pending.Register("unicode", func() { e.installUnicode(h) })
	e.pending.Register("unicode/utf8", func() { e.installUnicodeUtf8(h) })
	e.pending.Register("math", func() { e.installMath(h) })
	e.pending.Register("regexp", func() { e.installRegexp(h) })
	e.pending.Register("encoding/base64", func() { e.installEncodingBase64(h) })
	e.pending.Register("encoding/hex", func() { e.installEncodingHex(h) })
	e.pending.Register("encoding/json", func() { e.installEncodingJson(h) })
	e.pending.Register("internal/godebug", func() { e.installInternalGodebug(h) })
	e.pending.Register("internal/testenv", func() { e.installInternalTestenv(h) })
	e.pending.Register("syscall", func() { e.installSyscall(h) })
	e.pending.Register("internal/bytealg", func() { e.installInternalBytealg(h) })
	e.pending.Register("internal/race", func() { e.installInternalRace(h) })
	e.pending.Register("internal/stringslite", func() { e.installInternalStringslite(h) })
	e.pending.Register("html", func() { e.installHtml(h) })
	e.pending.Register("path", func() { e.installPath(h) })
	e.pending.Register("reflect", func() { e.installReflect() })
	e.pending.Register("sort", func() { e.installSort(h) })
	e.pending.Register("slices", func() { e.installSlices(h) })
	e.pending.Register("maps", func() { e.installMaps(h) })
	e.pending.Register("os", func() { e.installOs(h) })
	e.pending.Register("path/filepath", func() { e.installPathFilepath(h) })
	e.pending.Register("os/exec", func() { e.installOsExec(h) })
	hostInstall := func() {
		e.installHost(h)
		e.pending.Clear("minigo.dev/host")
		e.pending.Clear("github.com/podhmo/minigo/host")
	}
	e.pending.Register("minigo.dev/host", hostInstall)
	e.pending.Register("github.com/podhmo/minigo/host", hostInstall)
	inspectInstall := func() {
		e.installInspect()
		e.pending.Clear("minigo.dev/inspect")
		e.pending.Clear("github.com/podhmo/minigo/inspect")
	}
	e.pending.Register("minigo.dev/inspect", inspectInstall)
	e.pending.Register("github.com/podhmo/minigo/inspect", inspectInstall)
	e.pending.Register("unsafe", func() { e.installUnsafe(h) })
	e.pending.Register("runtime", func() { e.installRuntime(h) })
	e.pending.Register("time", func() { e.installTime(h) })
	e.pending.Register("sync", func() { e.installSync(h) })
	e.pending.Register("sync/atomic", func() { e.installSyncAtomic(h) })
	e.pending.Register("context", func() { e.installContext(h) })
	e.pending.Register("net", func() { e.installNet(h) })
	e.pending.Register("net/netip", func() { e.installNetNetip(h) })
	e.pending.Register("io", func() { e.installIo(h) })
	e.pending.Register("crypto/rand", func() { e.installCryptoRand(h) })
	e.pending.Register("crypto/sha256", func() { e.installCryptoSha256(h) })
	e.pending.Register("encoding/csv", func() { e.installEncodingCsv(h) })
	e.pending.Register("bufio", func() { e.installBufio(h) })
	e.pending.Register("text/template", func() { e.installTextTemplate(h) })
	e.pending.Register("html/template", func() { e.installHtmlTemplate(h) })
}

// templateFuncMap returns this session's FuncMap typedef — one per
// registry so td.Pkg names the session's own bound package instead of
// leaking the first engine's across all of them (P2-12).
func (e *Engine) templateFuncMap() *runtime.TypeDef {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessionState.templateFuncMap == nil {
		e.sessionState.templateFuncMap = &runtime.TypeDef{Name: "text/template.FuncMap", Kind: runtime.KindMap,
			Anon: &ast.MapType{Key: ast.NewIdent("string"), Value: ast.NewIdent("any")}}
	}
	return e.sessionState.templateFuncMap
}
