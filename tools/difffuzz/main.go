// Command difffuzz is a differential harness for minigo, using the go
// toolchain as the oracle.
//
//	difffuzz gen    [-seed N] [-batches N] [-probes N] [-depth N]
//	difffuzz corpus [-goroot-tests] [dir-or-file...]
//
// `gen` generates random well-typed scalar expressions (sized ints, named
// ints, floats, strings, bools; shifts, conversions, compound assignment),
// evaluates each one in its own recover-isolated closure, and greedily
// shrinks every divergence to a minimal expression. `corpus` runs whole
// programs — by default every `// run` test in $GOROOT/test — and buckets
// the results.
//
// Verdicts follow minigo's contract: a loud `runtime trap` after a matching
// output prefix is acceptable (TRAP); output that differs without a trap
// (SILENT), an interpreter-level panic (CRASH) or a hang (HANG) is a bug.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	ctx := context.Background()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: difffuzz gen|corpus [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = cmdGen(ctx, os.Args[2:])
	case "corpus":
		err = cmdCorpus(ctx, os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		slog.ErrorContext(ctx, "difffuzz", "error", err)
		os.Exit(1)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type common struct {
	masks   multiFlag
	root    string
	minigo  string
	work    string
	timeout time.Duration
	jobs    int
	out     string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.root, "root", "../..", "minigo repository root (used to build the binary)")
	fs.StringVar(&c.minigo, "minigo", "", "prebuilt minigo binary (default: build from -root)")
	fs.StringVar(&c.work, "work", "", "scratch directory (default: a fresh temp dir)")
	fs.DurationVar(&c.timeout, "timeout", 20*time.Second, "per-run timeout")
	fs.IntVar(&c.jobs, "j", 8, "parallel jobs")
	fs.StringVar(&c.out, "out", "", "write the markdown report here (default: stdout)")
	fs.Var(&c.masks, "mask", "regexp hiding a known divergence in probe lines (repeatable), e.g. '^\\[\\]?(rune|int32|byte|uint8) '")
}

func (c *common) runner(ctx context.Context) (*Runner, error) {
	if c.work == "" {
		d, err := os.MkdirTemp("", "difffuzz-")
		if err != nil {
			return nil, err
		}
		c.work = d
	}
	if c.minigo == "" {
		c.minigo = filepath.Join(c.work, "minigo.bin")
		root, err := filepath.Abs(c.root)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-o", c.minigo, "./cmd/minigo")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("build minigo: %w\n%s", err, out)
		}
	}
	slog.InfoContext(ctx, "difffuzz", "work", c.work, "minigo", c.minigo)
	r, err := NewRunner(c.minigo, c.work, c.timeout)
	if err != nil {
		return nil, err
	}
	for _, m := range c.masks {
		re, err := regexp.Compile(m)
		if err != nil {
			return nil, fmt.Errorf("-mask %q: %w", m, err)
		}
		r.Masks = append(r.Masks, re)
	}
	return r, nil
}

func (c *common) writer() (io.Writer, func() error, error) {
	if c.out == "" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.Create(c.out)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// ---- gen ------------------------------------------------------------------

type finding struct {
	Probe   Probe
	Result  ProbeResult
	Count   int
	Orig    string
	Derived []*finding // folded findings whose expression contains this one
}

// foldDerived approximates root-cause clustering: a reduced finding whose
// expression contains another (smaller, non-leaf) finding's expression is
// most likely the same bug propagated upward, so it is folded under it.
func foldDerived(bugs map[string]*finding) {
	keys := sortedKeys(bugs, func(f *finding) int { return -f.Probe.Size() }) // smallest first
	for i := len(keys) - 1; i >= 0; i-- {
		b := bugs[keys[i]]
		for _, ka := range keys[:i] {
			a := bugs[ka]
			if a.Probe.Size() >= b.Probe.Size() {
				continue
			}
			if a.Probe.R != nil || b.Probe.R != nil {
				// chain probes fold by shape prefix: a reduced chain is a
				// literal prefix of the longer one it came from.
				if a.Probe.R != nil && b.Probe.R != nil && strings.HasPrefix(b.Probe.Shape(), a.Probe.Shape()) {
					a.Derived = append(a.Derived, b)
					a.Count += b.Count
					delete(bugs, keys[i])
					break
				}
				continue
			}
			if a.Probe.Root.Op == "var" {
				continue
			}
			if strings.Contains(b.Probe.Root.Expr(), a.Probe.Root.Expr()) {
				a.Derived = append(a.Derived, b)
				a.Count += b.Count
				delete(bugs, keys[i])
				break
			}
		}
	}
}

func cmdGen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	var c common
	c.register(fs)
	seed := fs.Uint64("seed", uint64(time.Now().UnixNano()), "PRNG seed (printed in the report)")
	batches := fs.Int("batches", 8, "number of generated programs")
	nprobes := fs.Int("probes", 200, "probes per program")
	depth := fs.Int("depth", 4, "max expression depth")
	domain := fs.String("domain", "text", "probe domain: text (strings/strconv/fmt/collections), num (sized ints, shifts, conversions), reflect (facade operation chains), or lang (structs/methods/interfaces, control flow, generics)")
	shrinkRounds := fs.Int("shrink", 12, "max shrink rounds per finding (0 disables)")
	perBucket := fs.Int("per-bucket", 2, "findings shrunk per fingerprint group")
	emit := fs.String("emit", "", "write each reduced bug as <dir>/<slug>/{main.go,want.stdout,PENDING} — the layout TestDiffRegressions runs")
	_ = fs.Parse(args)
	d, ok := domains[*domain]
	if !ok {
		return fmt.Errorf("unknown -domain %q", *domain)
	}
	r, err := c.runner(ctx)
	if err != nil {
		return err
	}

	var (
		mu       sync.Mutex
		counts   = map[Verdict]int{}
		bugs     = map[string]*finding{} // keyed by reduced shape
		traps    = map[string]int{}      // keyed by trap signature
		trapEx   = map[string]finding{}
		errs     []error
		sem      = make(chan struct{}, c.jobs)
		wg       sync.WaitGroup
		started  = time.Now()
		rawFinds []finding
	)
	for b := range *batches {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			g := &Gen{R: rand.New(rand.NewPCG(*seed, uint64(b))), D: d}
			probes := make([]Probe, *nprobes)
			for i := range probes {
				probes[i] = g.Probe(*depth)
			}
			res, err := r.EvalProbes(ctx, fmt.Sprintf("gen%d", b), probes)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for i, pr := range res {
				counts[pr.Verdict]++
				switch pr.Verdict {
				case Trap:
					sig := Signature(pr.Detail)
					traps[sig]++
					if ex, ok := trapEx[sig]; !ok || probes[i].Size() < ex.Probe.Size() {
						trapEx[sig] = finding{Probe: probes[i], Result: pr}
					}
				case Pass, Skip:
				default:
					rawFinds = append(rawFinds, finding{Probe: probes[i], Result: pr})
				}
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}

	// Group raw findings by a cheap fingerprint before shrinking: one
	// dominant bug otherwise costs a shrink per occurrence. Each group
	// shrinks its smallest -per-bucket members; the reduced shape is the
	// final dedup key and inherits the group's occurrence count.
	groups := map[string][]finding{}
	for _, f := range rawFinds {
		k := fingerprint(f)
		groups[k] = append(groups[k], f)
	}
	for _, fs := range groups {
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Probe.Size() < fs[j].Probe.Size() })
		n := min(len(fs), *perBucket)
		for i := range n {
			weight := 1
			if i == 0 {
				weight += len(fs) - n // unshrunk members count toward the first
			}
			f := fs[i]
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				p, pr := f.Probe, f.Result
				if *shrinkRounds > 0 {
					rp, rpr, err := r.Reduce(ctx, f.Probe, f.Result, *shrinkRounds)
					if err != nil {
						slog.WarnContext(ctx, "shrink failed", "error", err)
					} else if rpr.Verdict != "" {
						p, pr = rp, rpr
					}
				}
				mu.Lock()
				defer mu.Unlock()
				key := string(pr.Verdict) + " " + pr.Symptom + " " + coarseShape(p.Shape())
				if b, ok := bugs[key]; ok {
					b.Count += weight
					if p.Size() < b.Probe.Size() {
						b.Probe, b.Result = p, pr
					}
					return
				}
				bugs[key] = &finding{Probe: p, Result: pr, Count: weight, Orig: f.Probe.Body()}
			}()
		}
	}
	// traps are by design, but a minimal repro tells the fixing session
	// exactly which construct is missing: shrink one per signature.
	if *shrinkRounds > 0 {
		for sig, f := range trapEx {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				rp, rpr, err := r.Reduce(ctx, f.Probe, f.Result, *shrinkRounds)
				if err != nil || rpr.Verdict == "" {
					return
				}
				mu.Lock()
				trapEx[sig] = finding{Probe: rp, Result: rpr}
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	w, closeFn, err := c.writer()
	if err != nil {
		return err
	}
	defer closeFn()
	fmt.Fprintf(w, "# difffuzz gen report\n\ndomain=%s seed=%d batches=%d probes=%d depth=%d elapsed=%s\n\n",
		d.Name, *seed, *batches, *nprobes, *depth, time.Since(started).Round(time.Second))
	for _, m := range c.masks {
		fmt.Fprintf(w, "mask: `%s`\n\n", m)
	}
	fmt.Fprintf(w, "| verdict | probes |\n|---|---|\n")
	for _, v := range []Verdict{Pass, Trap, Silent, Crash, Hang, Skip} {
		fmt.Fprintf(w, "| %s | %d |\n", v, counts[v])
	}
	reduced := len(bugs)
	foldDerived(bugs)
	if *emit != "" {
		for _, f := range bugs {
			if err := emitCase(ctx, r, *emit, f); err != nil {
				slog.WarnContext(ctx, "emit failed", "error", err)
			}
		}
	}
	fmt.Fprintf(w, "\n## Bugs (%d unique after shrinking, %d after folding derived findings)\n\n", reduced, len(bugs))
	keys := sortedKeys(bugs, func(f *finding) int { return f.Count })
	for _, k := range keys {
		f := bugs[k]
		fmt.Fprintf(w, "### %s/%s ×%d — `%s`\n\n```go\n%s\n```\n\n- go:     `%s`\n- minigo: `%s`\n",
			f.Result.Verdict, f.Result.Symptom, f.Count, f.Probe.Shape(), f.Probe.Body(), f.Result.Want, f.Result.Got)
		if f.Result.Detail != "" {
			fmt.Fprintf(w, "- detail: `%s`\n", f.Result.Detail)
		}
		for _, d := range f.Derived {
			fmt.Fprintf(w, "- derived ×%d (%s/%s): `%s`\n", d.Count, d.Result.Verdict, d.Result.Symptom, d.Probe.Body())
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "## Traps (by signature)\n\n| count | signature | example |\n|---|---|---|\n")
	for _, k := range sortedKeys(traps, func(n int) int { return n }) {
		fmt.Fprintf(w, "| %d | `%s` | `%s` |\n", traps[k], strings.ReplaceAll(k, "|", `\|`), strings.ReplaceAll(trapEx[k].Probe.Body(), "|", `\|`))
	}
	fmt.Fprintf(w, "\nPrelude for reproducing (variables/types used by probes):\n\n```go\n%s```\n", prelude(d))
	return nil
}

// emitCase writes a standalone, go-runnable regression case. want.stdout is
// go's output; PENDING marks it as a known failure until minigo is fixed.
// The case is verified standalone before it is written: a finding that does
// not reproduce alone (the batch program's shared state made it diverge, or
// the reduced program kills the go oracle itself) makes a false PENDING.
func emitCase(ctx context.Context, r *Runner, dir string, f *finding) error {
	src := Program(f.Probe.D, []Probe{f.Probe}, []int{0})
	c, err := r.Materialize("emit", src)
	if err != nil {
		return err
	}
	want, err := r.RunGo(ctx, c)
	if err != nil {
		return err
	}
	if want.Exit != 0 {
		return fmt.Errorf("skip emit %s: go exits %d on the reduced program: %s",
			f.Probe.Shape(), want.Exit, goPanic(want.Stderr))
	}
	got, err := r.RunMinigo(ctx, c)
	if err != nil {
		return err
	}
	if got.Exit == 0 && got.Stdout == want.Stdout {
		return fmt.Errorf("skip emit %s: no divergence standalone", f.Probe.Shape())
	}
	slug := fmt.Sprintf("%s_%s_%08x", f.Probe.D.Name, f.Result.Symptom, fnv32(src))
	out := filepath.Join(dir, slug)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	pending := fmt.Sprintf("%s/%s (seen ×%d)\nshape:  %s\ngo:     %s\nminigo: %s\n",
		f.Result.Verdict, f.Result.Symptom, f.Count, f.Probe.Shape(), f.Result.Want, f.Result.Got)
	for name, body := range map[string]string{"main.go": src, "want.stdout": want.Stdout, "PENDING": pending} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

var (
	reSignedT   = regexp.MustCompile(`\b(int(8|16|32|64)?|MyI16)\b`)
	reUnsignedT = regexp.MustCompile(`\b(uint(8|16|32|64|ptr)?|MyU8)\b`)
)

// coarseShape collapses sized integer types to their signedness class for
// the final dedup key: `int16 >> uint` and `int32 >> uint64` are one bug.
func coarseShape(s string) string {
	s = reUnsignedT.ReplaceAllString(s, "U")
	return reSignedT.ReplaceAllString(s, "S")
}

// fingerprint is the pre-shrink grouping key: how the line diverged, the
// types on both sides, the statement context and the root operator (the
// last chain step for reflect probes).
func fingerprint(f finding) string {
	w, g := parseObs(f.Result.Want), parseObs(f.Result.Got)
	op := ""
	if f.Probe.R != nil {
		op = f.Probe.R.Steps[len(f.Probe.R.Steps)-1].Text
	} else {
		op = f.Probe.Root.Op
	}
	return strings.Join([]string{string(f.Result.Verdict), f.Result.Symptom, w.Type, g.Type, f.Probe.Ctx, op}, "|")
}

func prelude(d *Domain) string {
	src := Program(d, nil, nil)
	i := strings.Index(src, "func try")
	return src[:i]
}

func sortedKeys[V any](m map[string]V, weight func(V) int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		wi, wj := weight(m[keys[i]]), weight(m[keys[j]])
		if wi != wj {
			return wi > wj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ---- corpus ---------------------------------------------------------------

func cmdCorpus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("corpus", flag.ExitOnError)
	var c common
	c.register(fs)
	gorootTests := fs.Bool("goroot-tests", false, "add every single-file `// run` test in $GOROOT/test")
	_ = fs.Parse(args)
	r, err := c.runner(ctx)
	if err != nil {
		return err
	}
	var files []string
	for _, a := range fs.Args() {
		st, err := os.Stat(a)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			files = append(files, a)
			continue
		}
		ms, _ := filepath.Glob(filepath.Join(a, "*.go"))
		files = append(files, ms...)
		ms, _ = filepath.Glob(filepath.Join(a, "*", "main.go"))
		files = append(files, ms...)
	}
	if *gorootTests {
		out, err := exec.CommandContext(ctx, "go", "env", "GOROOT").Output()
		if err != nil {
			return err
		}
		ms, _ := filepath.Glob(filepath.Join(strings.TrimSpace(string(out)), "test", "*.go"))
		for _, f := range ms {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			// only plain `// run` (no args/flags), single-file, package main
			first, _, _ := strings.Cut(string(b), "\n")
			if strings.TrimSpace(first) == "// run" && strings.Contains(string(b), "\npackage main") {
				files = append(files, f)
			}
		}
	}

	type row struct {
		file string
		j    Judgement
	}
	var (
		mu   sync.Mutex
		rows []row
		sem  = make(chan struct{}, c.jobs)
		wg   sync.WaitGroup
	)
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			j := runCorpusFile(ctx, r, f)
			mu.Lock()
			rows = append(rows, row{f, j})
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].file < rows[j].file })

	w, closeFn, err := c.writer()
	if err != nil {
		return err
	}
	defer closeFn()
	counts := map[Verdict]int{}
	buckets := map[string][]string{}
	for _, rw := range rows {
		counts[rw.j.Verdict]++
		if rw.j.Verdict == Trap || rw.j.Verdict == Crash {
			sig := string(rw.j.Verdict) + " " + Signature(rw.j.Detail)
			buckets[sig] = append(buckets[sig], filepath.Base(rw.file))
		}
	}
	fmt.Fprintf(w, "# difffuzz corpus report\n\n%d programs\n\n| verdict | programs |\n|---|---|\n", len(rows))
	for _, v := range []Verdict{Pass, Trap, Silent, Crash, Hang, Skip} {
		fmt.Fprintf(w, "| %s | %d |\n", v, counts[v])
	}
	fmt.Fprintf(w, "\n## SILENT / HANG (bugs: wrong behavior without a trap)\n\n")
	for _, rw := range rows {
		if rw.j.Verdict == Silent || rw.j.Verdict == Hang {
			fmt.Fprintf(w, "- `%s` line %d\n  - go:     `%s`\n  - minigo: `%s`\n", filepath.Base(rw.file), rw.j.Line, clip(rw.j.Want), clip(rw.j.Got))
			if rw.j.Detail != "" {
				fmt.Fprintf(w, "  - detail: `%s`\n", clip(rw.j.Detail))
			}
		}
	}
	fmt.Fprintf(w, "\n## TRAP / CRASH buckets (what to implement next, by reach)\n\n| programs | signature | examples |\n|---|---|---|\n")
	for _, k := range sortedKeys(buckets, func(xs []string) int { return len(xs) }) {
		ex := buckets[k]
		if len(ex) > 4 {
			ex = append(ex[:4:4], "…")
		}
		fmt.Fprintf(w, "| %d | `%s` | %s |\n", len(buckets[k]), strings.ReplaceAll(k, "|", `\|`), strings.Join(ex, " "))
	}
	return nil
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func runCorpusFile(ctx context.Context, r *Runner, file string) Judgement {
	src, err := os.ReadFile(file)
	if err != nil {
		return Judgement{Verdict: Skip, Line: -1, Detail: err.Error()}
	}
	name := strings.TrimSuffix(filepath.Base(file), ".go")
	if name == "main" {
		name = filepath.Base(filepath.Dir(file))
	}
	cs, err := r.Materialize(sanitize(name), string(src))
	if err != nil {
		return Judgement{Verdict: Skip, Line: -1, Detail: err.Error()}
	}
	want, err := r.RunGo(ctx, cs)
	if err != nil {
		return Judgement{Verdict: Skip, Line: -1, Detail: "oracle: " + err.Error()}
	}
	got, err := r.RunMinigo(ctx, cs)
	if err != nil {
		return Judgement{Verdict: Skip, Line: -1, Detail: err.Error()}
	}
	return Judge(want, got)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, s)
}
