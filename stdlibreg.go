//go:generate go run ./internal/cmd/genbind

package minigo

import (
	stdatomic "github.com/podhmo/minigo/internal/stdlib/atomic"
	stdlibbase64 "github.com/podhmo/minigo/internal/stdlib/base64"
	stdlibbufio "github.com/podhmo/minigo/internal/stdlib/bufio"
	stdlibbytealg "github.com/podhmo/minigo/internal/stdlib/bytealg"
	stdlibbytes "github.com/podhmo/minigo/internal/stdlib/bytes"
	stdlibcontext "github.com/podhmo/minigo/internal/stdlib/context"
	stdlibcrand "github.com/podhmo/minigo/internal/stdlib/crand"
	stdlibcsv "github.com/podhmo/minigo/internal/stdlib/csv"
	stdliberrors "github.com/podhmo/minigo/internal/stdlib/errors"
	stdlibfilepath "github.com/podhmo/minigo/internal/stdlib/filepath"
	stdfmt "github.com/podhmo/minigo/internal/stdlib/fmt"
	stdlibgodebug "github.com/podhmo/minigo/internal/stdlib/godebug"
	stdruntime "github.com/podhmo/minigo/internal/stdlib/goruntime"
	stdlibhex "github.com/podhmo/minigo/internal/stdlib/hex"
	stdlibhost "github.com/podhmo/minigo/internal/stdlib/host"
	stdlibhtml "github.com/podhmo/minigo/internal/stdlib/html"
	stdlibhtmltemplate "github.com/podhmo/minigo/internal/stdlib/htmltemplate"
	stdinspect "github.com/podhmo/minigo/internal/stdlib/inspect"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	stdjson "github.com/podhmo/minigo/internal/stdlib/json"
	stdlibmaps "github.com/podhmo/minigo/internal/stdlib/maps"
	stdlibmath "github.com/podhmo/minigo/internal/stdlib/math"
	stdlibnet "github.com/podhmo/minigo/internal/stdlib/net"
	stdlibnetip "github.com/podhmo/minigo/internal/stdlib/netip"
	stdlibos "github.com/podhmo/minigo/internal/stdlib/os"
	stdlibosexec "github.com/podhmo/minigo/internal/stdlib/osexec"
	stdlibpath "github.com/podhmo/minigo/internal/stdlib/path"
	stdlibrace "github.com/podhmo/minigo/internal/stdlib/race"
	stdreflect "github.com/podhmo/minigo/internal/stdlib/reflect"
	stdlibregexp "github.com/podhmo/minigo/internal/stdlib/regexp"
	stdlibsha256 "github.com/podhmo/minigo/internal/stdlib/sha256"
	stdlibslices "github.com/podhmo/minigo/internal/stdlib/slices"
	stdlibsort "github.com/podhmo/minigo/internal/stdlib/sort"
	stdlibstrconv "github.com/podhmo/minigo/internal/stdlib/strconv"
	stdlibstrings "github.com/podhmo/minigo/internal/stdlib/strings"
	stdlibstringslite "github.com/podhmo/minigo/internal/stdlib/stringslite"
	stdlibsync "github.com/podhmo/minigo/internal/stdlib/sync"
	stdlibsyscall "github.com/podhmo/minigo/internal/stdlib/syscall"
	stdlibtestenv "github.com/podhmo/minigo/internal/stdlib/testenv"
	stdlibtexttemplate "github.com/podhmo/minigo/internal/stdlib/texttemplate"
	stdlibtime "github.com/podhmo/minigo/internal/stdlib/time"
	stdlibunicode "github.com/podhmo/minigo/internal/stdlib/unicode"
	stdunsafe "github.com/podhmo/minigo/internal/stdlib/unsafe"
	stdlibutf8 "github.com/podhmo/minigo/internal/stdlib/utf8"
)

// registerStdlib registers the per-path installers without running
// them: bound packages build on first use through the install entry.
func (e *Engine) registerStdlib() {
	h := &hostHelpers{env: bindEnv{e}}
	e.pending.Register("fmt", func() { stdfmt.Install(e.bindBound, h, h.ioAdapters(), h.out, stdfmt.ScriptTypeString) })
	e.pending.Register("errors", func() { stdliberrors.Install(e.bindBound, h, e.ifaceReqSet) })
	e.pending.Register("strings", func() { stdlibstrings.Install(e.bindBound, h, e.typedSeq) })
	e.pending.Register("strconv", func() { stdlibstrconv.Install(e.bindBound) })
	e.pending.Register("bytes", func() { stdlibbytes.Install(e.bindBound) })
	e.pending.Register("unicode", func() { stdlibunicode.Install(e.bindBound) })
	e.pending.Register("unicode/utf8", func() { stdlibutf8.Install(e.bindBound) })
	e.pending.Register("math", func() { stdlibmath.Install(e.bindBound, h) })
	e.pending.Register("regexp", func() { stdlibregexp.Install(e.bindBound, h) })
	e.pending.Register("encoding/base64", func() { stdlibbase64.Install(e.bindBound, h, h.ioAdapters()) })
	e.pending.Register("encoding/hex", func() { stdlibhex.Install(e.bindBound, h, h.ioAdapters()) })
	e.pending.Register("encoding/json", func() { stdjson.Install(e.bindBound, h, h.ioAdapters(), stdfmt.ScriptTypeString) })
	e.pending.Register("internal/godebug", func() { stdlibgodebug.Install(e.bindBound, h) })
	e.pending.Register("internal/testenv", func() { stdlibtestenv.Install(e.bindBound, h) })
	e.pending.Register("syscall", func() { stdlibsyscall.Install(e.bindBound, h) })
	e.pending.Register("internal/bytealg", func() { stdlibbytealg.Install(e.bindBound, h) })
	e.pending.Register("internal/race", func() { stdlibrace.Install(e.bindBound, h) })
	e.pending.Register("internal/stringslite", func() { stdlibstringslite.Install(e.bindBound, h) })
	e.pending.Register("html", func() { stdlibhtml.Install(e.bindBound) })
	e.pending.Register("path", func() { stdlibpath.Install(e.bindBound) })
	e.pending.Register("reflect", func() { stdreflect.Install(e.bindBound, e.reflectHooks()) })
	e.pending.Register("sort", func() { stdlibsort.Install(e.bindBound, h) })
	e.pending.Register("slices", func() { stdlibslices.Install(e.bindBound, h, e.typedSeq) })
	e.pending.Register("maps", func() { stdlibmaps.Install(e.bindBound, h, e.typedSeq) })
	e.pending.Register("os", func() { stdlibos.Install(e.bindBound, h, h.env) })
	e.pending.Register("path/filepath", func() { stdlibfilepath.Install(e.bindBound, h, h.env) })
	e.pending.Register("os/exec", func() { stdlibosexec.Install(e.bindBound, h, h.env) })
	e.pending.Register("minigo.dev/host", func() { stdlibhost.Install(e.bindBound, h, h.env) })
	e.pending.Register("github.com/podhmo/minigo/host", func() { stdlibhost.Install(e.bindBound, h, h.env) })
	stdinspectHooks := stdinspect.Hooks{
		LoadPath:        e.loadPath,
		LoadPathLenient: e.loadPathLenient,
		LoadDirLenient:  e.loadDirLenient,
		LoadFileLenient: e.loadFileLenient,
		FSPath:          e.fsPath,
		Materialize:     e.materialize,
		BoundOf:         e.BoundOf,
		SourceOf:        e.SourceOf,
	}
	e.pending.Register("minigo.dev/inspect", func() { stdinspect.Install(e.bindBound, stdinspectHooks) })
	e.pending.Register("github.com/podhmo/minigo/inspect", func() { stdinspect.Install(e.bindBound, stdinspectHooks) })
	e.pending.Register("unsafe", func() { stdunsafe.Install(e.bindBound, e.reflectHooks()) })
	e.pending.Register("runtime", func() { stdruntime.Install(e.bindBound, h) })
	e.pending.Register("time", func() { stdlibtime.Install(e.bindBound, h) })
	e.pending.Register("sync", func() { stdlibsync.Install(e.bindBound, h) })
	e.pending.Register("sync/atomic", func() { stdatomic.Install(e.bindBound, h) })
	e.pending.Register("context", func() { stdlibcontext.Install(e.bindBound, h) })
	e.pending.Register("net", func() { stdlibnet.Install(e.bindBound, h, engineFileOf) })
	e.pending.Register("net/netip", func() { stdlibnetip.Install(e.bindBound) })
	e.pending.Register("io", func() { stdio.Install(e.bindBound, h, h.ioAdapters()) })
	e.pending.Register("crypto/rand", func() { stdlibcrand.Install(e.bindBound, h, h.ioAdapters(), stdfmt.ScriptTypeString) })
	e.pending.Register("crypto/sha256", func() { stdlibsha256.Install(e.bindBound) })
	e.pending.Register("encoding/csv", func() { stdlibcsv.Install(e.bindBound, h, h.ioAdapters()) })
	e.pending.Register("bufio", func() { stdlibbufio.Install(e.bindBound, h, h.ioAdapters()) })
	e.pending.Register("text/template", func() { stdlibtexttemplate.Install(e.bindBound, h, e.templateFuncMap()) })
	e.pending.Register("html/template", func() { stdlibhtmltemplate.Install(e.bindBound, h, e.templateFuncMap()) })
}
