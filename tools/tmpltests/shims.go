package main

// shims holds the source of the GOROOT packages the test files import that
// minigo cannot run from the real sources (testing's machinery lives in
// runtime internals; flag/iter pull in large dependency trees). The shim
// surface is whatever the test files under -tests actually use: reporting,
// subtests, skip, and Short.
var shims = map[string]string{
	"testing": `package testing

import (
	"fmt"
	"time"
)

// failNow aborts the current test via panic (t.Fatal/FailNow); skipNow is
// the t.Skip equivalent. IsSkip/IsFailNow let the generated driver tell
// them apart from a real script panic.
type failNow struct{ msg string }
type skipNow struct{ msg string }

type T struct {
	name     string
	failed   bool
	logs     []string
	cleanups []func()
	root     *T // parent for subtests: failures propagate to it
}

// NewT is the driver's entry point (go test makes T itself).
func NewT(name string) *T { return &T{name: name} }

// markFailed flags t and every ancestor, like testing.T's propagation
// (a failing grandchild subtest still fails the root test).
func (t *T) markFailed() {
	for r := t; r != nil; r = r.root {
		r.failed = true
	}
}

func (t *T) fail(msg string) {
	t.markFailed()
	t.logs = append(t.logs, msg)
	for r := t.root; r != nil; r = r.root {
		r.logs = append(r.logs, t.name+": "+msg)
	}
}

func (t *T) Helper()      {}
func (t *T) Name() string { return t.name }
func (t *T) Failed() bool { return t.failed }

func (t *T) FirstError() string {
	if len(t.logs) > 0 {
		return t.logs[0]
	}
	return ""
}

func (t *T) Error(args ...any)              { t.fail(fmt.Sprint(args...)) }
func (t *T) Errorf(f string, a ...any)      { t.fail(fmt.Sprintf(f, a...)) }
func (t *T) Fatal(args ...any)              { t.fail(fmt.Sprint(args...)); panic(failNow{}) }
func (t *T) Fatalf(f string, a ...any)      { t.fail(fmt.Sprintf(f, a...)); panic(failNow{}) }
func (t *T) FailNow()                       { t.markFailed(); panic(failNow{}) }
func (t *T) Log(args ...any)                {}
func (t *T) Logf(f string, a ...any)        {}
func (t *T) Skip(args ...any)               { panic(skipNow{fmt.Sprint(args...)}) }
func (t *T) Skipf(f string, a ...any)       { panic(skipNow{fmt.Sprintf(f, a...)}) }
func (t *T) SkipNow()                       { panic(skipNow{}) }
func (t *T) Parallel()                      {}
func (t *T) Cleanup(f func())               { t.cleanups = append(t.cleanups, f) }
func (t *T) TempDir() string                { return "/tmp" }
func (t *T) Deadline() (time.Time, bool) { return time.Time{}, false }

// RunCleanups runs Cleanup callbacks LIFO, like testing.T does after the
// test function returns (the generated driver calls it).
func (t *T) RunCleanups() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}

func (t *T) Run(name string, f func(*T)) bool {
	sub := &T{name: t.name + "/" + name, root: t}
	defer sub.RunCleanups()
	func() {
		defer func() {
			r := recover()
			switch r.(type) {
			case failNow, skipNow:
			case nil:
			default:
				panic(r)
			}
		}()
		f(sub)
	}()
	return !sub.failed
}

func Short() bool { return false }

func IsSkip(r any) (string, bool) {
	if s, ok := r.(skipNow); ok {
		return s.msg, true
	}
	return "", false
}

func IsFailNow(r any) bool {
	_, ok := r.(failNow)
	return ok
}
`,
	"flag": `package flag

// The tests only declare vars through these; parsing is a no-op.
func Bool(name string, def bool, usage string) *bool { return &def }
func String(name, def, usage string) *string         { return &def }
func Int(name string, def int, usage string) *int    { return &def }
func Parse()                                         {}
func Args() []string                                 { return nil }
`,
	"iter": `package iter

type Seq[V any] func(yield func(V) bool)
type Seq2[K, V any] func(yield func(K, V) bool)
`,
}
