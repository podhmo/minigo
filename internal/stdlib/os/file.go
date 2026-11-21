package stdos

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"syscall"
	"time"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/stdlib"
)

// file.go: the *os.File facade returned by Open/Create/OpenFile/
// CreateTemp plus the fsOp2 variant that wraps the result.

// fsFileOp is fsOp2 for an op returning *os.File: the result is the
// scriptFile facade so Name() and op errors spell the path as the
// script passed it (gc), not the resolved absolute path.
func fsFileOp(env intbind.Env, a []any, op func(string) (*os.File, error)) (any, error) {
	sp := stdlib.Str(a[0])
	p, err := env.FsPath(sp)
	if err != nil {
		return nil, err
	}
	f, err := op(p)
	if err != nil {
		return stdlib.RetErr2(f, stdlib.ScriptPathErr(err, sp, p))
	}
	return stdlib.RetErr2(&scriptFile{File: f, name: sp}, nil)
}

// fsTempDir resolves the dir argument of MkdirTemp/CreateTemp: an empty
// dir means os.TempDir() — only allowed when the engine is unrestricted
// (a temp dir outside the roots could otherwise anchor escaped writes).
func fsTempDir(env intbind.Env, dir string) (string, error) {
	if dir == "" {
		if len(env.AllowedRoots()) == 0 {
			return "", nil
		}
		return "", errors.New("dir must name a directory inside the allowed roots")
	}
	return env.FsPath(dir)
}

// scriptFile is the *os.File facade Open/Create/OpenFile/CreateTemp
// return: the host file lives at the resolved absolute path, but gc
// reports f.Name() and the file's op errors (`read f.txt: file already
// closed`) in the spelling the caller passed. APIType keeps %T and
// `x.(*os.File)` asserts reading *os.File like the Stdout facade.
type scriptFile struct {
	*os.File
	name string
}

func (f *scriptFile) Name() string          { return f.name }
func (f *scriptFile) APIType() reflect.Type { return reflect.TypeFor[*os.File]() }

// UnwrapHost hands host call sites demanding the API type the file the
// facade wraps — a []*os.File arg (exec.Cmd.ExtraFiles) gets *os.File,
// never the wrapper.
func (f *scriptFile) UnwrapHost() any { return f.File }

// err rewrites the path an op error reports back to the spelled name —
// gc builds the PathError with f.name as passed to Open.
func (f *scriptFile) err(err error) error {
	if pe, ok := err.(*fs.PathError); ok {
		pe.Path = stdlib.ScriptPathOf(f.name, f.File.Name(), pe.Path)
	}
	return err
}

func (f *scriptFile) Chdir() error               { return f.err(f.File.Chdir()) }
func (f *scriptFile) Chmod(m fs.FileMode) error  { return f.err(f.File.Chmod(m)) }
func (f *scriptFile) Chown(uid, gid int) error   { return f.err(f.File.Chown(uid, gid)) }
func (f *scriptFile) Close() error               { return f.err(f.File.Close()) }
func (f *scriptFile) Read(b []byte) (int, error) { n, e := f.File.Read(b); return n, f.err(e) }
func (f *scriptFile) ReadAt(b []byte, o int64) (int, error) {
	n, e := f.File.ReadAt(b, o)
	return n, f.err(e)
}
func (f *scriptFile) ReadDir(n int) ([]fs.DirEntry, error) {
	d, e := f.File.ReadDir(n)
	return d, f.err(e)
}
func (f *scriptFile) ReadFrom(r io.Reader) (int64, error) {
	n, e := f.File.ReadFrom(r)
	return n, f.err(e)
}
func (f *scriptFile) Readdir(n int) ([]fs.FileInfo, error) {
	i, e := f.File.Readdir(n)
	return i, f.err(e)
}
func (f *scriptFile) Readdirnames(n int) ([]string, error) {
	s, e := f.File.Readdirnames(n)
	return s, f.err(e)
}
func (f *scriptFile) Seek(o int64, w int) (int64, error) {
	n, e := f.File.Seek(o, w)
	return n, f.err(e)
}
func (f *scriptFile) SetDeadline(t time.Time) error      { return f.err(f.File.SetDeadline(t)) }
func (f *scriptFile) SetReadDeadline(t time.Time) error  { return f.err(f.File.SetReadDeadline(t)) }
func (f *scriptFile) SetWriteDeadline(t time.Time) error { return f.err(f.File.SetWriteDeadline(t)) }
func (f *scriptFile) Stat() (fs.FileInfo, error)         { i, e := f.File.Stat(); return i, f.err(e) }
func (f *scriptFile) Sync() error                        { return f.err(f.File.Sync()) }
func (f *scriptFile) SyscallConn() (syscall.RawConn, error) {
	c, e := f.File.SyscallConn()
	return c, f.err(e)
}
func (f *scriptFile) Truncate(size int64) error   { return f.err(f.File.Truncate(size)) }
func (f *scriptFile) Write(b []byte) (int, error) { n, e := f.File.Write(b); return n, f.err(e) }
func (f *scriptFile) WriteAt(b []byte, o int64) (int, error) {
	n, e := f.File.WriteAt(b, o)
	return n, f.err(e)
}
func (f *scriptFile) WriteString(s string) (int, error) {
	n, e := f.File.WriteString(s)
	return n, f.err(e)
}
