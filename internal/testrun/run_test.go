package testrun_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/internal/testrun"
)

// run executes `minigo test` on a testdata fixture and returns the
// report text with volatile durations normalized to 0.00s.
func run(t *testing.T, dir string, mutate func(*testrun.Options)) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	o := testrun.Options{Dir: dir, Out: &buf, Count: 1}
	if mutate != nil {
		mutate(&o)
	}
	err := testrun.Run(context.Background(), o)
	return normalizeDur(buf.String()), err
}

var durRe = regexp.MustCompile(`\d+\.\d+s`)

func normalizeDur(s string) string {
	return durRe.ReplaceAllString(s, "0.00s")
}

func TestPass(t *testing.T) {
	got, err := run(t, "testdata/pass", func(o *testrun.Options) { o.Verbose = true })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `=== RUN   TestAdd
--- PASS: TestAdd (0.00s)
=== RUN   TestLog
    pass_test.go:11: greeting: hello
--- PASS: TestLog (0.00s)
=== RUN   TestSkip
    pass_test.go:15: not today
--- SKIP: TestSkip (0.00s)
=== RUN   TestSub
=== RUN   TestSub/alpha
    pass_test.go:19: inside alpha
=== RUN   TestSub/beta
--- PASS: TestSub (0.00s)
    --- PASS: TestSub/alpha (0.00s)
    --- PASS: TestSub/beta (0.00s)
=== RUN   TestCleanup
    pass_test.go:30: body ran
    pass_test.go:30: cleanup ran
--- PASS: TestCleanup (0.00s)
PASS
ok  	github.com/podhmo/minigo/internal/testrun/testdata/pass	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestFail(t *testing.T) {
	got, err := run(t, "testdata/fail", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	want := `=== RUN   TestFail
    fail_test.go:5: this one fails
--- FAIL: TestFail (0.00s)
=== RUN   TestOK
--- PASS: TestOK (0.00s)
FAIL
FAIL	github.com/podhmo/minigo/internal/testrun/testdata/fail	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestFailQuiet(t *testing.T) {
	// without -v only the failing test's buffered block prints.
	got, err := run(t, "testdata/fail", nil)
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	want := `--- FAIL: TestFail (0.00s)
    fail_test.go:5: this one fails
FAIL
FAIL	github.com/podhmo/minigo/internal/testrun/testdata/fail	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestRunFilter(t *testing.T) {
	got, err := run(t, "testdata/pass", func(o *testrun.Options) {
		o.Verbose = true
		o.Run = "TestSub/beta"
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `=== RUN   TestSub
=== RUN   TestSub/beta
--- PASS: TestSub (0.00s)
    --- PASS: TestSub/beta (0.00s)
PASS
ok  	github.com/podhmo/minigo/internal/testrun/testdata/pass	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestRunFilterNoMatch(t *testing.T) {
	got, err := run(t, "testdata/pass", func(o *testrun.Options) {
		o.Verbose = true
		o.Run = "TestNothingMatches"
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(got, "no tests to run") {
		t.Errorf("want no-tests warning, got:\n%s", got)
	}
}

func TestCount(t *testing.T) {
	got, err := run(t, "testdata/pass", func(o *testrun.Options) {
		o.Verbose = true
		o.Run = "TestAdd"
		o.Count = 2
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `=== RUN   TestAdd
--- PASS: TestAdd (0.00s)
=== RUN   TestAdd
--- PASS: TestAdd (0.00s)
PASS
ok  	github.com/podhmo/minigo/internal/testrun/testdata/pass	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestTagged(t *testing.T) {
	got, err := run(t, "testdata/tagged", func(o *testrun.Options) {
		o.Verbose = true
		o.Tagged = "special"
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `=== RUN   TestGated
    special_test.go:7: only built and run under --tagged special
--- PASS: TestGated (0.00s)
=== RUN   TestAlways
    minigo test: excluded from the run set by --tagged special
--- SKIP: TestAlways (0.00s)
PASS
ok  	github.com/podhmo/minigo/internal/testrun/testdata/tagged	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestTaggedFileAbsent(t *testing.T) {
	// without --tagged/--tags the build-tag-gated file is not even
	// built — only the untagged test runs.
	got, err := run(t, "testdata/tagged", func(o *testrun.Options) { o.Verbose = true })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(got, "TestGated") {
		t.Errorf("gated test ran without its tag:\n%s", got)
	}
}

func TestUnsupported(t *testing.T) {
	got, err := run(t, "testdata/unsupport", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	for _, want := range []string{
		"benchmark: BenchmarkV is not supported",
		"fuzz target: FuzzV is not supported",
		"example: ExampleV is not supported",
		"TestBadShape has a signature minigo test does not run",
		"TestTwoParams has a signature minigo test does not run",
		"--- PASS: TestFine",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestExampleOutputFatal(t *testing.T) {
	got, err := run(t, "testdata/exampleout", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if !strings.Contains(got, "example: ExampleP is not supported") {
		t.Errorf("output missing example report:\n%s", got)
	}
	if strings.Contains(got, "=== RUN") {
		t.Errorf("tests ran despite a fatal output example:\n%s", got)
	}
}

func TestTestOnlyDir(t *testing.T) {
	got, err := run(t, "testdata/testonly", func(o *testrun.Options) { o.Verbose = true })
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	for _, want := range []string{
		"--- PASS: TestAlone",
		"ok  \tgithub.com/podhmo/minigo/internal/testrun/testdata/testonly",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestCgoSkipped(t *testing.T) {
	got, err := run(t, "testdata/cgotest", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if !strings.Contains(got, `imports "C"`) {
		t.Errorf("output missing cgo-skip report:\n%s", got)
	}
}

func TestTestMainFatal(t *testing.T) {
	got, err := run(t, "testdata/testmain", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if !strings.Contains(got, "TestMain is not supported") {
		t.Errorf("output missing TestMain report:\n%s", got)
	}
	if strings.Contains(got, "=== RUN") {
		t.Errorf("tests ran despite TestMain:\n%s", got)
	}
}

func TestExternalPackage(t *testing.T) {
	got, err := run(t, "testdata/external", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if !strings.Contains(got, "ext_test.go: external test package") {
		t.Errorf("output missing external-package report:\n%s", got)
	}
}

func TestPanic(t *testing.T) {
	got, err := run(t, "testdata/panic", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	for _, want := range []string{"--- PANIC: TestBoom", "panic: kaboom", "--- PASS: TestAfter", "FAIL\t"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFailFast(t *testing.T) {
	got, err := run(t, "testdata/ffast", func(o *testrun.Options) {
		o.Verbose = true
		o.FailFast = true
	})
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	want := `=== RUN   TestA
    ffast_test.go:5: first fails
--- FAIL: TestA (0.00s)
FAIL
FAIL	github.com/podhmo/minigo/internal/testrun/testdata/ffast	0.00s
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestNoTests(t *testing.T) {
	got, err := run(t, "testdata/notests", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(got, "testing: warning: no tests to run") || !strings.Contains(got, "ok  ") {
		t.Errorf("want no-tests warning + ok trailer, got:\n%s", got)
	}
}

func TestFlags(t *testing.T) {
	got, err := run(t, "testdata/flags", func(o *testrun.Options) { o.Verbose = true })
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	if strings.Contains(got, "FAIL") {
		t.Errorf("unexpected FAIL:\n%s", got)
	}
}

func TestShort(t *testing.T) {
	got, err := run(t, "testdata/flags", func(o *testrun.Options) {
		o.Verbose = true
		o.Short = true
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	if !strings.Contains(got, "--- SKIP: TestShort") {
		t.Errorf("TestShort did not honor -short:\n%s", got)
	}
}

func TestReviewVerdictSemantics(t *testing.T) {
	got, err := run(t, "testdata/reviewsem", func(o *testrun.Options) { o.Verbose = true })
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	for _, want := range []string{
		"--- FAIL: TestErrorThenSkip",
		"--- FAIL: TestCleanupError",
		"--- FAIL: TestSubCleanupError",
		"--- FAIL: TestSubCleanupError/child",
		"--- PASS: TestSkippedSubtestResult",
		"--- SKIP: TestSkippedSubtestResult/skip",
		"--- FAIL: TestDeferFatalDrainsDefers",
		"outer defer ran",
		"--- PANIC: TestCleanupPanicDrains",
		"--- PANIC: TestDeferSkipDoesNotHidePanic",
		"--- PANIC: TestCleanupSkipDoesNotHidePanic",
		"--- SKIP: TestRestoredOuterPanicDoesNotCancelSkip",
		"helper cleanup",
		"inner cleanup ran",
		"outer cleanup ran",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--- SKIP: TestErrorThenSkip") {
		t.Errorf("failure-then-skip was reported as skip:\n%s", got)
	}
}

func TestSlashInSubtestNameFilter(t *testing.T) {
	got, err := run(t, "testdata/reviewsem", func(o *testrun.Options) {
		o.Verbose = true
		o.Run = "TestZones/America/New_York"
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	for _, want := range []string{
		"=== RUN   TestZones/America/New_York",
		"--- PASS: TestZones",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestTaggedOnlyDirectory(t *testing.T) {
	got, err := run(t, "testdata/tagonly", func(o *testrun.Options) {
		o.Verbose = true
		o.Tagged = "special"
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	if !strings.Contains(got, "--- PASS: TestTaggedOnly") {
		t.Errorf("tagged-only test did not run:\n%s", got)
	}
}

func TestNestedFailFast(t *testing.T) {
	got, err := run(t, "testdata/ffastsub", func(o *testrun.Options) {
		o.Verbose = true
		o.FailFast = true
	})
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if strings.Contains(got, "TestNested/second") || strings.Contains(got, "second must not run") {
		t.Errorf("later sibling subtest ran under -failfast:\n%s", got)
	}
}

func TestHostBoundTargetPackageLoadsSource(t *testing.T) {
	got, err := run(t, "../../pkg/importmanager", nil)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, got)
	}
	if strings.Contains(got, "no index") || strings.Contains(got, "TRAP") || strings.Contains(got, "PANIC") {
		t.Errorf("host-bound target package was not source-loaded:\n%s", got)
	}
}

func TestBrokenNonTestFileIsNotTestOnly(t *testing.T) {
	got, err := run(t, "testdata/brokenmeta", nil)
	if !errors.Is(err, testrun.ErrFailed) {
		t.Fatalf("Run err = %v, want ErrFailed", err)
	}
	if !strings.Contains(got, "parsing //go:build line") {
		t.Errorf("broken non-test file was hidden by test-only synthesis:\n%s", got)
	}
	if strings.Contains(got, "=== RUN") {
		t.Errorf("test ran despite broken non-test file:\n%s", got)
	}
}
