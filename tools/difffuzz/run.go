package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Verdict classifies one program run against the `go` oracle. The order
// encodes severity for minigo's contract ("scripts are valid Go; anything
// unimplemented traps loudly"): a loud trap is acceptable, a silent wrong
// answer or a host-level crash is a bug.
type Verdict string

const (
	Pass   Verdict = "PASS"   // same stdout/stderr/exit as go
	Trap   Verdict = "TRAP"   // minigo stopped loudly (runtime trap) after a matching output prefix
	Silent Verdict = "SILENT" // minigo produced different output without trapping there
	Crash  Verdict = "CRASH"  // the interpreter itself panicked (host goroutine trace)
	Hang   Verdict = "HANG"   // minigo exceeded the timeout while go did not
	Skip   Verdict = "SKIP"   // the oracle could not build/run the program
)

// Outcome is the raw result of one execution.
type Outcome struct {
	Stdout   string
	Stderr   string
	Exit     int
	TimedOut bool
}

// Runner executes programs with both the go toolchain and a minigo binary
// inside a scratch module.
type Runner struct {
	Minigo  string // path to the minigo binary
	Work    string // scratch module root (has go.mod)
	Timeout time.Duration
	// Masks hide known divergences: every match is replaced by "…" in both
	// outputs before comparing, so a dominant known bug stops absorbing
	// findings (Csmith-style known-bug suppression).
	Masks []*regexp.Regexp
	seq   atomic.Int64
}

func (r *Runner) mask(s string) string {
	for _, m := range r.Masks {
		s = m.ReplaceAllString(s, "…")
	}
	return s
}

func NewRunner(minigo, work string, timeout time.Duration) (*Runner, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	gomod := "module difffuzz.example\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(gomod), 0o644); err != nil {
		return nil, err
	}
	return &Runner{Minigo: minigo, Work: work, Timeout: timeout}, nil
}

// Case is a materialized program directory inside the scratch module.
type Case struct {
	Dir string // absolute
	Rel string // "./name" relative to the module root
}

func (r *Runner) Materialize(name, src string) (Case, error) {
	name = fmt.Sprintf("%s_%d", name, r.seq.Add(1))
	dir := filepath.Join(r.Work, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Case{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		return Case{}, err
	}
	return Case{Dir: dir, Rel: "./" + name}, nil
}

// RunGo builds the case with the go toolchain and runs the binary, so the
// exit code is the program's own (go run folds everything into 1).
func (r *Runner) RunGo(ctx context.Context, c Case) (Outcome, error) {
	bin := filepath.Join(c.Dir, "oracle.bin")
	// -e: report every error, so rejected probes can be dropped in one pass
	build := exec.CommandContext(ctx, "go", "build", "-gcflags=-e", "-o", bin, c.Rel)
	build.Dir = r.Work
	if out, err := build.CombinedOutput(); err != nil {
		return Outcome{}, fmt.Errorf("go build: %w\n%s", err, out)
	}
	return r.exec(ctx, c.Dir, bin)
}

func (r *Runner) RunMinigo(ctx context.Context, c Case) (Outcome, error) {
	return r.exec(ctx, r.Work, r.Minigo, "run", c.Rel)
}

func (r *Runner) exec(ctx context.Context, dir, name string, args ...string) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	o := Outcome{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		o.TimedOut = true
		return o, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		o.Exit = ee.ExitCode()
		return o, nil
	}
	return o, err
}

var (
	// minigo reports failures through slog: error="runtime trap: ..." or
	// error="panic: ...".
	reMinigoErr = regexp.MustCompile(`error="((?:[^"\\]|\\.)*)"`)
	// a host-level panic inside the interpreter prints a goroutine trace.
	reHostPanic = regexp.MustCompile(`(?m)^goroutine \d+ \[`)
	reAddr      = regexp.MustCompile(`0x[0-9a-f]+`)
	reLineCol   = regexp.MustCompile(`:\d+(:\d+)?`)
)

// MinigoError extracts the first line of minigo's reported error.
func MinigoError(stderr string) string {
	m := reMinigoErr.FindStringSubmatch(stderr)
	if m == nil {
		return ""
	}
	msg := strings.ReplaceAll(m[1], `\"`, `"`)
	msg, _, _ = strings.Cut(msg, `\n`)
	return msg
}

// Signature normalizes an error line into a bucket key.
func Signature(msg string) string {
	msg = reAddr.ReplaceAllString(msg, "0x?")
	msg = reLineCol.ReplaceAllString(msg, ":?")
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return msg
}

// Judgement is the verdict plus the evidence that produced it.
type Judgement struct {
	Verdict Verdict
	// Line is the 0-based index of the first diverging stdout line, -1
	// when stdout matched.
	Line   int
	Want   string // go's line at Line (or exit/panic summary)
	Got    string // minigo's line at Line
	Detail string // trap/panic message, bucket input
}

// Judge compares a minigo outcome against the go oracle.
func Judge(want, got Outcome) Judgement {
	if want.TimedOut {
		return Judgement{Verdict: Skip, Line: -1, Detail: "oracle timed out"}
	}
	if got.TimedOut {
		return Judgement{Verdict: Hang, Line: -1, Detail: "timeout"}
	}
	if reHostPanic.MatchString(got.Stderr) && !strings.Contains(got.Stderr, "error=") {
		first, _, _ := strings.Cut(strings.TrimSpace(got.Stderr), "\n")
		return Judgement{Verdict: Crash, Line: -1, Detail: first}
	}
	errMsg := MinigoError(got.Stderr)
	wl := splitLines(want.Stdout)
	gl := splitLines(got.Stdout)
	k := firstDiff(wl, gl)
	trapped := strings.HasPrefix(errMsg, "runtime trap:")

	if k >= 0 {
		j := Judgement{Line: k, Want: at(wl, k), Got: at(gl, k), Detail: errMsg}
		// minigo stopped early and loudly: its output is a strict prefix.
		if trapped && k == len(gl) {
			j.Verdict = Trap
			return j
		}
		j.Verdict = Silent
		return j
	}
	// stdout identical; compare how each one ended.
	switch {
	case want.Exit == 0 && got.Exit == 0:
		if want.Stderr != got.Stderr {
			return Judgement{Verdict: Silent, Line: -1, Want: "stderr: " + want.Stderr, Got: "stderr: " + got.Stderr}
		}
		return Judgement{Verdict: Pass, Line: -1}
	case got.Exit != 0 && trapped:
		return Judgement{Verdict: Trap, Line: -1, Detail: errMsg}
	case want.Exit == 0 && got.Exit != 0:
		return Judgement{Verdict: Silent, Line: -1, Want: "exit 0", Got: errMsg, Detail: errMsg}
	case want.Exit != 0 && got.Exit == 0:
		return Judgement{Verdict: Silent, Line: -1, Want: "exit " + fmt.Sprint(want.Exit) + ": " + goPanic(want.Stderr), Got: "exit 0"}
	default: // both failed: compare the panic message loosely
		wp := goPanic(want.Stderr)
		if wp != "" && !strings.Contains(errMsg, strings.TrimPrefix(wp, "panic: ")) {
			return Judgement{Verdict: Silent, Line: -1, Want: wp, Got: errMsg, Detail: "panic message"}
		}
		return Judgement{Verdict: Pass, Line: -1}
	}
}

func goPanic(stderr string) string {
	for l := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(l, "panic: ") || strings.HasPrefix(l, "fatal error: ") {
			return strings.TrimSuffix(l, " [recovered]")
		}
	}
	return ""
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func firstDiff(a, b []string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) || i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return -1
}

func at(xs []string, i int) string {
	if i < len(xs) {
		return xs[i]
	}
	return "<missing>"
}
