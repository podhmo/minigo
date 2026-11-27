package testrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/internal/testrun/mtesting"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

// Options is one `minigo test <dir>` invocation.
type Options struct {
	Dir string // package directory; a filesystem path, not an import path
	Out io.Writer

	Verbose  bool   // -v: stream === RUN / --- / log lines like go test -v
	Short    bool   // -short: bound to testing.Short()
	Run      string // -run: "/" separated unanchored regexps
	Count    int    // -count: run the whole suite this many times (<=0 runs it once)
	Timeout  time.Duration
	FailFast bool // -failfast: stop scheduling after the first failing test

	// Modes/Tags carry `minigo run`'s --src/--deny/--tags plumbing:
	// Modes is minigo.WithPackageModes, Tags the build tag list.
	Modes map[string]minigo.PackageMode
	Tags  []string

	// Tagged (--tagged <tag>) adds the tag to the build config AND
	// restricts the run set to tests declared in files the tag selects
	// (x/storybook's "tag-gated test" semantics). Others are still
	// built — shared helpers compile — but reported as skipped in -v.
	Tagged string
}

// ErrFailed is Run's result when the suite ran but did not pass:
// cmd/minigo maps it to exit 1 without printing again (the report
// already said FAIL).
var ErrFailed = errors.New("minigo test: not all tests passed")

// Run executes `minigo test <dir>`: build the package's test variant,
// discover TestXxx functions, run them one Engine.Call each, and write
// the `go test -v` shaped report to o.Out. It returns nil on all-pass,
// [ErrFailed] on any failure outcome, and another error for
// operational failures (the directory cannot be scanned, ...).
func Run(ctx context.Context, o Options) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	rep := newReport(out, o.Verbose)
	start := time.Now()

	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(abs); err != nil {
		return fmt.Errorf("%w", err)
	} else if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", o.Dir)
	}

	match, err := compileRun(o.Run)
	if err != nil {
		return err
	}

	// The build config is --tags plus the --tagged gate tag; the run
	// set gate compares file lists under the two configs.
	cfg := resolve.BuildConfig{Tags: o.Tags}
	buildCfg := cfg
	if o.Tagged != "" {
		buildCfg.Tags = append(append([]string{}, cfg.Tags...), o.Tagged)
	}
	scan, err := scanTests(abs, buildCfg)
	if err != nil {
		return err
	}
	var gated map[string]bool
	if o.Tagged != "" {
		plain, err := scanTests(abs, cfg)
		if err != nil {
			return err
		}
		inPlain := map[string]bool{}
		for _, f := range plain.all {
			inPlain[f] = true
		}
		gated = map[string]bool{}
		for _, f := range scan.all {
			if !inPlain[f] {
				// keyed like syntax.File.Name: slash-normalized
				gated[filepath.ToSlash(f)] = true
			}
		}
	}

	// The suite state the facade reads (-short, -v, ctx, deadline).
	suite := &mtesting.Suite{
		Short:    o.Short,
		Verbose:  o.Verbose,
		FailFast: o.FailFast,
		Emit:     rep.emitLine,
		Ctx:      ctx,
		Match:    match,
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		suite.Ctx = ctx
		suite.Deadline = time.Now().Add(o.Timeout)
	}

	// problems block the build the way go build would reject the dir:
	// unreadable test files, and test files with a third package clause.
	failed := false
	for _, p := range scan.rejected {
		rep.note("%s", p)
		failed = true
	}
	for _, f := range scan.other {
		rep.note("%s: package clause is neither the package's name nor <name>_test", relPath(abs, f))
		failed = true
	}
	for _, f := range scan.external {
		rep.note("%s: external test package (package *_test) is not supported", relPath(abs, f))
		failed = true
	}
	for _, f := range scan.cgoSkipped {
		rep.note("%s: imports \"C\" — cgo is not supported, file skipped", relPath(abs, f))
		failed = true
	}

	// The test variant: the directory's own build files plus the
	// internal *_test.go files, via a resolver that splices them into
	// LocateDir's file list for this directory only.
	inner, err := resolve.NewGoScanResolver(abs, buildCfg)
	if err != nil {
		return err
	}
	vr := &variantResolver{inner: inner, dir: abs, testFiles: scan.internal, pkgName: scan.pkgName}

	// Locate before building: the meta carries the import path for the
	// trailer, and a locate failure is "[setup failed]" where a build
	// failure is "[build failed]" (go test's wording).
	importPath := pathOfDir(abs)
	meta, err := vr.LocateDir(ctx, abs)
	if err != nil {
		rep.note("%v", err)
		rep.buildFailed(importPath, time.Since(start), "setup failed")
		return ErrFailed
	}
	importPath = meta.ImportPath

	// The test target itself must always be interpreted from source. A
	// host-bound package may expose the same import path for ordinary
	// scripts, but its bound package has no syntax index to scan for
	// TestXxx declarations.
	modes := map[string]minigo.PackageMode{importPath: minigo.ModeSource}
	for path, mode := range o.Modes {
		if path != importPath {
			modes[path] = mode
		}
	}
	opts := []minigo.Option{
		minigo.WithOutput(out),
		minigo.WithArgs([]string{abs}),
		minigo.WithBuildConfig(buildCfg),
		minigo.WithPackageModes(modes),
	}
	e := minigo.NewEngine(abs, opts...)
	e.WithResolver(vr)
	e.Bind("testing", mtesting.Symbols(suite))
	e.Bind("flag", mtesting.FlagSymbols(suite))

	pkg, err := e.Package(ctx, abs)
	if err != nil {
		rep.note("%v", err)
		rep.buildFailed(importPath, time.Since(start), "build failed")
		return ErrFailed
	}

	testSet := map[string]bool{}
	for _, f := range scan.internal {
		// decl File.Name is slash-normalized (syntax.ParseFile)
		testSet[filepath.ToSlash(f)] = true
	}
	disc := discoverTests(pkg, testSet, abs)

	for _, m := range disc.Malformed {
		rep.note("%s: %s has a signature minigo test does not run (%s)", m.Pos, m.Name, m.Why)
	}
	fatal := false
	for _, u := range disc.Unsupported {
		rep.note("%s: %s: %s is not supported", u.Pos, u.What, u.Name)
		failed = true
		if u.Fatal {
			fatal = true
		}
	}
	if fatal {
		rep.finish(pkg.Path, true, time.Since(start))
		return ErrFailed
	}

	testingPkg, err := e.Package(ctx, "testing")
	if err != nil {
		return err
	}

	failfast := false
	ran := 0
	count := o.Count
	if count <= 0 {
		count = 1
	}
loop:
	for iter := 0; iter < count; iter++ {
		for _, ti := range disc.Tests {
			if !suite.MatchFullName(ti.Name) {
				continue
			}
			if gated != nil && !gated[ti.File] {
				rep.excludedByTag(ti.Name, o.Tagged)
				continue
			}
			if failfast || suite.Stop() || ctx.Err() != nil {
				break loop
			}
			ran++
			t := mtesting.NewT(suite, ti.Name, ti.Pos)
			if suite.Verbose {
				rep.emitLine("=== RUN   " + ti.Name)
			}
			cerr := callTest(ctx, e, pkg, ti.Name, t)
			t.CancelContext()
			if pkg.State() == runtime.Failed {
				// init failed: the remaining tests would all die the
				// same way — report once and stop scheduling.
				rep.note("%s: package initialization failed: %v", ti.Pos, cerr)
				t.SetVerdict(mtesting.Trap, cerr.Error())
				rep.testDone(t)
				failed = true
				break loop
			}
			classifyCall(t, cerr)
			// t.Cleanup script funcs need a live process: run them in a
			// second Engine.Call against the bound __cleanups member.
			var cleanupErr error
			for len(t.Cleanups()) > 0 {
				cerr := callCleanup(ctx, e, testingPkg, t)
				if cerr != nil && cleanupErr == nil {
					cleanupErr = cerr
				}
			}
			// A cleanup failure after a dead test keeps the body's
			// PANIC/TRAP — folding its stopError into the flags would
			// downgrade the verdict. Otherwise re-evaluate the flags even
			// when cleanup returned nil: t.Error/t.Fatal inside cleanup
			// must change a PASS body into FAIL.
			if t.Verdict() != mtesting.Panic && t.Verdict() != mtesting.Trap {
				classifyCall(t, cleanupErr)
			}
			t.Finish()
			rep.testDone(t)
			if mtesting.FailedVerdict(t.Verdict()) {
				failed = true
				if o.FailFast {
					failfast = true
					suite.Halt()
				}
			}
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		rep.timedOut(o.Timeout)
		failed = true
	}
	if ran == 0 && !failed {
		rep.noTests()
	}
	rep.finish(pkg.Path, failed, time.Since(start))
	if failed {
		return ErrFailed
	}
	return nil
}

// classifyCall folds an Engine.Call error into the test's verdict.
func classifyCall(t *mtesting.T, err error) {
	if err == nil {
		setFlagVerdict(t)
		return
	}
	if st, ok := mtesting.StopOf(err); ok && st == t {
		setFlagVerdict(t)
		return
	}
	if _, ok := err.(*runtime.Panic); ok {
		t.SetVerdict(mtesting.Panic, err.Error())
		return
	}
	t.SetVerdict(mtesting.Trap, err.Error())
}

// setFlagVerdict derives the verdict from the T's own flags after a
// normally-returned (or own-sentinel) call.
func setFlagVerdict(t *mtesting.T) {
	switch {
	case t.Failed():
		t.SetVerdict(mtesting.Fail, "")
	case t.Skipped():
		t.SetVerdict(mtesting.Skip, "")
	default:
		t.SetVerdict(mtesting.Pass, "")
	}
}

// compileRun splits -run into per-level unanchored regexps.
func compileRun(run string) ([]*regexp.Regexp, error) {
	if run == "" {
		return nil, nil
	}
	var out []*regexp.Regexp
	for _, pat := range splitSlash(run) {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid -run %q: %w", run, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// splitSlash splits a -run pattern on unescaped "/" — go's level
// separator.
func splitSlash(s string) []string {
	var out []string
	var cur []rune
	esc := false
	for _, r := range s {
		if esc {
			cur = append(cur, r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			cur = append(cur, r)
			continue
		}
		if r == '/' {
			out = append(out, string(cur))
			cur = nil
			continue
		}
		cur = append(cur, r)
	}
	return append(out, string(cur))
}

// pathOfDir is the package spelling for the trailer line: the
// directory's slash-cleaned absolute path.
func pathOfDir(dir string) string {
	return filepath.ToSlash(dir)
}

// relPath spells a scanned file like declPos does — relative to the
// package dir when it nests inside it.
func relPath(dir, file string) string {
	if rel, err := filepath.Rel(dir, file); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(file)
}
