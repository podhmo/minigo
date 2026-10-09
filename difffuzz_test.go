package minigo_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

// runCaseTimeout bounds one regression run: a case that regresses into a
// HANG (e.g. a deadlock that stops firing) must fail the test, not stall
// the suite — a parked frame cannot be ctx-killed, so the run happens on
// a goroutine the test abandons on timeout.
const runCaseTimeout = 30 * time.Second

// TestDiffRegressions runs every case emitted by `tools/difffuzz gen -emit`
// (testdata/difffuzz/<slug>/{main.go,want.stdout}). want.stdout is the go
// toolchain's output, so a case needs no hand-written expectation.
//
// A want.err file pins an expected error outcome instead: a trap or fatal
// whose existence — not stdout — is the oracle (minigo's `runtime trap:`
// where gc compile-rejects, or `fatal error: ...` where both must die).
// The run must then fail with an error containing the file's trimmed
// contents; want.stdout still pins any output before the error.
//
// A case with a PENDING file is a known, unfixed divergence: it must still
// diverge. Once minigo matches go, the test fails asking to delete PENDING,
// so a fix is never left unpinned.
func TestDiffRegressions(t *testing.T) {
	dirs, err := filepath.Glob("testdata/difffuzz/*/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range dirs {
		dir := filepath.Dir(f)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatal(err)
			}
			// want.err holds the expected error text; an absent runErr
			// or a non-matching one both fail below.
			wantErrText, err := os.ReadFile(filepath.Join(dir, "want.err"))
			expectErr := err == nil
			_, statErr := os.Stat(filepath.Join(dir, "PENDING"))
			pending := statErr == nil
			// A case with a want.stderr file also compares stderr (e.g.
			// builtin print/println output). Builtins write to os.Stderr
			// directly, so the run happens with os.Stderr redirected to
			// a pipe; subtests run serially, so the swap is safe.
			wantStderr, err := os.ReadFile(filepath.Join(dir, "want.stderr"))
			captureErr := err == nil

			type outcome struct {
				stdout string
				stderr string
				runErr error
			}
			resCh := make(chan outcome, 1)
			// stderrOld is set before the os.Stderr swap so the timeout
			// path can restore it when the run goroutine is abandoned.
			var stderrOld *os.File
			go func() {
				var buf bytes.Buffer
				opts := []minigo.Option{minigo.WithOutput(&buf)}
				// A SRC file lists packages to interpret from source (the
				// CLI's --src), one per line — for fixes on the source path
				// of a package that also has a host binding.
				if src, err := os.ReadFile(filepath.Join(dir, "SRC")); err == nil {
					modes := map[string]minigo.PackageMode{}
					for _, p := range strings.Fields(string(src)) {
						modes[p] = minigo.ModeSource
					}
					opts = append(opts, minigo.WithPackageModes(modes))
				}
				e := minigo.NewEngine(".", opts...)

				var runErr error
				var gotErr string
				if captureErr {
					r, w, err := os.Pipe()
					if err != nil {
						resCh <- outcome{runErr: err}
						return
					}
					old := os.Stderr
					stderrOld = old
					os.Stderr = w
					done := make(chan string)
					go func() {
						b, _ := io.ReadAll(r)
						done <- string(b)
					}()
					_, runErr = e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
					w.Close()
					gotErr = <-done
					os.Stderr = old
					r.Close()
				} else {
					_, runErr = e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
				}
				resCh <- outcome{stdout: buf.String(), stderr: gotErr, runErr: runErr}
			}()

			var res outcome
			select {
			case res = <-resCh:
			case <-time.After(runCaseTimeout):
				if stderrOld != nil {
					os.Stderr = stderrOld
				}
				t.Fatalf("run did not finish within %s (HANG regression?)", runCaseTimeout)
			}

			diff := cmp.Diff(string(want), res.stdout)
			if captureErr && diff == "" {
				diff = cmp.Diff(string(wantStderr), res.stderr)
			}
			errMatch := expectErr && res.runErr != nil &&
				strings.Contains(res.runErr.Error(), strings.TrimSpace(string(wantErrText)))
			matched := diff == "" && (expectErr == (res.runErr != nil)) && (!expectErr || errMatch)

			switch {
			case pending && matched:
				t.Errorf("%s now matches go: delete %s/PENDING to pin the fix", dir, dir)
			case pending:
				t.Skipf("known divergence (see %s/PENDING)", dir)
			case expectErr && res.runErr == nil:
				t.Errorf("expected error containing %q, but the run succeeded", strings.TrimSpace(string(wantErrText)))
			case expectErr && !errMatch:
				t.Errorf("run error differs from %s/want.err (-want +got):\n%s", dir, cmp.Diff(strings.TrimSpace(string(wantErrText)), res.runErr.Error()))
			case diff != "":
				t.Errorf("output differs from go (-go +minigo):\n%s", diff)
			case matched:
				// the pinned outcome holds — nothing to report
			case res.runErr != nil:
				t.Errorf("run: %v", res.runErr)
			}
		})
	}
}
