// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/podhmo/minigo/runtime"
)

// scriptIfaceWT/scriptIfaceRF/scriptIfaceWTRF carry the io extension
// methods a script value may declare: embedding *scriptIface promotes
// its methods and the variant adds only the probe methods the script
// actually has, so the concrete method set stays honest.
type (
	scriptIfaceWT   struct{ *scriptIface }
	scriptIfaceRF   struct{ *scriptIface }
	scriptIfaceWTRF struct{ *scriptIface }
)

func (s scriptIfaceWT) WriteTo(w io.Writer) (int64, error) { return s.writeTo(w) }

func (s scriptIfaceRF) ReadFrom(r io.Reader) (int64, error) { return s.readFrom(r) }

func (s scriptIfaceWTRF) WriteTo(w io.Writer) (int64, error) { return s.writeTo(w) }

func (s scriptIfaceWTRF) ReadFrom(r io.Reader) (int64, error) {
	return s.readFrom(r)
}

// scriptIface forwards host-interface calls into script: the script
// value's Read/Write/Close/... methods run through vc, so an
// interpreted type can serve as an io.Writer or error to bound host
// helpers. Its method set omits the io extension methods WriteTo and
// ReadFrom — host code probes those opportunistically (io.Copy asserts
// them on its io.Reader/io.Writer args), so they only appear on the
// scriptIfaceW* variants adaptIface picks when the script declares them.
type scriptIface struct {
	vc   runtime.VMCaller
	recv runtime.Value
}

// ScriptValue reports the script receiver the proxy forwards to: when
// host code hands the proxy back (an io.Writer accessor, a cancel cause
// adapted to error), the script sees its own value.
func (s *scriptIface) ScriptValue() runtime.Value { return s.recv }

func (s *scriptIface) call(name string, args ...runtime.Value) ([]runtime.Value, error) {
	vc := callbackContext(s.vc)
	m, ok := vc.Member(s.recv, name)
	if !ok || m == nil || m == runtime.NIL {
		return nil, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	r, err := vc.Call(m, args)
	if err != nil {
		return nil, err
	}
	if t, ok := r.(*runtime.Tuple); ok {
		return t.Elems, nil
	}
	return []runtime.Value{r}, nil
}

func (s *scriptIface) Write(p []byte) (int, error) {
	pv := byteSliceArg(p)
	rs, err := s.call("Write", pv)
	n := int(intOut(rs, 0))
	backBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, errOut(s.vc, rs, 1)
}

func (s *scriptIface) Read(p []byte) (int, error) {
	pv := byteSliceArg(p)
	rs, err := s.call("Read", pv)
	n := int(intOut(rs, 0))
	backBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, errOut(s.vc, rs, 1)
}

func (s *scriptIface) Close() error {
	rs, err := s.call("Close")
	if err != nil {
		return err
	}
	return errOut(s.vc, rs, 0)
}

func (s *scriptIface) WriteString(str string) (int, error) {
	rs, err := s.call("WriteString", str)
	if err != nil {
		return 0, err
	}
	return int(intOut(rs, 0)), errOut(s.vc, rs, 1)
}

func (s *scriptIface) ReadByte() (byte, error) {
	rs, err := s.call("ReadByte")
	if err != nil {
		return 0, err
	}
	return byte(intOut(rs, 0)), errOut(s.vc, rs, 1)
}

func (s *scriptIface) WriteByte(b byte) error {
	rs, err := s.call("WriteByte", runtime.ByteValue(b))
	if err != nil {
		return err
	}
	return errOut(s.vc, rs, 0)
}

func (s *scriptIface) ReadRune() (rune, int, error) {
	rs, err := s.call("ReadRune")
	if err != nil {
		return 0, 0, err
	}
	return rune(intOut(rs, 0)), int(intOut(rs, 1)), errOut(s.vc, rs, 2)
}

func (s *scriptIface) Seek(offset int64, whence int) (int64, error) {
	rs, err := s.call("Seek", offset, int64(whence))
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(s.vc, rs, 1)
}

func (s *scriptIface) Flush() error {
	rs, err := s.call("Flush")
	if err != nil {
		return err
	}
	return errOut(s.vc, rs, 0)
}

func (s *scriptIface) Error() string {
	rs, err := s.call("Error")
	if err != nil || len(rs) == 0 {
		return "script error"
	}
	if str, ok := runtime.Unwrap(rs[0]).(string); ok {
		return str
	}
	return "script error"
}

func (s *scriptIface) String() string {
	rs, err := s.call("String")
	if err != nil || len(rs) == 0 {
		return ""
	}
	if str, ok := runtime.Unwrap(rs[0]).(string); ok {
		return str
	}
	return ""
}

func (s *scriptIface) Len() int {
	rs, err := s.call("Len")
	if err != nil {
		return 0
	}
	return int(intOut(rs, 0))
}

func (s *scriptIface) Less(i, j int) bool {
	rs, err := s.call("Less", int64(i), int64(j))
	if err != nil || len(rs) == 0 {
		return false
	}
	b, _ := runtime.Unwrap(rs[0]).(bool)
	return b
}

func (s *scriptIface) Swap(i, j int) {
	s.call("Swap", int64(i), int64(j))
}

// writeTo/readFrom back the io extension methods — they are
// deliberately unexported so *scriptIface's method set never claims an
// io.WriterTo/io.ReaderFrom the script value does not declare (only the
// scriptIfaceW* wrapper types surface them).
func (s *scriptIface) writeTo(w io.Writer) (int64, error) {
	rs, err := s.call("WriteTo", &runtime.GoValue{V: w})
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(s.vc, rs, 1)
}

func (s *scriptIface) readFrom(r io.Reader) (int64, error) {
	rs, err := s.call("ReadFrom", &runtime.GoValue{V: r})
	if err != nil {
		return 0, err
	}
	return intOut(rs, 0), errOut(s.vc, rs, 1)
}

// scriptFSFor reports the fs.FS adapter for v when v declares Open —
// nil otherwise, so a script value that is not a filesystem keeps the
// generic error. The optional fs extension methods (ReadDir for
// fs.ReadDirFS, ReadFile for fs.ReadFileFS — asserted by fs.ReadDir /
// fs.ReadFile, and reached by fs.Glob, fs.WalkDir, and template.ParseFS)
// surface only when the script declares them, like the io extension
// probes: fs.GlobFS/fs.StatFS stay unadopted, so host helpers asserting
// them fall back through Open.
func scriptFSFor(vc runtime.VMCaller, v runtime.Value) any {
	if _, ok := runtime.IfaceMember(vc, v, "Open"); !ok {
		return nil
	}
	sfs := &scriptFS{scriptIface: &scriptIface{vc: vc, recv: v}}
	_, hasDir := runtime.IfaceMember(vc, v, "ReadDir")
	_, hasFile := runtime.IfaceMember(vc, v, "ReadFile")
	switch {
	case hasDir && hasFile:
		return scriptFSRDRF{sfs}
	case hasDir:
		return scriptFSRD{sfs}
	case hasFile:
		return scriptFSRF{sfs}
	}
	return sfs
}

// scriptFileFor reports the fs.File adapter for v when v declares
// Stat — the fs.File contract's discriminator beside Read/Close — nil
// otherwise, so a non-file value keeps the generic error. fileOf does
// the per-file variant probing.
func scriptFileFor(vc runtime.VMCaller, v runtime.Value) any {
	if _, ok := runtime.IfaceMember(callbackContext(vc), v, "Stat"); !ok {
		return nil
	}
	return (&scriptFS{scriptIface: &scriptIface{vc: vc, recv: v}}).fileOf(v)
}

// scriptFS/scriptFSRD/scriptFSRF/scriptFSRDRF adapt a script fs.FS: the
// base type carries fs.FS's Open, and each variant adds only the
// optional surface the script declares — like scriptIface's WriteTo/
// ReadFrom variants, an unprobed method would let a host assertion
// (fs.ReadDirFS, fs.ReadFileFS) claim support the script lacks.
type (
	scriptFS     struct{ *scriptIface }
	scriptFSRD   struct{ *scriptFS }
	scriptFSRF   struct{ *scriptFS }
	scriptFSRDRF struct{ *scriptFS }
)

func (s *scriptFS) Open(name string) (fs.File, error) {
	rs, err := s.call("Open", name)
	if err != nil {
		return nil, err
	}
	if err := errOut(s.vc, rs, 1); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("script Open returned no file")
	}
	switch runtime.Unwrap(rs[0]).(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		// (nil, nil) violates fs.FS's contract — host helpers would
		// nil-deref the file; report it as the script's error instead.
		return nil, fmt.Errorf("script Open returned a nil file with a nil error")
	}
	return s.fileOf(rs[0]), nil
}

// fileOf wraps a script Open result as a host fs.File. A host file
// riding inside a script value passes through untouched. A script file
// gets scriptFile — Read/Close/Stat only — plus wrapper types for the
// optional surfaces the script declares (Seek, which http.FS asserts;
// WriteTo, which io.Copy asserts; ReadDir, which fs.ReadDir/fs.Glob/
// fs.WalkDir assert), so a host probe never finds a method the script
// lacks.
func (s *scriptFS) fileOf(v runtime.Value) fs.File {
	if gv, ok := runtime.Unwrap(v).(*runtime.GoValue); ok {
		if f, ok := gv.V.(fs.File); ok {
			return f
		}
	}
	si := &scriptIface{vc: s.vc, recv: v}
	f := &scriptFile{inner: si}
	ctx := callbackContext(s.vc)
	_, seek := runtime.IfaceMember(ctx, v, "Seek")
	_, wt := runtime.IfaceMember(ctx, v, "WriteTo")
	if _, ok := runtime.IfaceMember(ctx, v, "ReadDir"); ok {
		// a directory file can still be seekable — pick a dir variant
		// carrying every optional surface the script declares, since
		// embedding fs.File would hide Seek/WriteTo again.
		df := scriptDirFile{File: f, inner: si}
		switch {
		case seek && wt:
			return scriptDirFileSeekWT{df}
		case seek:
			return scriptDirFileSeek{df}
		case wt:
			return scriptDirFileWT{df}
		}
		return df
	}
	switch {
	case seek && wt:
		return scriptFileSeekWT{f}
	case seek:
		return scriptFileSeek{f}
	case wt:
		return scriptFileWT{f}
	}
	return f
}

// readDir/readFile back the fs extension variants — unexported like
// writeTo/readFrom so scriptFS never advertises what the script lacks.
func (s *scriptFS) readDir(name string) ([]fs.DirEntry, error) {
	rs, err := s.call("ReadDir", name)
	if err != nil {
		return nil, err
	}
	return dirEntriesOf(s.vc, rs, errOut(s.vc, rs, 1))
}

// dirEntriesOf wraps each element of a script []fs.DirEntry result as a
// host DirEntry proxy. A nil script slice means an empty directory, and
// the script's own error rides along so partial results survive — e.g.
// entries plus io.EOF, which fs.ReadDirFile permits.
func dirEntriesOf(vc runtime.VMCaller, rs []runtime.Value, serr error) ([]fs.DirEntry, error) {
	if len(rs) == 0 {
		return nil, serr
	}
	switch v := runtime.Unwrap(rs[0]).(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return nil, serr
	case *runtime.GoValue:
		if es, ok := v.V.([]fs.DirEntry); ok {
			return es, serr
		}
		if serr != nil {
			return nil, serr
		}
		return nil, fmt.Errorf("script ReadDir returned %T, want []fs.DirEntry", v.V)
	case *runtime.Slice:
		out := make([]fs.DirEntry, 0, len(v.Elems))
		for _, e := range v.Elems {
			out = append(out, &scriptDirEntry{scriptIface: &scriptIface{vc: vc, recv: e}})
		}
		return out, serr
	}
	if serr != nil {
		return nil, serr
	}
	return nil, fmt.Errorf("script ReadDir returned %T, want []fs.DirEntry", rs[0])
}

func (s *scriptFS) readFile(name string) ([]byte, error) {
	rs, err := s.call("ReadFile", name)
	if err != nil {
		return nil, err
	}
	serr := errOut(s.vc, rs, 1)
	if len(rs) == 0 {
		return nil, serr
	}
	switch v := runtime.Unwrap(rs[0]).(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return nil, serr
	case *runtime.GoValue:
		if bs, ok := v.V.([]byte); ok {
			return bs, serr
		}
	case *runtime.Slice:
		if bs, ok := scriptBytes(v); ok {
			return bs, serr
		}
	}
	if serr != nil {
		return nil, serr
	}
	return nil, fmt.Errorf("script ReadFile returned %T, want []byte", rs[0])
}

func (s scriptFSRD) ReadDir(name string) ([]fs.DirEntry, error) { return s.readDir(name) }

func (s scriptFSRF) ReadFile(name string) ([]byte, error) { return s.readFile(name) }

func (s scriptFSRDRF) ReadDir(name string) ([]fs.DirEntry, error) { return s.readDir(name) }

func (s scriptFSRDRF) ReadFile(name string) ([]byte, error) { return s.readFile(name) }

// scriptFile adapts a script fs.File with just the fs.File contract —
// Read, Close, Stat. It cannot embed *scriptIface: scriptIface's Seek/
// ReadByte/WriteTo surface would answer host interface probes the
// script never declared, turning a host's fallback path into a failed
// call. Optional methods join through the wrapper types below.
type scriptFile struct{ inner *scriptIface }

func (s *scriptFile) Read(p []byte) (int, error) { return s.inner.Read(p) }

func (s *scriptFile) Close() error { return s.inner.Close() }

// ScriptValue lets goValueOf hand the original script file back when
// host code carries the proxy through an fs.File return — declared on
// scriptFile so every wrapper variant promotes it (scriptDirFile
// embeds fs.File, which hides it, so it declares its own).
func (s *scriptFile) ScriptValue() runtime.Value { return s.inner.ScriptValue() }

func (s *scriptFile) Stat() (fs.FileInfo, error) {
	rs, err := s.inner.call("Stat")
	if err != nil {
		return nil, err
	}
	if err := errOut(s.inner.vc, rs, 1); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("script Stat returned no FileInfo")
	}
	return &scriptFileInfo{scriptIface: &scriptIface{vc: s.inner.vc, recv: rs[0]}}, nil
}

// scriptFileSeek/scriptFileWT/scriptFileSeekWT add the io extension
// methods a script file declares: Seek is asserted by http.FS and
// http.ServeContent, WriteTo by io.Copy.
type (
	scriptFileSeek   struct{ *scriptFile }
	scriptFileWT     struct{ *scriptFile }
	scriptFileSeekWT struct{ *scriptFile }
)

func (s scriptFileSeek) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func (s scriptFileWT) WriteTo(w io.Writer) (int64, error) { return s.inner.writeTo(w) }

func (s scriptFileSeekWT) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func (s scriptFileSeekWT) WriteTo(w io.Writer) (int64, error) { return s.inner.writeTo(w) }

// scriptDirFile layers fs.ReadDirFile's ReadDir onto whichever file
// variant the probes selected.
type scriptDirFile struct {
	fs.File
	inner *scriptIface
}

func (s scriptDirFile) ReadDir(count int) ([]fs.DirEntry, error) {
	rs, err := s.inner.call("ReadDir", int64(count))
	if err != nil {
		return nil, err
	}
	return dirEntriesOf(s.inner.vc, rs, errOut(s.inner.vc, rs, 1))
}

// ScriptValue: see scriptFile.ScriptValue — the embedded fs.File
// interface hides the inner file's own.
func (s scriptDirFile) ScriptValue() runtime.Value { return s.inner.ScriptValue() }

// scriptDirFileSeek/scriptDirFileWT/scriptDirFileSeekWT restore the
// Seek/WriteTo probes on a directory file that declares them.
type (
	scriptDirFileSeek   struct{ scriptDirFile }
	scriptDirFileWT     struct{ scriptDirFile }
	scriptDirFileSeekWT struct{ scriptDirFile }
)

func (s scriptDirFileSeek) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func (s scriptDirFileWT) WriteTo(w io.Writer) (int64, error) { return s.inner.writeTo(w) }

func (s scriptDirFileSeekWT) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func (s scriptDirFileSeekWT) WriteTo(w io.Writer) (int64, error) {
	return s.inner.writeTo(w)
}

// scriptFileInfo proxies a script fs.FileInfo: each getter forwards to
// the script method, so Mode/ModTime/Size read lazily like a real
// FileInfo's.
type scriptFileInfo struct{ *scriptIface }

func (s *scriptFileInfo) Name() string {
	rs, _ := s.call("Name")
	return strOut(rs, 0)
}

func (s *scriptFileInfo) Size() int64 {
	rs, _ := s.call("Size")
	return intOut(rs, 0)
}

func (s *scriptFileInfo) Mode() fs.FileMode {
	rs, _ := s.call("Mode")
	return fileModeOut(rs, 0)
}

func (s *scriptFileInfo) ModTime() time.Time {
	rs, err := s.call("ModTime")
	if err == nil && len(rs) > 0 {
		if gv, ok := runtime.Unwrap(rs[0]).(*runtime.GoValue); ok {
			if tm, ok := gv.V.(time.Time); ok {
				return tm
			}
		}
	}
	return time.Time{}
}

func (s *scriptFileInfo) IsDir() bool {
	rs, _ := s.call("IsDir")
	return boolOut(rs, 0)
}

func (s *scriptFileInfo) Sys() any {
	rs, err := s.call("Sys")
	if err != nil || len(rs) == 0 {
		return nil
	}
	switch v := runtime.Unwrap(rs[0]).(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return nil
	case *runtime.GoValue:
		return v.V
	default:
		return v
	}
}

// scriptDirEntry proxies a script fs.DirEntry — the element type of a
// script ReadDir result.
type scriptDirEntry struct{ *scriptIface }

func (s *scriptDirEntry) Name() string {
	rs, _ := s.call("Name")
	return strOut(rs, 0)
}

func (s *scriptDirEntry) IsDir() bool {
	rs, _ := s.call("IsDir")
	return boolOut(rs, 0)
}

func (s *scriptDirEntry) Type() fs.FileMode {
	rs, _ := s.call("Type")
	return fileModeOut(rs, 0)
}

func (s *scriptDirEntry) Info() (fs.FileInfo, error) {
	rs, err := s.call("Info")
	if err != nil {
		return nil, err
	}
	if err := errOut(s.vc, rs, 1); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("script DirEntry.Info returned no FileInfo")
	}
	return &scriptFileInfo{scriptIface: &scriptIface{vc: s.vc, recv: rs[0]}}, nil
}
