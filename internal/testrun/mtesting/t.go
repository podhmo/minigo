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
	"time"
	"unicode"

	"github.com/podhmo/minigo/internal/hostbridge"
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
	Short   bool
	Verbose bool
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

// MatchName reports whether a test segment at depth matches the -run
// filter: deeper levels than the pattern's always match.
func (s *Suite) MatchName(depth int, name string) bool {
	if depth >= len(s.Match) {
		return true
	}
	return s.Match[depth].MatchString(name)
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

	failed   bool
	skipped  bool
	finished bool
	parallel bool

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
	return &T{suite: s, name: name, pos: pos, used: map[string]int{}}
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
		t.skipped = true
		return &runtime.Trap{Reason: "test skipped", Err: &stopError{t: t, skip: true}}
	}
	t.failed = true
	return &runtime.Trap{Reason: "test failed", Err: &stopError{t: t}}
}

func (t *T) logGo(args []any, fail bool) {
	if fail {
		t.failed = true
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
	t.failed = true
	t.logMsg(fmt.Sprintf(format, args...))
}

// Fail marks the test failed and lets it continue.
func (t *T) Fail() { t.failed = true }

// FailNow marks the test failed and stops its execution.
func (t *T) FailNow() { panic(t.trapStop(false)) }

// Fatal is Error plus FailNow.
func (t *T) Fatal(args ...any) {
	t.logGo(args, true)
	panic(t.trapStop(false))
}

// Fatalf is Errorf plus FailNow.
func (t *T) Fatalf(format string, args ...any) {
	t.failed = true
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
func (t *T) Cleanup(f func()) { t.cleanups = append(t.cleanups, f) }

// TempDir creates a temporary directory removed at test end.
func (t *T) TempDir() string {
	dir, err := os.MkdirTemp("", "minigo-test-*")
	if err != nil {
		panic(&runtime.Trap{Reason: "t.TempDir: " + err.Error(), Err: err})
	}
	t.cleanups = append(t.cleanups, func() { _ = os.RemoveAll(dir) })
	return dir
}

// Run runs f as a subtest. This reflect-path variant exists only to
// complete the method set — ScriptMethod answers real calls.
func (t *T) Run(name string, f func(*T)) bool {
	sub := &T{suite: t.suite, parent: t, name: t.childName(name), pos: t.pos, depth: t.depth + 1, used: map[string]int{}}
	t.children = append(t.children, sub)
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
	sub.elapsed = time.Since(start)
	if rerr != nil {
		sub.verdict, sub.verdictErr = Panic, rerr.Error()
		t.failed = true
	}
	sub.finished = true
	t.events = append(t.events, Event{Child: sub})
	return sub.verdict == Pass
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
	if t.suite == nil || t.suite.Ctx == nil {
		return context.Background()
	}
	return t.suite.Ctx
}

// Parallel is a no-op: the runner is serial.
func (t *T) Parallel() { t.parallel = true }

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
func (t *T) Events() []Event { return t.events }

// Verdict is the finished test's classification.
func (t *T) Verdict() Verdict { return t.verdict }

// VerdictErr is the detail text for PANIC/TRAP verdicts ("" otherwise).
func (t *T) VerdictErr() string { return t.verdictErr }

// SetVerdict records the finished classification.
func (t *T) SetVerdict(v Verdict, detail string) { t.verdict, t.verdictErr = v, detail }

// Elapsed is the measured run time.
func (t *T) Elapsed() time.Duration { return t.elapsed }

// SetElapsed records the measured run time.
func (t *T) SetElapsed(d time.Duration) { t.elapsed = d }

// Finish marks the test done: post-completion log calls trap, like go's
// "Log in goroutine after test has completed" — here the call returns
// a plain error (a trap for the script) instead of a host panic.
func (t *T) Finish() { t.finished = true }

// Failed reports whether the test (or one of its subtests) failed.
func (t *T) Failed() bool { return t.failed }

// Skipped reports whether the test was skipped.
func (t *T) Skipped() bool { return t.skipped }

// MarkFailed is how a finished subtest reports back to its parent.
func (t *T) MarkFailed() { t.failed = true }

// AddCleanup registers f to run when the test finishes (LIFO). f is
// either a script function value or a host func().
func (t *T) AddCleanup(f any) { t.cleanups = append(t.cleanups, f) }

// Cleanups returns the registered cleanups in registration order.
func (t *T) Cleanups() []any { return t.cleanups }

// AppendEvent records a subtest's buffered block or a stray host log
// line under this test.
func (t *T) AppendEvent(e Event) { t.events = append(t.events, e) }

// LogLine appends one decorated log line ("pos: msg"); in -v mode it
// also streams eagerly through the suite's sink — always a single
// indent: only the buffered --- lines nest per level in go test.
func (t *T) LogLine(msg string) {
	line := t.pos + ": " + msg
	t.events = append(t.events, Event{Log: line})
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
			t.failed = true
			t.logMsg(sprintArgs(args))
			return runtime.NIL, nil
		}, true
	case "Errorf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.failed = true
			t.logMsg(s)
			return runtime.NIL, nil
		}, true
	case "Fail":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.failed = true
			return runtime.NIL, nil
		}, true
	case "FailNow":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.failed = true
			return nil, &stopError{t: t}
		}, true
	case "Fatal":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.failed = true
			t.logMsg(sprintArgs(args))
			return nil, &stopError{t: t}
		}, true
	case "Fatalf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.failed = true
			t.logMsg(s)
			return nil, &stopError{t: t}
		}, true
	case "Skip":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			t.skipped = true
			t.logMsg(sprintArgs(args))
			return nil, &stopError{t: t, skip: true}
		}, true
	case "Skipf":
		return func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := sprintfArgs(args)
			if err != nil {
				return nil, err
			}
			t.skipped = true
			t.logMsg(s)
			return nil, &stopError{t: t, skip: true}
		}, true
	case "SkipNow":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			t.skipped = true
			return nil, &stopError{t: t, skip: true}
		}, true
	case "Failed":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return t.failed, nil
		}, true
	case "Skipped":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return t.skipped, nil
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
			t.cleanups = append(t.cleanups, args[0])
			return runtime.NIL, nil
		}, true
	case "TempDir":
		return func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			dir, err := os.MkdirTemp("", "minigo-test-*")
			if err != nil {
				return nil, err
			}
			t.cleanups = append(t.cleanups, func() { _ = os.RemoveAll(dir) })
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
				return &runtime.Tuple{Elems: []runtime.Value{runtime.NIL, false}}, nil
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
			t.parallel = true
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
	if t.finished {
		// post-finish logs have nowhere to go: record under the test
		// anyway so the suite keeps them visible rather than dropping.
		t.events = append(t.events, Event{Log: t.pos + ": " + msg})
		return
	}
	t.LogLine(msg)
}

// run implements t.Run: a subtest f(t *testing.T) invoked through the
// caller's VM. The returned bool is Go's Run result — whether the
// subtest passed.
func (t *T) run(vc runtime.VMCaller, name string, f runtime.Value) (runtime.Value, error) {
	sub := &T{suite: t.suite, parent: t, name: t.childName(name), pos: t.pos, depth: t.depth + 1, used: map[string]int{}}
	if !t.suite.MatchName(sub.depth, shortName(sub.name)) {
		// filtered out by -run: the subtest is discarded — go test
		// reports neither the RUN line nor a verdict for it.
		return true, nil
	}
	t.children = append(t.children, sub)
	if t.suite.Verbose && t.suite.Emit != nil {
		t.suite.Emit("=== RUN   " + sub.name)
	}
	start := time.Now()
	_, err := vc.Call(f, []runtime.Value{&runtime.GoValue{V: sub}})
	sub.elapsed = time.Since(start)
	if rerr := t.classifyErr(sub, err); rerr != nil {
		// a stopError for an ancestor keeps unwinding toward its own
		// level; the aborted subtest is recorded as a trap.
		sub.finished = true
		t.failed = true
		t.events = append(t.events, Event{Child: sub})
		return nil, rerr
	}
	if cerr := runCleanups(vc, sub); cerr != nil {
		if st, ok := StopOf(cerr); ok && st != sub {
			// a cleanup called an ancestor's FailNow — propagate it.
			sub.finished = true
			t.failed = true
			t.events = append(t.events, Event{Child: sub})
			return nil, cerr
		}
		// cleanup errors land on the subtest through classifyErr's
		// host-error arm; the flags were set by the sentinel path.
		t.classifyErr(sub, cerr)
	}
	sub.finished = true
	if FailedVerdict(sub.verdict) {
		t.failed = true
	}
	t.events = append(t.events, Event{Child: sub})
	return sub.verdict == Pass, nil
}

// classifyErr folds a finished call's error into the subtest's verdict.
// It returns the error itself when it is a stopError aimed at another
// test — that sentinel belongs to an ancestor's unwinding and must
// re-raise, not be swallowed as this child's own FailNow.
func (t *T) classifyErr(sub *T, err error) error {
	if err == nil {
		sub.verdict = flagsVerdict(sub)
		return nil
	}
	if st, ok := StopOf(err); ok {
		if st == sub {
			// the subtest's own FailNow/SkipNow: flags carry it.
			sub.verdict = flagsVerdict(sub)
			return nil
		}
		sub.verdict, sub.verdictErr = Trap, err.Error()
		return err
	}
	if _, ok := err.(*runtime.Panic); ok {
		sub.verdict, sub.verdictErr = Panic, err.Error()
		return nil
	}
	sub.verdict, sub.verdictErr = Trap, err.Error()
	return nil
}

// childName sanitizes and deduplicates a subtest name under t, the way
// go's matcher does: spaces become underscores and repeats get "#NN"
// suffixes.
func (t *T) childName(name string) string {
	name = sanitizeName(name)
	full := t.name + "/" + name
	if t.used[name] == 0 {
		t.used[name] = 1
		return full
	}
	for {
		suffix := fmt.Sprintf("#%02d", t.used[name])
		if t.used[name+suffix] == 0 {
			t.used[name]++
			t.used[name+suffix] = 1
			return full + suffix
		}
		t.used[name]++
	}
}

// shortName is the leaf segment of a full test name.
func shortName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// sanitizeName maps a subtest name the way go test's rewrite does:
// whitespace and other non-printing characters become underscores.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte('_')
		case !unicode.IsPrint(r):
			fmt.Fprintf(&b, "_%x", r)
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// flagsVerdict classifies a normally returned test function from its
// flags (Skip marks before the test body's own work when SkipNow
// unwound).
func flagsVerdict(t *T) Verdict {
	switch {
	case t.skipped:
		return Skip
	case t.failed:
		return Fail
	default:
		return Pass
	}
}

// runCleanups runs t's registered cleanups in LIFO order, the way
// tRunner runs them on the way out. Host func()s run directly; script
// function values run through vc.Call — which is why the top-level
// test's cleanups need a second Engine.Call of their own (the test
// Call's VM is gone by then), invoked via the bound "__cleanups".
func runCleanups(vc runtime.VMCaller, t *T) error {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		c := t.cleanups[i]
		var err error
		if h, ok := c.(func()); ok {
			err = safeHostCall(h)
		} else {
			_, err = vc.Call(c, nil)
		}
		if err != nil {
			if st, ok := StopOf(err); ok && st == t {
				// cleanup's own FailNow/SkipNow — flags already set.
				continue
			}
			return err
		}
	}
	return nil
}

// safeHostCall runs a host cleanup func, reporting a Go panic as the
// error a script panic would surface as.
func safeHostCall(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("panic: %v", r)
			}
		}
	}()
	f()
	return nil
}

// RunCleanups exposes the cleanup loop for the bound __cleanups helper.
func RunCleanups(vc runtime.VMCaller, t *T) error { return runCleanups(vc, t) }

// --- argument rendering ---

// argString renders one raw script value as text: untyped constants
// materialize, Named peels, cells deref.
func argString(v runtime.Value) string {
	return fmt.Sprint(hostbridge.GoNative(v))
}

// sprintArgs joins args like fmt.Sprintln (go's t.Log/Error bodies):
// operands separated by spaces, trailing newline trimmed by the caller.
func sprintArgs(args []runtime.Value) string {
	el := make([]any, len(args))
	for i, a := range args {
		el[i] = hostbridge.GoNative(a)
	}
	return strings.TrimSuffix(fmt.Sprintln(el...), "\n")
}

// sprintfArgs formats like t.Logf/Errorf: first arg the format string,
// rest the operands.
func sprintfArgs(args []runtime.Value) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing format argument")
	}
	format := argString(args[0])
	el := make([]any, len(args)-1)
	for i, a := range args[1:] {
		el[i] = hostbridge.GoNative(a)
	}
	return fmt.Sprintf(format, el...), nil
}

// checkCallable reports whether v can be vc.Call'd — a runtime func
// value or a builtin. t.Run/t.Cleanup reject anything else with a
// readable error instead of a deep VM failure.
func checkCallable(v runtime.Value) error {
	switch runtime.Unwrap(v).(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return nil
	}
	return fmt.Errorf("argument is not a function (%T)", v)
}

// outputWriter adapts t.Output() to io.Writer: writes decorate into
// the test's log buffer.
type outputWriter struct{ t *T }

func (w outputWriter) Write(b []byte) (int, error) {
	w.t.logMsg(strings.TrimSuffix(string(b), "\n"))
	return len(b), nil
}
