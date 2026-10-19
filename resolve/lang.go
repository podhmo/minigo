package resolve

import (
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// ModuleLang returns the `go` directive of the nearest go.mod at or above
// dir as raw directive text (e.g. "1.26.0"), or "" when there is none or
// it does not parse. "" means unversioned: the newest language features
// apply, as with `go run` on a module-less file. A go.mod without a `go`
// directive yields "1.16", as the go command assumes.
func ModuleLang(dir string) string {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			f, err := modfile.Parse(filepath.Join(d, "go.mod"), data, nil)
			if err != nil {
				return ""
			}
			if f.Go == nil {
				// the go command assumes go1.16 for a go.mod with no
				// `go` directive, and compiles with -lang=go1.16
				return "1.16"
			}
			return f.Go.Version
		}
		if !os.IsNotExist(err) {
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// ModulePath returns the module path declared by the nearest go.mod at or
// above dir, or "" when there is none.
func ModulePath(dir string) string {
	if f := enclosingModFile(dir); f != nil && f.Module != nil {
		return f.Module.Mod.Path
	}
	return ""
}

// RequiredVersion reports the version the module enclosing dir requires
// of modPath — the version a `go run` build stamps for a dependency. ok
// is false when there is no go.mod or it has no require for modPath.
func RequiredVersion(dir, modPath string) (string, bool) {
	f := enclosingModFile(dir)
	if f == nil {
		return "", false
	}
	for _, r := range f.Require {
		if r.Mod.Path == modPath {
			return r.Mod.Version, true
		}
	}
	return "", false
}

func enclosingModFile(dir string) *modfile.File {
	for d := dir; ; {
		name := filepath.Join(d, "go.mod")
		data, err := os.ReadFile(name)
		if err == nil {
			f, err := modfile.Parse(name, data, nil)
			if err != nil {
				return nil
			}
			return f
		}
		if !os.IsNotExist(err) {
			return nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}
