package mtesting

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/podhmo/minigo/runtime"
)

// run implements t.Run: a subtest f(t *testing.T) invoked through the
// caller's VM. The returned bool is Go's Run result — whether the
// subtest passed.
func (t *T) run(vc runtime.VMCaller, name string, f runtime.Value) (runtime.Value, error) {
	if t.suite.Stop() {
		return false, nil
	}
	sub := newSub(t, t.childName(name))
	if !t.suite.MatchFullName(sub.name) {
		// filtered out by -run: the subtest is discarded — go test
		// reports neither the RUN line nor a verdict for it.
		return true, nil
	}
	t.appendChild(sub)
	if t.suite.Verbose && t.suite.Emit != nil {
		t.suite.Emit("=== RUN   " + sub.name)
	}
	start := time.Now()
	_, err := vc.Call(f, []runtime.Value{&runtime.GoValue{V: sub}})
	sub.SetElapsed(time.Since(start))
	sub.cancelContext()
	if rerr := t.classifyErr(sub, err); rerr != nil {
		// a stopError for an ancestor keeps unwinding toward its own
		// level; the aborted subtest is recorded as a trap.
		sub.setFinished()
		t.markFailed()
		t.appendEvent(Event{Child: sub})
		return nil, rerr
	}
	cerr := runCleanups(vc, sub)
	if cerr != nil {
		if st, ok := StopOf(cerr); ok && st != sub {
			// a cleanup called an ancestor's FailNow — propagate it.
			sub.setFinished()
			t.markFailed()
			t.appendEvent(Event{Child: sub})
			return nil, cerr
		}
	}
	if sub.Verdict() != Panic && sub.Verdict() != Trap {
		// Re-evaluate even when cleanup returned nil: t.Error/t.Fatal
		// inside cleanup must change a PASS body into FAIL.
		t.classifyErr(sub, cerr)
	}
	sub.setFinished()
	if FailedVerdict(sub.Verdict()) {
		t.markFailed()
		if t.suite.FailFast {
			t.suite.Halt()
		}
	}
	t.appendEvent(Event{Child: sub})
	return !FailedVerdict(sub.Verdict()), nil
}

// classifyErr folds a finished call's error into the subtest's verdict.
// It returns the error itself when it is a stopError aimed at another
// test — that sentinel belongs to an ancestor's unwinding and must
// re-raise, not be swallowed as this child's own FailNow.
func (t *T) classifyErr(sub *T, err error) error {
	if err == nil {
		sub.SetVerdict(flagsVerdict(sub), "")
		return nil
	}
	if st, ok := StopOf(err); ok {
		if st == sub {
			// the subtest's own FailNow/SkipNow: flags carry it.
			sub.SetVerdict(flagsVerdict(sub), "")
			return nil
		}
		sub.SetVerdict(Trap, err.Error())
		return err
	}
	if _, ok := err.(*runtime.Panic); ok {
		sub.SetVerdict(Panic, err.Error())
		return nil
	}
	sub.SetVerdict(Trap, err.Error())
	return nil
}

// childName sanitizes and deduplicates a subtest name under t, the way
// go's matcher does: spaces become underscores and repeats get "#NN"
// suffixes.
func (t *T) childName(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
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
	case t.Failed():
		return Fail
	case t.Skipped():
		return Skip
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
	var first error
	for {
		err, ok := runOneCleanup(vc, t)
		if !ok {
			return first
		}
		if err != nil {
			if st, stop := StopOf(err); stop && st == t {
				// cleanup's own FailNow/SkipNow — flags already set.
				continue
			}
			if first == nil {
				first = err
			}
		}
	}
}

// runOneCleanup pops and invokes the innermost cleanup. The bool is
// false when none remain.
func runOneCleanup(vc runtime.VMCaller, t *T) (error, bool) {
	c, ok := t.popCleanup()
	if !ok {
		return nil, false
	}
	var err error
	if h, isHost := c.(func()); isHost {
		err = safeHostCall(h)
	} else {
		_, err = vc.Call(c, nil)
	}
	return err, true
}

// RunOneCleanup exposes single-cleanup execution for the bound
// __cleanup helper. Top-level cleanups run one Engine.Call each so a
// cleanup panic cannot kill the process the remaining cleanups need.
func RunOneCleanup(vc runtime.VMCaller, t *T) error {
	err, _ := runOneCleanup(vc, t)
	return err
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
