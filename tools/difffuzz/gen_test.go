package main

import (
	"context"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestGenDeterministic(t *testing.T) {
	render := func() []string {
		g := &Gen{R: rand.New(rand.NewPCG(42, 0)), D: textDomain}
		var out []string
		for range 50 {
			out = append(out, g.Probe(4).Body())
		}
		return out
	}
	if diff := cmp.Diff(render(), render()); diff != "" {
		t.Errorf("same seed, different probes (-first +second):\n%s", diff)
	}
}

// TestGeneratedProgramsCompile is the generator's own oracle check: every
// generated probe and every shrink candidate must build under gc, otherwise
// the harness reports generator bugs as oracle failures.
func TestGeneratedProgramsCompile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found")
	}
	if testing.Short() {
		t.Skip("builds generated programs")
	}
	for _, d := range []*Domain{numDomain, textDomain, reflDomain, langDomain} {
		// each domain builds in its own scratch module, so they run in parallel
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			testCompiles(t, d)
		})
	}
}

func testCompiles(t *testing.T, d *Domain) {
	g := &Gen{R: rand.New(rand.NewPCG(7, 7)), D: d}
	var probes []Probe
	for range 200 {
		p := g.Probe(4)
		probes = append(probes, p)
		probes = append(probes, Shrink(p)...)
	}
	r, err := NewRunner("", t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, len(probes))
	for i := range ids {
		ids[i] = i
	}
	c, err := r.Materialize("compile", Program(d, probes, ids))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunGo(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestParseProbeLine(t *testing.T) {
	cases := []struct {
		line string
		want Obs
	}{
		{"int8 -3", Obs{Type: "int8", Value: "-3"}},
		{"string ", Obs{Type: "string", Value: ""}},
		{"panic: runtime error: integer divide by zero", Obs{Panic: "runtime error: integer divide by zero"}},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, parseObs(c.line)); diff != "" {
			t.Errorf("parseObs(%q) (-want +got):\n%s", c.line, diff)
		}
	}
}

func TestSymptom(t *testing.T) {
	cases := []struct {
		want, got string
		sym       string
	}{
		{"int64 0", "int 0", "type"},
		{"float64 1.5", "int 1", "value"},
		{"uint8 0", "panic: runtime error: negative shift amount", "spurious-panic"},
		{"panic: runtime error: integer divide by zero", "int 0", "missing-panic"},
		{"panic: runtime error: integer divide by zero", "panic: division by zero", "panic-message"},
	}
	for _, c := range cases {
		if got := symptom(c.want, c.got); got != c.sym {
			t.Errorf("symptom(%q, %q) = %q, want %q", c.want, c.got, got, c.sym)
		}
	}
}

// When the oracle dies mid-program (e.g. a probe's spawned goroutine
// panics outside every recover), probes past the death point have no
// oracle line — they must be Skip, not Silent on an empty want.
func TestMarkOracleGaps(t *testing.T) {
	res := make([]ProbeResult, 4)
	res[0] = ProbeResult{Verdict: Pass}
	res[1] = ProbeResult{Verdict: Skip, Detail: "rejected by gc"}
	want := Outcome{
		Exit:   2,
		Stdout: "0: int 0\n",
		Stderr: "panic: runtime error: invalid memory address or nil pointer dereference\n",
	}
	wantLines, _ := probeLines(want.Stdout)
	markOracleGaps(res, want, wantLines)
	if res[0].Verdict != Pass || res[1].Verdict != Skip || res[1].Detail != "rejected by gc" {
		t.Errorf("judged/skipped probes must be untouched: %+v", res)
	}
	for _, i := range []int{2, 3} {
		if res[i].Verdict != Skip {
			t.Errorf("res[%d].Verdict = %s, want Skip (oracle never reached it)", i, res[i].Verdict)
		}
		if !strings.Contains(res[i].Detail, "nil pointer dereference") {
			t.Errorf("res[%d].Detail = %q, want the oracle's death cause", i, res[i].Detail)
		}
	}

	// a clean oracle exit leaves every probe judgeable
	res2 := make([]ProbeResult, 2)
	markOracleGaps(res2, Outcome{Stdout: "0: int 0\n"}, map[int]string{0: "int 0"})
	for i := range res2 {
		if res2[i].Verdict != "" {
			t.Errorf("exit-0 oracle: res[%d] = %s, want untouched", i, res2[i].Verdict)
		}
	}
}
