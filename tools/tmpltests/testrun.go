package main

import (
	"context"
	"strings"
	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun"
)

// runTestRun executes the copied internal test files in one minigo-test
// invocation. The scratch GOROOT is passed as resolver state rather than
// through the process environment, and each test gets an isolated engine
// session so package-level mutation cannot change a later test's oracle.
func runTestRun(pkgDir, gr, srcList, only string, timeout time.Duration, report string) error {
	if err := renameSrcTests(pkgDir); err != nil {
		return err
	}
	modes := map[string]minigo.PackageMode{}
	for _, path := range strings.Split(srcList, ",") {
		if path != "" {
			modes[path] = minigo.ModeSource
		}
	}
	out, reportFile, err := testRunOutput(report)
	if err != nil {
		return err
	}
	err = testrun.Run(context.Background(), testrun.Options{
		Dir:         pkgDir,
		Out:         out,
		Verbose:     true,
		Run:         only,
		TestTimeout: timeout,
		Isolate:     true,
		Modes:       modes,
		GOROOT:      gr,
	})
	err = finishTestRunOutput(reportFile, out, err)
	if err == testrun.ErrFailed {
		// tmpltests is a conformance report harness: like the process
		// runner, non-passing upstream tests are data in the report rather
		// than a failure of the harness itself.
		return nil
	}
	return err
}
