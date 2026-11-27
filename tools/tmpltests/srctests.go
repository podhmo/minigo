package main

import (
	"os"
	"path/filepath"
	"strings"
)

// renameSrcTests turns copied upstream *_srctest.go files into internal
// test files for the testrun runner.
func renameSrcTests(pkgDir string) error {
	ents, err := os.ReadDir(pkgDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, "_srctest.go") {
			continue
		}
		to := strings.TrimSuffix(name, "_srctest.go") + "_test.go"
		if err := os.Rename(filepath.Join(pkgDir, name), filepath.Join(pkgDir, to)); err != nil {
			return err
		}
	}
	return nil
}
