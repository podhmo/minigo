// Package mtesting is the host-side facade for `minigo test`: it
// implements the script-visible *testing.T surface plus the runner
// state the bound "testing" package reads (testing.Short, t.Deadline).
// The runner binds it as the import path "testing"; it is NOT part of
// the default stdlib table, so `minigo run` scripts cannot import it.
package mtesting

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podhmo/minigo/runtime"
)

// Verdict is a finished test's classification: PASS and SKIP are
// script-controlled outcomes, FAIL marks t.Error/t.Fatal use, PANIC an
// unrecovered script panic, and TRAP a minigo limitation (an error the
// VM cannot express — never recoverable by the script).
type Verdict int

const (
	Pass Verdict = iota
	Skip
	Fail
	Trap
	Panic
)

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "PASS"
	case Skip:
		return "SKIP"
	case Fail:
		return "FAIL"
	case Trap:
		return "TRAP"
	case Panic:
		return "PANIC"
	}
	return "?"
}

// FailedVerdict reports whether v counts as a failure: SKIP does not
// propagate to a parent test's failure, the rest do.
func FailedVerdict(v Verdict) bool { return v == Fail || v == Trap || v == Panic }

// Event is one buffered report item of a finished test: a decorated log
// line, or a completed subtest whose own block nests inside this one.
type Event struct {
	Log   string // "file:line: message" (may span lines)
	Child *T
}

// Suite is the runner state the facade shares: flag values bound
// functions answer, and the output sink chatty (-v) lines stream into.
// The runner fills it once per `minigo test` invocation.
type Suite struct {
	Short    bool
	Verbose  bool
	FailFast bool
	// halt is the shared -failfast latch. Subtests consult it before
	// each nested Run, so a failure in one sibling stops later sibling
	// subtests instead of only later top-level tests.
	halt atomic.Bool
	// Emit writes one already-formatted runner line (=== RUN, === ATTR,
	// eager log lines in -v mode). Nil discards — the non-verbose report
	// buffers everything and never calls it.
	Emit func(line string)
	// Ctx is the suite context (with the -timeout deadline when set);
	// t.Context returns it.
	Ctx context.Context
	// Deadline is the suite's -timeout deadline for t.Deadline; zero
	// when -timeout was not given.
	Deadline time.Time
	// Match is the -run pattern, one unanchored regexp per name level
	// ("" means every test runs). Level deeper than len(Match) matches
	// unconditionally, like go test.
	Match []*regexp.Regexp
}

// Halt records a failure for -failfast; Stop reports whether further
// tests or subtests must not be scheduled.
func (s *Suite) Halt()      { s.halt.Store(true) }
func (s *Suite) Stop() bool { return s != nil && s.halt.Load() }

// MatchFullName reports whether a slash-separated full test name
// matches the -run filter. Each pattern level applies to the same
// slash level of the full name; deeper levels than the pattern match
// unconditionally, like go test. Matching the full name keeps slashes
// inside a subtest name addressable (TestZones/America/New_York).
func (s *Suite) MatchFullName(name string) bool {
	parts := strings.Split(name, "/")
	for depth, re := range s.Match {
		if depth >= len(parts) {
			break
		}
		if !re.MatchString(parts[depth]) {
			return false
		}
	}
	return true
}

// T is the script-visible *testing.T. The runner creates one per test
// (t.Run creates children), hands it to the test function as a GoValue,
// and reads the verdict and buffered events back after the call.
//
// Method calls dispatch through [runtime.ScriptMethoder]: t.Run and
// t.Cleanup need raw script values (the callback func and a VMCaller
// to invoke it), so the reflected method set is only a method-set
// marker for `testing.TB`-typed helper parameters — every exported
// method is answered by ScriptMethod first.
type T struct {
	suite  *Suite
	parent *T
	name   string // full name chain: "TestX/sub/deep"
	pos    string // file:line of the test function's declaration
	depth  int

	// mu guards the mutable test state. Script goroutines may call T
	// methods concurrently even though top-level tests are scheduled
	// serially; callbacks are invoked only after releasing it.
	mu       sync.Mutex
	failed   bool
	skipped  bool
	finished bool
	parallel bool

	ctx    context.Context
	cancel context.CancelFunc

	events   []Event
	children []*T
	used     map[string]int // child name dedup: "a#01" suffixes
	cleanups []any          // func() (host) or runtime.Value (script fn)

	verdict    Verdict
	verdictErr string // detail line for PANIC/TRAP verdicts
	elapsed    time.Duration
}

// NewT creates the top-level test value for one TestXxx run: name is
// the leaf name (or a slash-chain when a -count round names it), pos
// its declaration's file:line.
func NewT(s *Suite, name, pos string) *T {
	parent := context.Background()
	if s != nil && s.Ctx != nil {
		parent = s.Ctx
	}
	ctx, cancel := context.WithCancel(parent)
	return &T{suite: s, name: name, pos: pos, used: map[string]int{}, ctx: ctx, cancel: cancel}
}

// newSub creates a child test with a context canceled before its own
// cleanups, like testing.T.Context.
func newSub(t *T, name string) *T {
	ctx, cancel := context.WithCancel(t.Context())
	return &T{suite: t.suite, parent: t, name: name, pos: t.pos, depth: t.depth + 1, used: map[string]int{}, ctx: ctx, cancel: cancel}
}

// cancelContext implements testing.T.Context's contract: the test's
// context is canceled before its cleanups run.
func (t *T) cancelContext() {
	if t.cancel != nil {
		t.cancel()
	}
}

// CancelContext cancels the test context before the runner invokes
// cleanups. It is exported for the runner; scripts only see Context.
func (t *T) CancelContext() { t.cancelContext() }

func (t *T) markFailed() {
	t.mu.Lock()
	t.failed = true
	t.mu.Unlock()
}

func (t *T) markSkipped() {
	t.mu.Lock()
	t.skipped = true
	t.mu.Unlock()
}

func (t *T) addCleanup(f any) {
	t.mu.Lock()
	t.cleanups = append(t.cleanups, f)
	t.mu.Unlock()
}

// popCleanup removes the innermost registered cleanup. Popping rather
// than indexing a fixed length lets a cleanup register another cleanup;
// that later registration is the next one run.
func (t *T) popCleanup() (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.cleanups)
	if n == 0 {
		return nil, false
	}
	c := t.cleanups[n-1]
	t.cleanups[n-1] = nil
	t.cleanups = t.cleanups[:n-1]
	return c, true
}

// CleanupCount reports how many cleanup callbacks remain. The runner uses
// the count so checking progress does not copy the callback slice once per
// cleanup.
func (t *T) CleanupCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cleanups)
}

func (t *T) appendEvent(e Event) {
	t.mu.Lock()
	t.events = append(t.events, e)
	t.mu.Unlock()
}

func (t *T) appendChild(sub *T) {
	t.mu.Lock()
	t.children = append(t.children, sub)
	t.mu.Unlock()
}

func (t *T) setParallel() {
	t.mu.Lock()
	t.parallel = true
	t.mu.Unlock()
}

func (t *T) setFinished() {
	t.mu.Lock()
	t.finished = true
	t.mu.Unlock()
}

func (t *T) isFinished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finished
}

// --- plain Go methods ---
//
// Script call sites never reach these — ScriptMethod answers first —
// so they exist for the reflect-driven method-set queries behind
// `testing.TB`-typed parameters (MethodSetOf on a GoValue reads the
// real method set). Their semantics are the same helpers where a
// VMCaller is not required; FailNow/SkipNow panic a hand-built Trap
// carrying the same sentinel, which asScriptPanic passes through
// unchanged, so even this unreachable path keeps Goexit semantics.

func (t *T) trapStop(skip bool) *runtime.Trap {
	if skip {
		t.markSkipped()
		return &runtime.Trap{Reason: "test skipped", Err: &stopError{t: t, skip: true}}
	}
	t.markFailed()
	return &runtime.Trap{Reason: "test failed", Err: &stopError{t: t}}
}

func (t *T) logGo(args []any, fail bool) {
	if fail {
		t.markFailed()
	}
	t.logMsg(strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
}

// Log records its args like fmt.Sprintln.
func (t *T) Log(args ...any) { t.logGo(args, false) }

// Logf records a formatted message.
func (t *T) Logf(format string, args ...any) { t.logMsg(fmt.Sprintf(format, args...)) }

// Error is Log plus a test failure.
func (t *T) Error(args ...any) { t.logGo(args, true) }

// Errorf is Logf plus a test failure.
func (t *T) Errorf(format string, args ...any) {
	t.markFailed()
	t.logMsg(fmt.Sprintf(format, args...))
}

// Fail marks the test failed and lets it continue.
func (t *T) Fail() { t.markFailed() }

// FailNow marks the test failed and stops its execution.
func (t *T) FailNow() { panic(t.trapStop(false)) }

// Fatal is Error plus FailNow.
func (t *T) Fatal(args ...any) {
	t.logGo(args, true)
	panic(t.trapStop(false))
}

// Fatalf is Errorf plus FailNow.
func (t *T) Fatalf(format string, args ...any) {
	t.markFailed()
	t.logMsg(fmt.Sprintf(format, args...))
	panic(t.trapStop(false))
}

// Skip is Log plus SkipNow.
func (t *T) Skip(args ...any) {
	t.logMsg(strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
	panic(t.trapStop(true))
}

// Skipf is Logf plus SkipNow.
func (t *T) Skipf(format string, args ...any) {
	t.logMsg(fmt.Sprintf(format, args...))
	panic(t.trapStop(true))
}

// SkipNow marks the test skipped and stops its execution.
func (t *T) SkipNow() { panic(t.trapStop(true)) }

// Helper is a no-op in v1 (log lines decorate the test's own
// declaration site — there is no frame for Helper to hide).
func (t *T) Helper() {}

// Cleanup registers f to run at test end (LIFO). The ScriptMethod
// path stores script func values; this reflect path — reachable only
// through method-set queries, never a script call — stores the host
// func as is.
func (t *T) Cleanup(f func()) { t.addCleanup(f) }

// TempDir creates a temporary directory removed at test end.
func (t *T) TempDir() string {
	dir, err := os.MkdirTemp("", "minigo-test-*")
	if err != nil {
		panic(&runtime.Trap{Reason: "t.TempDir: " + err.Error(), Err: err})
	}
	t.addCleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Run runs f as a subtest. This reflect-path variant exists only to
// complete the method set — ScriptMethod answers real calls.
func (t *T) Run(name string, f func(*T)) bool {
	sub := newSub(t, t.childName(name))
	t.appendChild(sub)
	if t.suite != nil && t.suite.Verbose && t.suite.Emit != nil {
		t.suite.Emit("=== RUN   " + sub.name)
	}
	start := time.Now()
	var rerr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				rerr = panicErr(r)
			}
		}()
		f(sub)
	}()
	sub.SetElapsed(time.Since(start))
	if rerr != nil {
		sub.SetVerdict(Panic, rerr.Error())
		t.markFailed()
	}
	sub.setFinished()
	t.appendEvent(Event{Child: sub})
	return !FailedVerdict(sub.Verdict())
}

// Deadline reports the suite's -timeout deadline.
func (t *T) Deadline() (time.Time, bool) {
	if t.suite == nil || t.suite.Deadline.IsZero() {
		return time.Time{}, false
	}
	return t.suite.Deadline, true
}

// Context returns the suite's context.
func (t *T) Context() context.Context {
	if t.ctx != nil {
		return t.ctx
	}
	return context.Background()
}

// Parallel is a no-op: the runner is serial.
func (t *T) Parallel() { t.setParallel() }

// Setenv is not supported (host process state).
func (t *T) Setenv(key, value string) {
	panic(&runtime.Trap{Reason: "minigo test: t.Setenv is not supported (mutating host process state)"})
}

// Chdir is not supported (host process state).
func (t *T) Chdir(dir string) {
	panic(&runtime.Trap{Reason: "minigo test: t.Chdir is not supported (mutating host process state)"})
}

// Attr emits an === ATTR line in -v mode.
func (t *T) Attr(key, value string) {
	if t.suite != nil && t.suite.Verbose && t.suite.Emit != nil {
		t.suite.Emit(fmt.Sprintf("=== ATTR   %s %s %s", t.name, key, value))
	}
}

// Output returns a writer that lands in this test's log buffer.
func (t *T) Output() io.Writer { return outputWriter{t: t} }

// ArtifactDir creates a directory kept after the test for artifacts.
func (t *T) ArtifactDir() string {
	dir, err := os.MkdirTemp("", "minigo-artifacts-*")
	if err != nil {
		panic(&runtime.Trap{Reason: "t.ArtifactDir: " + err.Error(), Err: err})
	}
	return dir
}

// panicErr normalizes a recovered value to error.
func panicErr(r any) error {
	if e, ok := r.(error); ok {
		return e
	}
	return fmt.Errorf("panic: %v", r)
}

// --- read accessors the runner's report uses ---

// Name returns the full test name ("TestX/sub").
func (t *T) Name() string { return t.name }

// Pos returns the test function's declaration position, "file:line".
// v1 decorates log lines with the declaration site — the call-site
// decoration go test produces needs caller-position plumbing the VM
// does not have yet (see TODO).
func (t *T) Pos() string { return t.pos }

// Depth returns the nesting level (top-level tests are 0).
func (t *T) Depth() int { return t.depth }

// Events returns the buffered report items in order.
func (t *T) Events() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Event(nil), t.events...)
}

// Verdict is the finished test's classification.
func (t *T) Verdict() Verdict {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.verdict
}

// VerdictErr is the detail text for PANIC/TRAP verdicts ("" otherwise).
func (t *T) VerdictErr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.verdictErr
}

// SetVerdict records the finished classification.
func (t *T) SetVerdict(v Verdict, detail string) {
	t.mu.Lock()
	t.verdict, t.verdictErr = v, detail
	t.mu.Unlock()
}

// Elapsed is the measured run time.
func (t *T) Elapsed() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.elapsed
}

// SetElapsed records the measured run time.
func (t *T) SetElapsed(d time.Duration) {
	t.mu.Lock()
	t.elapsed = d
	t.mu.Unlock()
}

// Finish marks the test done: post-completion log calls trap, like go's
// "Log in goroutine after test has completed" — here the call returns
// a plain error (a trap for the script) instead of a host panic.
func (t *T) Finish() { t.setFinished() }

// Failed reports whether the test (or one of its subtests) failed.
func (t *T) Failed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failed
}

// Skipped reports whether the test was skipped.
func (t *T) Skipped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.skipped
}

// MarkFailed is how a finished subtest reports back to its parent.
func (t *T) MarkFailed() { t.markFailed() }

// AddCleanup registers f to run when the test finishes (LIFO). f is
// either a script function value or a host func().
func (t *T) AddCleanup(f any) { t.addCleanup(f) }

// AppendEvent records a subtest's buffered block or a stray host log
// line under this test.
func (t *T) AppendEvent(e Event) { t.appendEvent(e) }

// LogLine appends one decorated log line ("pos: msg"); in -v mode it
// also streams eagerly through the suite's sink — always a single
// indent: only the buffered --- lines nest per level in go test.
func (t *T) LogLine(msg string) {
	line := t.pos + ": " + msg
	t.appendEvent(Event{Log: line})
	if t.suite.Verbose && t.suite.Emit != nil {
		t.suite.Emit("    " + line)
	}
}

// Suite returns the shared runner state (used by the runner's cleanup
// builtin for the emit path).
func (t *T) Suite() *Suite { return t.suite }

// Parent returns the parent test of a subtest (nil at top level).
func (t *T) Parent() *T { return t.parent }

// --- classification ---

// stopError unwinds a test function like runtime.Goexit: it is not a
// script panic, so recover() must not see it — the builtin's error
// channel carries it up as a *runtime.Trap (unrecoverable). It names
// the T whose FailNow/SkipNow fired it, so a nested t.Run knows
// whether to swallow it (its own child) or re-raise (an ancestor's).
type stopError struct {
	t    *T
	skip bool
}

// DrainDefers marks the sentinel as runtime.Goexit-like control flow:
// the VM drains the frame's remaining defers before returning it.
func (e *stopError) DrainDefers() {}

func (e *stopError) Error() string {
	if e.skip {
		return "test skipped"
	}
	return "test failed"
}

// StopOf unwraps err for the sentinel a FailNow/SkipNow built. A nil
// result means err is an ordinary panic/trap — use errors.As(err, &se)
// on this type through the *runtime.Trap the VM wraps it in.
func StopOf(err error) (*T, bool) {
	var se *stopError
	for err != nil {
		if s, ok := err.(*stopError); ok {
			se = s
			break
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	if se == nil {
		return nil, false
	}
	return se.t, true
}

// --- script-callable surface (ScriptMethoder) ---

// ScriptMethod implements [runtime.ScriptMethoder]: every method call
// on *T arrives here with raw script arguments.
func (t *T) ScriptMethod(name string) (func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), bool) {
	switch name {
	case "Log":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.logMsg(sprintArgs(args))
			return runtime.NIL, nil
		}, true
	case "Logf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.logMsg(s)
			return runtime.NIL, nil
		}, true
	case "Error":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.markFailed()
			t.logMsg(sprintArgs(args))
			return runtime.NIL, nil
		}, true
	case "Errorf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.markFailed()
			t.logMsg(s)
			return runtime.NIL, nil
		}, true
	case "Fail":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.markFailed()
			return runtime.NIL, nil
		}, true
	case "FailNow":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.markFailed()
			return nil, &stopError{t: t}
		}, true
	case "Fatal":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.markFailed()
			t.logMsg(sprintArgs(args))
			return nil, &stopError{t: t}
		}, true
	case "Fatalf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.markFailed()
			t.logMsg(s)
			return nil, &stopError{t: t}
		}, true
	case "Skip":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.markSkipped()
			t.logMsg(sprintArgs(args))
			return nil, &stopError{t: t, skip: true}
		}, true
	case "Skipf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.markSkipped()
			t.logMsg(s)
			return nil, &stopError{t: t, skip: true}
		}, true
	case "SkipNow":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.markSkipped()
			return nil, &stopError{t: t, skip: true}
		}, true
	case "Failed":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return t.Failed(), nil
		}, true
	case "Skipped":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return t.Skipped(), nil
		}, true
	case "Name":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return t.name, nil
		}, true
	case "Helper":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			// v1: log lines decorate with the test's declaration site
			// regardless, so there is no frame for Helper to skip.
			return runtime.NIL, nil
		}, true
	case "Cleanup":
		return func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("t.Cleanup: got %d argument(s), want 1", len(args))
			}
			if err := checkCallable(args[0]); err != nil {
				return nil, fmt.Errorf("t.Cleanup: %w", err)
			}
			t.addCleanup(args[0])
			return runtime.NIL, nil
		}, true
	case "TempDir":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			dir, err := os.MkdirTemp("", "minigo-test-*")
			if err != nil {
				return nil, err
			}
			t.addCleanup(func() { _ = os.RemoveAll(dir) })
			return dir, nil
		}, true
	case "Run":
		return func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("t.Run: got %d argument(s), want 2", len(args))
			}
			name := argString(args[0])
			if err := checkCallable(args[1]); err != nil {
				return nil, fmt.Errorf("t.Run: %w", err)
			}
			return t.run(vc, name, args[1])
		}, true
	case "Deadline":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			if t.suite.Deadline.IsZero() {
				// A boxed zero Time — scripts can still call
				// d.IsZero() like they would on a real time.Time.
				return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: time.Time{}}, false}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: t.suite.Deadline}, true}}, nil
		}, true
	case "Context":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			ctx := t.suite.Ctx
			if ctx == nil {
				ctx = context.Background()
			}
			return &runtime.GoValue{V: ctx}, nil
		}, true
	case "Parallel":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			// serial runner: recorded so a diagnostic can report it,
			// but no scheduling pauses exist to honor.
			t.setParallel()
			return runtime.NIL, nil
		}, true
	case "Setenv":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("minigo test: t.Setenv is not supported (mutating host process state)")
		}, true
	case "Chdir":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("minigo test: t.Chdir is not supported (mutating host process state)")
		}, true
	case "Attr":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if t.suite.Verbose && t.suite.Emit != nil && len(args) == 2 {
				t.suite.Emit(fmt.Sprintf("=== ATTR   %s %s %s", t.name, argString(args[0]), argString(args[1])))
			}
			return runtime.NIL, nil
		}, true
	case "Output":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return &runtime.GoValue{V: outputWriter{t: t}}, nil
		}, true
	case "ArtifactDir":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			dir, err := os.MkdirTemp("", "minigo-artifacts-*")
			if err != nil {
				return nil, err
			}
			return dir, nil
		}, true
	}
	return nil, false
}

// logMsg appends one log entry; logging after Finish is the go test
// "Log in goroutine after test has completed" error.
func (t *T) logMsg(msg string) {
	if t.isFinished() {
		// post-finish logs have nowhere to go: record under the test
		// anyway so the suite keeps them visible rather than dropping.
		t.appendEvent(Event{Log: t.pos + ": " + msg})
		return
	}
	t.LogLine(msg)
}
