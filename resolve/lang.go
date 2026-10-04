package resolve

import (
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// ModuleLang returns the `go` directive of the module enclosing dir as raw
// directive text (e.g. "1.26.0"), or "" when dir is outside any module —
// files without a governing go.mod stay unversioned and keep the
// toolchain's newest features, matching `go run` on a module-less file.
// The nearest ancestor go.mod wins, so packages in the module cache see
// their own module's version and GOROOT packages see src/go.mod.
func ModuleLang(dir string) string {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			f, err := modfile.Parse(filepath.Join(d, "go.mod"), data, nil)
			if err != nil || f.Go == nil {
				return ""
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
