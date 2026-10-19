package resolve

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FileScopeFiles returns named, a .go file in dir, followed by the
// sibling files in dir that declare the same package clause. Unlike
// [ReadPackageFiles] no //go:build constraint filtering applies, so a file
// excluded by build tags (task-runner conventions like `//go:build task`)
// still sees its package. Siblings that go/build ignores by name
// (_GOOS/_GOARCH suffixes for another platform, _test.go, names starting
// with "_" or "."), import "C", or whose package clause does not parse are
// left out. named must be spelled as filepath.Join(dir, base) to be
// recognized among the siblings; an unreadable package clause in named is
// an error.
func FileScopeFiles(dir, named string, cfg BuildConfig) ([]string, error) {
	fset := token.NewFileSet()
	pkgName, _, err := packageClause(fset, named)
	if err != nil {
		return nil, err
	}
	goos, goarch := cfg.GOOS, cfg.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	gofiles := []string{named}
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") ||
			strings.HasPrefix(fname, "_") || strings.HasPrefix(fname, ".") {
			continue
		}
		abs := filepath.Join(dir, fname)
		if abs == named || !goodOSArchFile(fname, goos, goarch) {
			continue
		}
		name, cgo, err := packageClause(fset, abs)
		if err != nil || cgo {
			// a file whose package clause cannot be read — or that uses
			// cgo — contributes nothing
			continue
		}
		if name == pkgName {
			gofiles = append(gofiles, abs)
		}
	}
	return gofiles, nil
}

// packageClause reads a file's package clause and import block, reporting
// the clause name and whether the file uses cgo (imports "C").
func packageClause(fset *token.FileSet, abs string) (name string, cgo bool, err error) {
	f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
	if err != nil {
		return "", false, err
	}
	return f.Name.Name, importsC(f), nil
}

// goodOSArchFile replicates the filename half of go/build's
// invisibility: name_GOOS.go / name_GOARCH.go / name_GOOS_GOARCH.go
// name their own visibility without a build constraint. The
// GOOS/GOARCH tables mirror internal/syslist (which cannot be
// imported) — they are the "past, present, and future" lists gc
// consults for filename matching.
func goodOSArchFile(name, goos, goarch string) bool {
	base, _, _ := strings.Cut(name, ".")
	// everything before the first _ is an ordinary prefix — only
	// suffixes can name targets (a bare "linux.go" means nothing).
	i := strings.Index(base, "_")
	if i < 0 {
		return true
	}
	l := strings.Split(base[i:], "_")
	if n := len(l); n > 0 && l[n-1] == "test" {
		l = l[:n-1]
	}
	n := len(l)
	if n >= 2 && knownOS[l[n-2]] && knownArch[l[n-1]] {
		return l[n-2] == goos && l[n-1] == goarch
	}
	if n >= 1 && (knownOS[l[n-1]] || knownArch[l[n-1]]) {
		return l[n-1] == goos || l[n-1] == goarch
	}
	return true
}

var knownOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
}

var knownArch = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true,
	"armbe": true, "arm64": true, "arm64be": true, "loong64": true,
	"mips": true, "mipsle": true, "mips64": true, "mips64le": true,
	"mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
	"ppc64le": true, "riscv": true, "riscv64": true, "s390": true,
	"s390x": true, "sparc": true, "sparc64": true, "wasm": true,
}
