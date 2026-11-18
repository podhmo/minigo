package minigo

// The lazy-install seam for bound packages: per-path installers
// register into e.pending at engine/session construction (P2-12), and
// the read paths — hostTwin, imports, Bound* queries, the vet check —
// run a path's installer on first use through Install.

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// hostPackageOf is the typedef lookup's install entry: a registered
// path's install is guaranteed before it is returned; an unregistered
// path only answers the packages already present.
func (e *Engine) hostPackageOf(path string) *runtime.Package {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pkgs[path]
}

// installBound installs a registered path's bound package on first use.
// A source-built package already in e.pkgs keeps answering loads: the
// bound table still installs into names, but pkgs is restored.
func (e *Engine) installBound(path string) {
	e.mu.Lock()
	src, occupied := e.pkgs[path]
	if occupied && e.names.Bound(path) {
		occupied = false // pkgs already holds a bound package
	}
	e.mu.Unlock()
	e.pending.Install(path)
	if occupied {
		e.mu.Lock()
		e.pkgs[path] = src
		e.mu.Unlock()
	}
}

// bindBound is [Engine.Bind] for the lazy installers: a path already
// bound — an explicit Bind, or a sibling alias installed on first use
// — is not clobbered by the default table.
func (e *Engine) bindBound(importPath string, symbols map[string]runtime.Value) {
	e.mu.Lock()
	bound := e.names.Bound(importPath)
	e.mu.Unlock()
	if bound {
		return
	}
	e.Bind(importPath, symbols)
}

// registered reports whether path is bound — installed already or
// registered for lazy install.
func (e *Engine) registered(path string) bool {
	return e.names.Bound(path) || e.pending.Registered(path)
}

// registerStdlib registers the per-path installers without running
// them: bound packages build on first use through the install entry.
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
	e.pending.Register("minigo.dev/host", func() { e.installHost(h) })
	e.pending.Register("github.com/podhmo/minigo/host", func() { e.installHost(h) })
	e.pending.Register("minigo.dev/inspect", func() { e.installInspect() })
	e.pending.Register("github.com/podhmo/minigo/inspect", func() { e.installInspect() })
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

// hostSymbol converts a plain Go value in a [Engine.Bind] symbol map to its
// script form; runtime values pass through unchanged.
func hostSymbol(name string, v runtime.Value) runtime.Value {
	switch v.(type) {
	case *runtime.BuiltinFunc, *runtime.TypeDef, *runtime.GoValue, int64, float64, string, bool:
		return v // the common cases, without reflect
	case nil:
		return runtime.NIL
	}
	t := reflect.TypeOf(v)
	base := t
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if base.PkgPath() == runtimePkgPath {
		return v
	}
	if t.Kind() == reflect.Func {
		return WrapFunc(name, v)
	}
	if t.Name() != "" && t.PkgPath() != "" && (t.Kind() <= reflect.Complex128 || t.Kind() == reflect.String) {
		return v // named host scalars (time.Duration) are script values as is
	}
	return ValueOf(v)
}
