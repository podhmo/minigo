package main

func init() {
	helps["test"] = `usage: minigo test [dir] [flags]

Run the *_test.go files of the package directory dir (default: .),
the way 'go test -v' reports them: === RUN / --- PASS|FAIL|SKIP lines,
'ok  <pkg>' or 'FAIL <pkg>' trailers, exit 1 when a test fails,
panics, or hits an interpreter trap (SKIP alone does not fail the suite).

The directory builds as its test variant: the non-test files plus the
internal (same-package) *_test.go files form one package, and each
'func TestXxx(t *testing.T)' runs as its own call. A bound host
"testing" provides *testing.T, testing.Short and testing.Verbose; a
bound "flag" provides package-level flag constructors.

flags (accepted before or after dir):
  -v                stream === RUN / --- lines and test output
  -run pat          run only tests whose name matches the unanchored
                    regexp; "/" separates levels (like go test -run)
  -count n          run the suite n times (default 1)
  -timeout d        fail after a duration like 30s; timeout abandons unstarted cleanups too (including TempDir removal)
  -short            bound to testing.Short()
  -failfast         stop scheduling tests after the first failure
  --isolate         run each test in a fresh interpreter session
  --goroot dir      resolve standard-library source under dir
  --tagged tag      also select files needing this build tag, running only tests declared there
  --tags tag,...    build tags for file selection, like go build -tags
  --deny pkg,...    make importing these packages fail with a clear error
  --src pkg,...     interpret these packages from source even where a host-bound version exists
  -h, --help        show this help

Not supported in this version (reported loudly): external
'package foo_test' files, FuzzXxx, examples with an
// Output: comment, TestMain, t.Parallel scheduling, t.Setenv, t.Chdir.
`
}
