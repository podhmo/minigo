package minigo

import (
	stdlibbytes "github.com/podhmo/minigo/internal/stdlib/bytes"
	stdlibcontext "github.com/podhmo/minigo/internal/stdlib/context"
	stdliberrors "github.com/podhmo/minigo/internal/stdlib/errors"
	stdlibhtml "github.com/podhmo/minigo/internal/stdlib/html"
	stdlibhtmltemplate "github.com/podhmo/minigo/internal/stdlib/htmltemplate"
	stdlibmath "github.com/podhmo/minigo/internal/stdlib/math"
	stdlibnetip "github.com/podhmo/minigo/internal/stdlib/netip"
	stdlibpath "github.com/podhmo/minigo/internal/stdlib/path"
	stdlibregexp "github.com/podhmo/minigo/internal/stdlib/regexp"
	stdlibsha256 "github.com/podhmo/minigo/internal/stdlib/sha256"
	stdlibstrconv "github.com/podhmo/minigo/internal/stdlib/strconv"
	stdlibsync "github.com/podhmo/minigo/internal/stdlib/sync"
	stdlibsyscall "github.com/podhmo/minigo/internal/stdlib/syscall"
	stdlibtexttemplate "github.com/podhmo/minigo/internal/stdlib/texttemplate"
	stdlibtime "github.com/podhmo/minigo/internal/stdlib/time"
	stdlibunicode "github.com/podhmo/minigo/internal/stdlib/unicode"
	stdlibutf8 "github.com/podhmo/minigo/internal/stdlib/utf8"
)

// registerStdlib registers the per-path installers without running
// them: bound packages build on first use through the install entry.
func (e *Engine) registerStdlib() {
	h := &hostHelpers{env: bindEnv{e}}
	e.pending.Register("fmt", func() { e.installFmt(h) })
	e.pending.Register("errors", func() { stdliberrors.Install(e.bindBound, h, e.ifaceReqSet) })
	e.pending.Register("strings", func() { e.installStrings(h) })
	e.pending.Register("strconv", func() { stdlibstrconv.Install(e.bindBound) })
	e.pending.Register("bytes", func() { stdlibbytes.Install(e.bindBound) })
	e.pending.Register("unicode", func() { stdlibunicode.Install(e.bindBound) })
	e.pending.Register("unicode/utf8", func() { stdlibutf8.Install(e.bindBound) })
	e.pending.Register("math", func() { stdlibmath.Install(e.bindBound, h) })
	e.pending.Register("regexp", func() { stdlibregexp.Install(e.bindBound, h) })
	e.pending.Register("encoding/base64", func() { e.installEncodingBase64(h) })
	e.pending.Register("encoding/hex", func() { e.installEncodingHex(h) })
	e.pending.Register("encoding/json", func() { e.installEncodingJson(h) })
	e.pending.Register("internal/godebug", func() { e.installInternalGodebug(h) })
	e.pending.Register("internal/testenv", func() { e.installInternalTestenv(h) })
	e.pending.Register("syscall", func() { stdlibsyscall.Install(e.bindBound, h) })
	e.pending.Register("internal/bytealg", func() { e.installInternalBytealg(h) })
	e.pending.Register("internal/race", func() { e.installInternalRace(h) })
	e.pending.Register("internal/stringslite", func() { e.installInternalStringslite(h) })
	e.pending.Register("html", func() { stdlibhtml.Install(e.bindBound) })
	e.pending.Register("path", func() { stdlibpath.Install(e.bindBound) })
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
	e.pending.Register("time", func() { stdlibtime.Install(e.bindBound, h) })
	e.pending.Register("sync", func() { stdlibsync.Install(e.bindBound, h) })
	e.pending.Register("sync/atomic", func() { e.installSyncAtomic(h) })
	e.pending.Register("context", func() { stdlibcontext.Install(e.bindBound, h) })
	e.pending.Register("net", func() { e.installNet(h) })
	e.pending.Register("net/netip", func() { stdlibnetip.Install(e.bindBound) })
	e.pending.Register("io", func() { e.installIo(h) })
	e.pending.Register("crypto/rand", func() { e.installCryptoRand(h) })
	e.pending.Register("crypto/sha256", func() { stdlibsha256.Install(e.bindBound) })
	e.pending.Register("encoding/csv", func() { e.installEncodingCsv(h) })
	e.pending.Register("bufio", func() { e.installBufio(h) })
	e.pending.Register("text/template", func() { stdlibtexttemplate.Install(e.bindBound, h, e.templateFuncMap()) })
	e.pending.Register("html/template", func() { stdlibhtmltemplate.Install(e.bindBound, h, e.templateFuncMap()) })
}
