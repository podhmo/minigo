package minigo_test

import (
	"testing"
)

// TestInspectBody exercises the experimental body view: a script-side
// analyzer (testdata/inspectbody) walks the compiled op list
// (inspect.Ops — the "special VM code" tracking call arguments and
// return values) and infers net/http request parameters from handler
// bodies in testdata/httppkg — including reads hidden inside
// same-package and cross-package helper functions.
func TestInspectBody(t *testing.T) {
	e := newEngine(t)
	if got := run(t, e, "./testdata/inspectbody", "BodySmoke"); got != "ok" {
		t.Fatalf("BodySmoke: %v", got)
	}
	if got := run(t, e, "./testdata/inspectbody", "OpsSmoke"); got != "ok" {
		t.Fatalf("OpsSmoke: %v", got)
	}

	want := `handler CreateUserH POST /users
  body age int
  body name string
  form note string
handler GET /ping (func literal)
  query ping string
handler GetItem GET /items/{iid}
  query v string
handler GetUser GET /users/{id}
  cookie sess string
  header X-Token string
  path id string
  query dbg bool
  query filter string
  query n int64
  query page string
  query q string
  query size string
  query trace string
  query via string
handler decode
  body age int
  body name string
handler local
  form note string
handler parseFilter
  cookie sess string
  query filter string
handler pass`
	if got := run(t, e, "./testdata/inspectbody", "Report"); got != want {
		t.Errorf("Report mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
