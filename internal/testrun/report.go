package testrun

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/podhmo/minigo/internal/testrun/mtesting"
)

// indent is go test's one-level output indent — four spaces aligning
// log lines with the status dashes of their test's --- line.
const indent = "    "

// report renders the `go test -v` stream: === RUN / --- PASS lines in
// -v mode (plus eager log lines through emitLine), and only the
// buffered output of failing tests in quiet mode.
type report struct {
	w       io.Writer
	verbose bool
}

func newReport(w io.Writer, verbose bool) *report {
	return &report{w: w, verbose: verbose}
}

// emitLine is the suite's chatty sink: the facade hands it
// already-formatted lines (=== RUN, === ATTR, eager log lines with
// their indent).
func (r *report) emitLine(s string) {
	fmt.Fprintln(r.w, s)
}

// note writes a runner diagnostic ("minigo test: ...") unconditionally.
func (r *report) note(format string, args ...any) {
	fmt.Fprintf(r.w, "minigo test: "+format+"\n", args...)
}

// testDone renders a finished top-level test's buffered block. In -v
// mode every test renders; quietly only non-PASS verdicts print (and a
// skipped test prints nothing, like go test).
func (r *report) testDone(t *mtesting.T) {
	if r.verbose || mtesting.FailedVerdict(t.Verdict()) {
		r.block(t, 0)
	}
}

// block writes "--- V: name (dur)" then the test's buffered events:
// log lines (quiet mode only — verbose streamed them eagerly) and
// completed subtest blocks indented one level deeper.
func (r *report) block(t *mtesting.T, depth int) {
	pad := strings.Repeat(indent, depth)
	fmt.Fprintf(r.w, "%s--- %s: %s (%s)\n", pad, t.Verdict(), t.Name(), fmtDur(t.Elapsed()))
	for _, e := range t.Events() {
		switch {
		case e.Child != nil:
			// quiet mode renders only failed children — a passing or
			// skipped subtest's buffer is dropped, as go test drops it.
			if r.verbose || mtesting.FailedVerdict(e.Child.Verdict()) {
				r.block(e.Child, depth+1)
			}
		case e.Log != "" && !r.verbose:
			for _, line := range strings.Split(e.Log, "\n") {
				fmt.Fprintf(r.w, "%s%s\n", pad+indent, line)
			}
		}
	}
	// a PANIC/TRAP detail closes the block — the failure ended the test.
	if detail := t.VerdictErr(); detail != "" {
		for _, line := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
			fmt.Fprintf(r.w, "%s%s\n", pad+indent, line)
		}
	}
}

// excludedByTag prints the -v marker for a top-level test skipped by
// the --tagged gate — go test -v shows skipped tests the same way.
func (r *report) excludedByTag(name, tag string) {
	if !r.verbose {
		return
	}
	fmt.Fprintf(r.w, "=== RUN   %s\n", name)
	fmt.Fprintf(r.w, "%sminigo test: excluded from the run set by --tagged %s\n", indent, tag)
	fmt.Fprintf(r.w, "--- SKIP: %s (0.00s)\n", name)
}

// noTests prints go's "no tests to run" warning.
func (r *report) noTests() {
	fmt.Fprintln(r.w, "testing: warning: no tests to run")
}

// timedOut prints the -timeout expiry marker.
func (r *report) timedOut(d time.Duration) {
	fmt.Fprintf(r.w, "minigo test: test timed out after %s\n", d)
}

// finish writes the test-binary trailer (PASS or FAIL) and the
// go-test tool's package line ("ok" / "FAIL\tpkg\tdur").
func (r *report) finish(importPath string, failed bool, elapsed time.Duration) {
	if failed {
		fmt.Fprintln(r.w, "FAIL")
		fmt.Fprintf(r.w, "FAIL\t%s\t%s\n", importPath, fmtDur(elapsed))
		return
	}
	fmt.Fprintln(r.w, "PASS")
	fmt.Fprintf(r.w, "ok  \t%s\t%s\n", importPath, fmtDur(elapsed))
}

// buildFailed reports a package that never built/initialized —
// the go test "FAIL pkg [build failed]" shape; kind is
// "setup failed" for locate errors, "build failed" for compile ones.
func (r *report) buildFailed(importPath string, elapsed time.Duration, kind string) {
	fmt.Fprintln(r.w, "FAIL")
	fmt.Fprintf(r.w, "FAIL\t%s\t%s [%s]\n", importPath, fmtDur(elapsed), kind)
}

func fmtDur(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}
