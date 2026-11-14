package main

// Script-side fs.FS crossing into bound host code (#1068): a
// //go:embed'd embed.FS — and a script-defined fs.FS declaring only
// Open — adapt to a host fs.FS parameter through a live proxy, and a
// bound template.ParseFS consumes the same script value.

import (
	"bytes"
	"embed"
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
	return memInfo{name: f.name, size: int64(len(f.data))}, nil
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
	name string
	size int64
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }

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
