package minigo

import (
	stdlibhtml "github.com/podhmo/minigo/internal/stdlib/html"
	stdlibutf8 "github.com/podhmo/minigo/internal/stdlib/utf8"
)

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
	e.pending.Register("unicode/utf8", func() { stdlibutf8.Install(e.bindBound) })
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
	e.pending.Register("html", func() { stdlibhtml.Install(e.bindBound) })
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
