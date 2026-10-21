package minigo

import (
	"io"
	"io/fs"

	"github.com/podhmo/minigo/runtime"
)

// hostVars pins package-level vars of source-interpreted stdlib packages
// to their host values, keyed by import path then var name. A sentinel
// error declared in a package minigo builds from source would otherwise
// exist twice: the interpreted errors.New object and the host one that
// host-bound packages return and compare against. io/fs is interpreted
// while os and path/filepath are bound, so without the pin
// errors.Is(err, fs.ErrNotExist) on an os.Open error is false and a
// WalkDir callback's fs.SkipDir reaches the host walker as an unknown
// error. The compiler binds each listed var to the host value in place of
// its initializer (runtime.Package.HostVars), so the package's own code
// (`err == SkipDir` in fs.WalkDir) compares against the same object.
//
// internal/oserror is listed too: it is where io/fs's sentinels come from,
// and any other source package importing it gets the host objects.
var hostVars = map[string]map[string]runtime.Value{
	// io under --src: host-bound readers (strings.Reader, bufio) return
	// the host io.EOF, so the interpreted io must name the same objects.
	"io": {
		"EOF":              &runtime.GoValue{V: io.EOF},
		"ErrUnexpectedEOF": &runtime.GoValue{V: io.ErrUnexpectedEOF},
		"ErrShortWrite":    &runtime.GoValue{V: io.ErrShortWrite},
		"ErrShortBuffer":   &runtime.GoValue{V: io.ErrShortBuffer},
		"ErrNoProgress":    &runtime.GoValue{V: io.ErrNoProgress},
		"ErrClosedPipe":    &runtime.GoValue{V: io.ErrClosedPipe},
	},
	"io/fs": {
		"ErrInvalid":    &runtime.GoValue{V: fs.ErrInvalid},
		"ErrPermission": &runtime.GoValue{V: fs.ErrPermission},
		"ErrExist":      &runtime.GoValue{V: fs.ErrExist},
		"ErrNotExist":   &runtime.GoValue{V: fs.ErrNotExist},
		"ErrClosed":     &runtime.GoValue{V: fs.ErrClosed},
		"SkipDir":       &runtime.GoValue{V: fs.SkipDir},
		"SkipAll":       &runtime.GoValue{V: fs.SkipAll},
	},
	// internal/oserror's vars are the io/fs ones (fs.ErrInvalid is
	// oserror.ErrInvalid), so the host values come through io/fs.
	"internal/oserror": {
		"ErrInvalid":    &runtime.GoValue{V: fs.ErrInvalid},
		"ErrPermission": &runtime.GoValue{V: fs.ErrPermission},
		"ErrExist":      &runtime.GoValue{V: fs.ErrExist},
		"ErrNotExist":   &runtime.GoValue{V: fs.ErrNotExist},
		"ErrClosed":     &runtime.GoValue{V: fs.ErrClosed},
	},
}
