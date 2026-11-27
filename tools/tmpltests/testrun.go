package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun"
)

// runTestRun executes the copied internal test files in one minigo-test
// invocation. The scratch GOROOT is passed as resolver state rather than
// through the process environment, and each test gets an isolated engine
// session so package-level mutation cannot change a later test's oracle.
func runTestRun(pkgDir, gr, srcList, only string, timeout time.Duration) error {
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
	modes := map[string]minigo.PackageMode{}
	for _, path := range strings.Split(srcList, ",") {
		if path != "" {
			modes[path] = minigo.ModeSource
		}
	}
	err = testrun.Run(context.Background(), testrun.Options{
		Dir:     pkgDir,
		Out:     os.Stdout,
		Verbose: true,
		Run:     only,
		Timeout: timeout,
		Isolate: true,
		Modes:   modes,
		GOROOT:  gr,
	})
	if err == testrun.ErrFailed {
		// tmpltests is a conformance report harness: like the process
		// runner, non-passing upstream tests are data in the report rather
		// than a failure of the harness itself.
		return nil
	}
	return err
}
