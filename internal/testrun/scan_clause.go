package testrun

import (
	"strings"

	"github.com/podhmo/minigo/resolve"
)

// packageClause reads the package clause the non-test files of dir
// agree on — the majority vote [resolve.ReadPackageFiles] applies.
func packageClause(dir string, cfg resolve.BuildConfig) (string, error) {
	meta, err := resolve.ReadPackageFiles(dir, "", cfg)
	if err != nil {
		return "", err
	}
	return meta.Name, nil
}

// baseClause names the package of a test-only directory: strip the
// _test suffix from the test files' clause when one is there (a
// directory of only `package foo_test` files tests `foo`).
func baseClause(files []scannedFile) string {
	name := ""
	if len(files) > 0 {
		name = files[0].clause
	}
	return strings.TrimSuffix(name, "_test")
}
