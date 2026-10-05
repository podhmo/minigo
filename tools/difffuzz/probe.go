package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ProbeResult is the per-line verdict for one probe.
type ProbeResult struct {
	Verdict Verdict
	Symptom string // for SILENT: see symptom()
	Want    string
	Got     string
	Detail  string
}

var reProbeLine = regexp.MustCompile(`^(\d+): (.*)$`)

func probeLines(stdout string) (map[int]string, []int) {
	m := map[int]string{}
	var order []int
	for _, l := range splitLines(stdout) {
		if sm := reProbeLine.FindStringSubmatch(l); sm != nil {
			id, _ := strconv.Atoi(sm[1])
			m[id] = sm[2]
			order = append(order, id)
		}
	}
	return m, order
}

// Obs is one observed probe line: `<%T> <%v>` or `panic: <msg>`.
type Obs struct {
	Type  string
	Value string
	Panic string
}

func parseObs(line string) Obs {
	if msg, ok := strings.CutPrefix(line, "panic: "); ok {
		return Obs{Panic: msg}
	}
	typ, val, _ := strings.Cut(line, " ")
	return Obs{Type: typ, Value: val}
}

// symptom names how a diverging probe line differs. Shrinking preserves the
// symptom, and the report groups by it, so a type-name bug cannot mask a
// wrong-value bug in the same expression.
func symptom(want, got string) string {
	w, g := parseObs(want), parseObs(got)
	switch {
	case w.Panic != "" && g.Panic != "":
		return "panic-message"
	case w.Panic != "":
		return "missing-panic"
	case g.Panic != "":
		return "spurious-panic"
	case w.Value != g.Value:
		return "value"
	default:
		return "type"
	}
}

// EvalProbes runs the probes once under go and as many times as needed
// under minigo: whenever minigo stops early (trap, unrecovered panic,
// interpreter crash, hang) the probe it stopped on is charged with it and
// the run resumes from the next probe. Go needs only one run because each
// probe is isolated by its own recover.
func (r *Runner) EvalProbes(ctx context.Context, name string, probes []Probe) ([]ProbeResult, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	d := probes[0].D
	res := make([]ProbeResult, len(probes))
	// gc is the validity filter: probes it rejects are dropped (SKIP) and
	// the rest rebuilt, so generator/shrinker rules need not be complete.
	var want Outcome
	for attempt := 0; ; attempt++ {
		var live []Probe
		var liveIDs []int
		for i, p := range probes {
			if res[i].Verdict != Skip {
				live, liveIDs = append(live, p), append(liveIDs, i)
			}
		}
		c, err := r.Materialize(name, Program(d, live, liveIDs))
		if err != nil {
			return nil, err
		}
		want, err = r.RunGo(ctx, c)
		if err == nil {
			break
		}
		bad := rejectedProbes(c, err)
		if len(bad) == 0 || attempt >= 8 {
			return nil, fmt.Errorf("oracle failed on generated program %s: %w", c.Dir, err)
		}
		for _, id := range bad {
			res[id] = ProbeResult{Verdict: Skip, Detail: "rejected by gc"}
		}
	}
	wantLines, _ := probeLines(want.Stdout)

	// go may have died mid-program with an unrecoverable error — e.g. a
	// probe's spawned goroutine panicking escapes every recover — leaving
	// no oracle line for that probe and every later one. Those probes are
	// unobservable, not divergent: Skip them and say why the oracle ended.
	markOracleGaps(res, want, wantLines)

	// wantPanics feeds the order-unspecified pass below: every panic go
	// produced on any probe is an authentic panic this program can emit.
	wantPanics := map[string]bool{}
	for _, l := range wantLines {
		if parseObs(l).Panic != "" {
			wantPanics[r.mask(l)] = true
		}
	}

	start := 0
	for start < len(probes) {
		var sub []Probe
		var subIDs []int
		for i := start; i < len(probes); i++ {
			if res[i].Verdict != Skip {
				sub, subIDs = append(sub, probes[i]), append(subIDs, i)
			}
		}
		if len(sub) == 0 {
			break
		}
		c, err := r.Materialize(name, Program(d, sub, subIDs))
		if err != nil {
			return nil, err
		}
		got, err := r.RunMinigo(ctx, c)
		if err != nil {
			return nil, err
		}
		gotLines, _ := probeLines(got.Stdout)
		next := len(probes)
		for _, id := range subIDs {
			g, ok := gotLines[id]
			if !ok {
				next = id
				break
			}
			w := wantLines[id]
			if r.mask(g) == r.mask(w) {
				res[id] = ProbeResult{Verdict: Pass}
			} else {
				res[id] = ProbeResult{Verdict: Silent, Symptom: symptom(w, g), Want: w, Got: g}
			}
		}
		if next == len(probes) {
			break
		}
		// minigo stopped before printing probe `next`: charge it.
		pr := ProbeResult{Want: wantLines[next]}
		msg := MinigoError(got.Stderr)
		switch {
		case got.TimedOut:
			pr.Verdict, pr.Detail = Hang, "timeout"
		case reHostPanic.MatchString(got.Stderr) && msg == "":
			first, _, _ := strings.Cut(strings.TrimSpace(got.Stderr), "\n")
			pr.Verdict, pr.Detail = Crash, first
		case strings.HasPrefix(msg, "runtime trap: panic:"):
			// a Go-recoverable panic (e.g. raised inside a host callback)
			// surfaced as an unrecoverable trap: the probe's recover never
			// ran, so this is wrong behavior, not a missing feature.
			pr.Verdict, pr.Symptom, pr.Detail = Silent, "panic-became-trap", msg
			pr.Got = "<" + msg + ">"
		case strings.HasPrefix(msg, "runtime trap:"):
			pr.Verdict, pr.Symptom, pr.Detail = Trap, Signature(msg), msg
		default:
			// a script panic escaped the probe's recover, or minigo
			// exited without a diagnosable reason — wrong behavior
			// without a trap.
			pr.Verdict, pr.Symptom, pr.Detail = Silent, "unrecovered-panic", "unrecovered: "+msg
			pr.Got = "<" + pr.Detail + ">"
		}
		res[next] = pr
		start = next + 1
	}
	// Order-unspecified pass: Go specifies no evaluation order between
	// non-call operands, so when one probe contains several panic-capable
	// sub-expressions, which one panics first is free for both runtimes.
	// minigo's strict left-to-right order and gc's hoisting both produce
	// legal outcomes — flag it only when minigo's panic text is one go
	// never produced on ANY probe: a panic go itself emitted somewhere is
	// authentic (not a fabricated index/length), while a panic unique to
	// minigo is the real panic-message bug this verdict exists to catch.
	for i := range res {
		if res[i].Verdict == Silent && res[i].Symptom == "panic-message" &&
			wantPanics[r.mask(res[i].Got)] {
			res[i] = ProbeResult{Verdict: Pass}
		}
	}
	return res, nil
}

// markOracleGaps marks unjudged probes the go oracle never reached as Skip.
// When the oracle program exits non-zero — e.g. a probe's spawned goroutine
// panicking escapes every recover — all probes after the death point have
// no line in wantLines; without this they would flag Silent on Want:"".
func markOracleGaps(res []ProbeResult, want Outcome, wantLines map[int]string) {
	if want.Exit == 0 {
		return
	}
	death := goPanic(want.Stderr)
	if death == "" {
		death = "exit " + fmt.Sprint(want.Exit)
	}
	for i := range res {
		if _, ok := wantLines[i]; res[i].Verdict == "" && !ok {
			res[i] = ProbeResult{Verdict: Skip, Detail: "oracle ended before this probe: " + death}
		}
	}
}

var (
	reBuildErr = regexp.MustCompile(`main\.go:(\d+):\d+: `)
	reTryLine  = regexp.MustCompile(`^\ttry\((\d+),`)
)

// rejectedProbes maps gc's error positions back to probe ids.
func rejectedProbes(c Case, buildErr error) []int {
	src, err := os.ReadFile(filepath.Join(c.Dir, "main.go"))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	var ids []int
	for _, m := range reBuildErr.FindAllStringSubmatch(buildErr.Error(), -1) {
		n, _ := strconv.Atoi(m[1])
		if n-1 < len(lines) {
			if t := reTryLine.FindStringSubmatch(lines[n-1]); t != nil {
				id, _ := strconv.Atoi(t[1])
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// Reduce greedily shrinks a failing probe while it keeps the same verdict.
// All single-step candidates of one round go into a single program sorted
// by size, so one go build + a few minigo runs evaluate the whole round.
func (r *Runner) Reduce(ctx context.Context, p Probe, want ProbeResult, maxRounds int) (Probe, ProbeResult, error) {
	var last ProbeResult
	for range maxRounds {
		cands := Shrink(p)
		if len(cands) == 0 {
			break
		}
		if len(cands) > 400 {
			cands = cands[:400]
		}
		res, err := r.EvalProbes(ctx, "shrink", cands)
		if err != nil {
			return p, last, err
		}
		found := false
		for i, pr := range res {
			// keep the symptom, not just the verdict: otherwise one
			// dominant bug (e.g. a wrong %T) absorbs every finding.
			if pr.Verdict == want.Verdict && pr.Symptom == want.Symptom {
				p, last, found = cands[i], pr, true
				break
			}
		}
		if !found {
			break
		}
	}
	return p, last, nil
}
