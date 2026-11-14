package main

// Script-side fs.FS crossing into bound host code (#1068): a
// //go:embed'd embed.FS — and a script-defined fs.FS declaring only
// Open — adapt to a host fs.FS parameter through a live proxy, and a
// bound template.ParseFS consumes the same script value.

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"io"
	"io/fs"
	"text/template"
	"time"

	"example.com/fsbox"
)

//go:embed tpl
var fsys embed.FS

// EmbedToHostFS: host fs.ReadFile reads a //go:embed'ed file — the
// script embed.FS declares ReadDir/ReadFile, so the proxy surfaces them
// instead of the Open+Read fallback.
func EmbedToHostFS() string {
	s, err := fsbox.ReadFile(fsys, "tpl/sub/b.txt")
	if err != nil {
		return "err:" + err.Error()
	}
	return s
}

// EmbedGlob: host fs.Glob lists the embedded dir — it reads through the
// adapted fs.ReadDirFS/ReadDirFile path.
func EmbedGlob() string {
	names, err := fsbox.Glob(fsys, "tpl/*.tmpl")
	if err != nil {
		return "err:" + err.Error()
	}
	return names
}

// ParseFSEmbed: bound text/template's ParseFS globs+reads through the
// script embed.FS and ExecuteTemplate renders the base-named template.
func ParseFSEmbed() string {
	t, err := template.ParseFS(fsys, "tpl/*.tmpl")
	if err != nil {
		return "err:" + err.Error()
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "a.tmpl", "world"); err != nil {
		return "err:" + err.Error()
	}
	return b.String()
}

// ParseFSEmbedHTML: bound html/template's ParseFS consumes the script
// embed.FS too, and the rendered output is context-escaped.
func ParseFSEmbedHTML() string {
	t, err := htmltemplate.ParseFS(fsys, "tpl/*.tmpl")
	if err != nil {
		return "err:" + err.Error()
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "a.tmpl", "<x>"); err != nil {
		return "err:" + err.Error()
	}
	return b.String()
}

// memFS is a script-defined fs.FS declaring Open only: host helpers
// must fall back to Open+Read since no ReadDir/ReadFile exists.
type memFS struct{ files map[string]string }

func (m memFS) Open(name string) (fs.File, error) {
	data, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{name: name, data: data}, nil
}

type memFile struct {
	name string
	data string
	off  int
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return memInfo{name: f.name, size: int64(len(f.data)), sys: fsbox.Token()}, nil
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}

func (f *memFile) Close() error { return nil }

type memInfo struct {
	name  string
	size  int64
	isDir bool
	sys   any
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir
	}
	return 0
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.isDir }
func (i memInfo) Sys() any           { return i.sys }

// treeFS serves one directory "d" and one file "f" — enough to check
// which io extension surfaces a script file advertises to the host and
// to exercise chunked ReadDir.
type treeFS struct{}

func (treeFS) Open(name string) (fs.File, error) {
	switch name {
	case "d":
		return &memDir{ents: []fs.DirEntry{memEntry{"x"}, memEntry{"y"}, memEntry{"z"}}}, nil
	case "f":
		return &memFile{name: "f", data: "data"}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// memDir is a script directory file whose ReadDir returns the final
// batch together with io.EOF, like os.File.Readdir — the host must
// still consume those entries.
type memDir struct {
	ents []fs.DirEntry
	off  int
}

func (d *memDir) Stat() (fs.FileInfo, error) { return memInfo{name: "d", isDir: true}, nil }
func (d *memDir) Read(p []byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: "d", Err: fs.ErrInvalid}
}
func (d *memDir) Close() error { return nil }

// Seek makes memDir a seekable directory — enough to prove a
// ReadDir+Seek script file keeps both probes on the host side.
func (d *memDir) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart && offset == 0 {
		d.off = 0
		return 0, nil
	}
	return 0, &fs.PathError{Op: "seek", Path: "d", Err: fs.ErrInvalid}
}

func (d *memDir) ReadDir(count int) ([]fs.DirEntry, error) {
	n := len(d.ents) - d.off
	if n == 0 {
		if count > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	if count > 0 && n > count {
		n = count
	}
	ents := d.ents[d.off : d.off+n]
	d.off += n
	if count > 0 && d.off == len(d.ents) {
		return ents, io.EOF
	}
	return ents, nil
}

type memEntry struct{ name string }

func (e memEntry) Name() string               { return e.name }
func (e memEntry) IsDir() bool                { return false }
func (e memEntry) Type() fs.FileMode          { return 0 }
func (e memEntry) Info() (fs.FileInfo, error) { return memInfo{name: e.name}, nil }

// emptyFS declares ReadDir/ReadFile that return nil slices — the proxy
// must read them as empty results, not type errors.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
func (emptyFS) ReadDir(name string) ([]fs.DirEntry, error) { return nil, nil }
func (emptyFS) ReadFile(name string) ([]byte, error)       { return nil, nil }

// ScriptToHostFS: host fs.ReadFile over a script fs with only Open —
// falls back to Open+Read, and file.Stat's FileInfo forwards to the
// script methods.
func ScriptToHostFS() string {
	m := memFS{files: map[string]string{"hello.txt": "hi host"}}
	s, err := fsbox.ReadFile(m, "hello.txt")
	if err != nil {
		return "err:" + err.Error()
	}
	return s
}

// ScriptToHostFSStat: the FileInfo fields a script Stat crosses — the
// host sees the script-declared Name/Size/IsDir/Mode.
func ScriptToHostFSStat() string {
	m := memFS{files: map[string]string{"hello.txt": "hi host"}}
	return fsbox.Stat(m, "hello.txt")
}

// EmbedMissing reports the script-side open error text the host sees.
func EmbedMissing() string {
	_, err := fsbox.ReadFile(fsys, "tpl/missing.txt")
	if err == nil {
		return "noerr"
	}
	return err.Error()
}

// HostFileProbes: a minimal script file advertises no io extension —
// the host must not see Seek, WriteTo, or ReadDirFile on it.
func HostFileProbes() string { return fsbox.Probes(treeFS{}, "f") }

// HostDirProbes: a script dir file declaring ReadDir surfaces
// fs.ReadDirFile — and nothing more.
func HostDirProbes() string { return fsbox.Probes(treeFS{}, "d") }

// HostEmbedFileProbes/HostEmbedDirProbes: the interpreted embed's
// openFile declares Seek (http.FS needs it), openDir declares ReadDir.
func HostEmbedFileProbes() string { return fsbox.Probes(fsys, "tpl/sub/b.txt") }
func HostEmbedDirProbes() string  { return fsbox.Probes(fsys, "tpl") }

// HostReadDirChunked: a host loop ReadDir(2) collects every entry even
// though the script returns the final batch together with io.EOF.
func HostReadDirChunked() string {
	names, err := fsbox.ChunkedReadDir(treeFS{}, "d", 2)
	if err != nil {
		return "err:" + err.Error()
	}
	return names
}

// HostEmptyDir/HostEmptyFile: nil slices from the script are empty
// results for the host, not type errors.
func HostEmptyDir() string {
	names, err := fsbox.ReadDirNames(emptyFS{}, "e")
	if err != nil {
		return "err:" + err.Error()
	}
	return "[" + names + "]"
}

func HostEmptyFile() string {
	s, err := fsbox.ReadFile(emptyFS{}, "e")
	if err != nil {
		return "err:" + err.Error()
	}
	return "[" + s + "]"
}

// HostSysTag: a host value carried by a script FileInfo's Sys crosses
// back unwrapped — the host sees the concrete type, not a GoValue box.
func HostSysTag() string { return fsbox.SysTag(treeFS{}, "f") }

// HostSeekDir: after a chunked drain the host seeks the directory file
// back to offset 0 and drains it again — ReadDir+Seek coexist on the
// same proxied file.
func HostSeekDir() string {
	names, err := fsbox.ReReadDir(treeFS{}, "d", 2)
	if err != nil {
		return "err:" + err.Error()
	}
	return names
}

// HostRoundTripFile/HostRoundTripDir: a file the host opened returns
// to script land as the original script value — type assertions on
// *memFile and *memDir must succeed.
func HostRoundTripFile() string {
	f, err := fsbox.OpenFile(treeFS{}, "f")
	if err != nil {
		return "err:" + err.Error()
	}
	if _, ok := f.(*memFile); !ok {
		return fmt.Sprintf("not-memfile:%T", f)
	}
	return "ok"
}

func HostRoundTripDir() string {
	f, err := fsbox.OpenFile(treeFS{}, "d")
	if err != nil {
		return "err:" + err.Error()
	}
	if _, ok := f.(*memDir); !ok {
		return fmt.Sprintf("not-memdir:%T", f)
	}
	return "ok"
}
