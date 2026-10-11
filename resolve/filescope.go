package resolve

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FileScopeFiles returns the .go files the named file shares its
// package with: the named file's package clause defines the scope and
// siblings keep only files declaring the same clause. Unlike
// ReadPackageFiles no //go:build constraint filtering applies — a file
// excluded by build tags still sees its package (task-runner
// conventions like `//go:build task`) — while go/build's FILENAME
// invisibility stays: _GOOS/_GOARCH suffixes, _test.go, and
// _/.-prefixed names are never read. The named file itself is always
// included — it was asked for explicitly.
func FileScopeFiles(dir, named string, cfg BuildConfig) ([]string, error) {
	fset := token.NewFileSet()
	pkgName, err := packageClause(fset, named)
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
		name, err := packageClause(fset, abs)
		if err != nil {
			continue // a file whose package clause cannot be read contributes nothing
		}
		if name == pkgName {
			gofiles = append(gofiles, abs)
		}
	}
	return gofiles, nil
}

// packageClause reads just a file's package clause.
func packageClause(fset *token.FileSet, abs string) (string, error) {
	f, err := parser.ParseFile(fset, abs, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return f.Name.Name, nil
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
