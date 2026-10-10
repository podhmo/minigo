// Command tmpltests runs Go's own test files for a stdlib package verbatim
// under minigo: the package source is copied into a scratch GOROOT next to
// its (renamed) *_test.go files, a generated driver calls each func TestXxx
// in its own minigo process, and per-test verdicts are printed.
//
//	go -C ./tools/tmpltests run ./                     # text/template, all tests
//	go -C ./tools/tmpltests run ./ -only 'TestExec'    # a subset
//	go -C ./tools/tmpltests run ./ -keep -work /tmp/t  # keep the scratch tree
//
// The scratch GOROOT is a symlink farm over the real one with selected
// packages replaced by shims (testing, flag, iter) and the target package
// replaced by a real directory holding the upstream sources plus the test
// files. See README.md for the rewrite table applied to the test sources.
package main

import (
	"context"
	"flag"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tmpltests:", err)
		os.Exit(1)
	}
}

var (
	pkg      = flag.String("pkg", "text/template", "stdlib package under test")
	src      = flag.String("src", "", "value for minigo --src (default: -pkg)")
	tests    = flag.String("tests", "exec_test.go,multi_test.go", "upstream test files to include, comma-separated")
	examples = flag.Bool("examples", false, "treat -tests as external <pkg>_test example files and run their Example funcs with // Output: checking")
	only     = flag.String("only", "", "run only tests whose name matches this regexp")
	work     = flag.String("work", "", "scratch work directory (default: mkdtemp)")
	keep     = flag.Bool("keep", false, "keep the work directory")
	bin      = flag.String("minigo", "", "prebuilt minigo binary (default: build ./cmd/minigo)")
	timeout  = flag.Duration("timeout", 60*time.Second, "per-test timeout")
	report   = flag.String("report", "", "also write the report to this file")
	goroot   = flag.String("goroot", "", "real GOROOT (default: build.Default.GOROOT)")
	repoRoot = flag.String("repo", "../..", "repository root holding cmd/minigo")
)

// rewrite is a textual patch applied while copying a test file. Each entry
// compensates for a minigo limitation that would otherwise keep the whole
// file (or package init) from running; every one must stay honest about the
// test's intent and is tracked in TODO.md / the README.
type rewrite struct {
	old, new string
}

// fileRewrites maps an upstream test file name to its rewrites.
var fileRewrites = map[string][]rewrite{
	"exec_test.go": {
		// minigo has no unsafe.Pointer type yet; `any` fields keep the same
		// truth values for the rows that use them ({ptr, true},{nil, false}).
		{"unsafe.Pointer", "any"},
	},
}

// srcRewrites maps a package path to per-file rewrites applied while
// copying that file into the scratch GOROOT (same mechanism as test
// files; files not listed are symlinked verbatim). The target package's
// own sources rewrite in place; a -src dependency package's directory
// is materialized — real dirs with symlinked siblings — so its listed
// files can rewrite too, since a wholesale symlinked dep is verbatim.
var srcRewrites = map[string]map[string][]rewrite{
	"text/template": {
		"exec.go": {
			// minigo's interpreter frame limit (10000) fires long before the
			// upstream exec depth cap (100000), so TestMaxExecDepth could only
			// ever observe `stack exhausted`. Lower the cap so the real guard
			// is exercised well under the frame limit.
			{"var maxExecDepth = initMaxExecDepth()", "var maxExecDepth = 250"},
		},
	},
}

// driverExtra is appended to the generated driver per package.
var driverExtra = map[string]string{}

func run() error {
	flag.Parse()
	root := *goroot
	if root == "" {
		root = build.Default.GOROOT
	}
	srcList := *src
	if srcList == "" {
		srcList = *pkg
	}
	workDir := *work
	if workDir == "" {
		d, err := os.MkdirTemp("", "tmpltests-")
		if err != nil {
			return err
		}
		workDir = d
	}
	if !*keep {
		defer os.RemoveAll(workDir)
	}
	fmt.Println("workdir:", workDir)

	minigo := *bin
	if minigo == "" {
		minigo = filepath.Join(workDir, "minigo")
		cmd := exec.Command("go", "build", "-o", minigo, "./cmd/minigo")
		cmd.Dir = *repoRoot
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build minigo: %w", err)
		}
	}

	gr := filepath.Join(workDir, "goroot")
	var onlyRe *regexp.Regexp
	if *only != "" {
		re, err := regexp.Compile(*only)
		if err != nil {
			return err
		}
		onlyRe = re
	}
	pkgDir, err := buildGOROOT(gr, root, *pkg, strings.Split(*tests, ","), onlyRe, *examples)
	if err != nil {
		return err
	}
	var names []string
	var specs map[string]exampleSpec
	if *examples {
		names, specs, err = genExampleDriver(filepath.Join(gr, "src", *pkg+"_test"), *pkg, onlyRe)
	} else {
		names, err = genDriver(pkgDir, *pkg, onlyRe)
	}
	if err != nil {
		return err
	}
	if len(names) == 0 {
		kind := "func TestXxx(*testing.T)"
		if *examples {
			kind = "func ExampleXxx() with a // Output: comment"
		}
		return fmt.Errorf("no %s found under %s", kind, pkgDir)
	}
	sort.Strings(names)

	mainDir := filepath.Join(workDir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		return err
	}
	importPath, driverPkg := *pkg, filepath.Base(*pkg)
	if *examples {
		// the driver lives in the external test package — same shape as
		// `go test`, which compiles package template_test files as an
		// importable text/template_test package.
		importPath, driverPkg = importPath+"_test", driverPkg+"_test"
	}
	mainSrc := fmt.Sprintf("package main\n\nimport (\n\t\"os\"\n\t%q\n)\n\nfunc main() {\n\t%s.RunAll(os.Args[1:])\n}\n", importPath, driverPkg)
	if err := os.WriteFile(filepath.Join(mainDir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		return err
	}

	var out strings.Builder
	pass, fail, other := 0, 0, 0
	for _, name := range names {
		var spec *exampleSpec
		if *examples {
			s := specs[name]
			spec = &s
		}
		line := runOne(minigo, mainDir, pkgDir, gr, srcList, name, *timeout, spec)
		fmt.Println(line)
		out.WriteString(line + "\n")
		switch {
		case strings.Contains(line, " PASS"):
			pass++
		case strings.Contains(line, " FAIL"), strings.Contains(line, " PANIC"):
			fail++
		default:
			other++
		}
	}
	summary := fmt.Sprintf("SUMMARY pass=%d fail=%d trap/other=%d total=%d", pass, fail, other, len(names))
	fmt.Println(summary)
	out.WriteString(summary + "\n")
	if *report != "" {
		if err := os.WriteFile(*report, []byte(out.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// buildGOROOT assembles the scratch GOROOT and returns the directory of the
// copied target package (used as the run cwd so testdata/ resolves). With
// external set, the listed test files go into <src>/<pkg>_test — the
// external test package, where upstream keeps `package <base>_test` files.
func buildGOROOT(gr, realRoot, pkg string, testFiles []string, only *regexp.Regexp, external bool) (string, error) {
	shimmed := map[string]bool{"testing": true, "flag": true, "iter": true}

	src := filepath.Join(gr, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", err
	}
	ents, err := os.ReadDir(filepath.Join(realRoot, "src"))
	if err != nil {
		return "", err
	}
	// Top-level dirs a rewritten -src dependency lives under must
	// materialize instead of linking wholesale.
	depParents := map[string]bool{}
	for dep := range srcRewrites {
		if dep != pkg {
			depParents[strings.SplitN(dep, "/", 2)[0]] = true
		}
	}
	for _, e := range ents {
		if shimmed[e.Name()] || e.Name() == pkg || strings.HasPrefix(pkg, e.Name()+"/") || depParents[e.Name()] {
			continue // replaced below, the package itself, or a parent dir of it
		}
		if err := os.Symlink(filepath.Join(realRoot, "src", e.Name()), filepath.Join(src, e.Name())); err != nil {
			return "", err
		}
	}
	for name := range shimmed {
		dir := filepath.Join(src, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte(shims[name]), 0o644); err != nil {
			return "", err
		}
	}

	// Parent dirs of the target package are real dirs of symlinks so the
	// package itself can hold test files without following a symlink.
	var pkgDir string
	parts := strings.Split(pkg, "/")
	cur := src
	for i, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		if err := os.MkdirAll(cur, 0o755); err != nil {
			return "", err
		}
		sub := filepath.Join(realRoot, "src", strings.Join(parts[:i+1], "/"))
		sents, err := os.ReadDir(sub)
		if err != nil {
			return "", err
		}
		for _, e := range sents {
			if i+1 < len(parts) && e.Name() == parts[i+1] {
				continue
			}
			if err := os.Symlink(filepath.Join(sub, e.Name()), filepath.Join(cur, e.Name())); err != nil {
				return "", err
			}
		}
	}
	pkgDir = filepath.Join(src, pkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", err
	}
	// External test files cannot live next to the package's own sources
	// (their `package <base>_test` clause belongs to a different package):
	// they go into <src>/<pkg>_test, matching how `go test` exposes them
	// as an importable package.
	testDst := pkgDir
	funcRe := testFuncRe
	if external {
		testDst = filepath.Join(src, pkg+"_test")
		funcRe = exampleFuncRe
		if err := os.MkdirAll(testDst, 0o755); err != nil {
			return "", err
		}
	}

	// Copy the package's own files: symlinks for subdirectories (e.g.
	// parse/), file copies for sources and testdata/, renamed copies for
	// the selected test files.
	real := filepath.Join(realRoot, "src", pkg)
	pents, err := os.ReadDir(real)
	if err != nil {
		return "", err
	}
	testSet := map[string]bool{}
	for _, t := range testFiles {
		testSet[t] = true
	}
	for _, e := range pents {
		name := e.Name()
		dst := filepath.Join(pkgDir, name)
		switch {
		case e.IsDir():
			if err := copyTree(filepath.Join(real, name), dst); err != nil {
				return "", err
			}
		case strings.HasSuffix(name, "_test.go"):
			if !testSet[name] {
				continue
			}
			if only != nil {
				// route around files contributing no selected tests —
				// an uncompilable one must not block --only subsets.
				b, err := os.ReadFile(filepath.Join(real, name))
				if err != nil {
					return "", err
				}
				matched := false
				for _, m := range funcRe.FindAllSubmatch(b, -1) {
					if only.MatchString(string(m[1])) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			if err := copyTestFile(filepath.Join(real, name), filepath.Join(testDst, renamedTest(name)), fileRewrites[name]); err != nil {
				return "", err
			}
		case strings.HasSuffix(name, ".go"):
			if rws, ok := srcRewrites[pkg][name]; ok {
				if err := copyTestFile(filepath.Join(real, name), dst, rws); err != nil {
					return "", err
				}
				continue
			}
			if err := os.Symlink(filepath.Join(real, name), dst); err != nil {
				return "", err
			}
		}
	}

	// -src dependency packages with rewrites materialize so their listed
	// files can patch: real dirs along the path, symlinks for siblings,
	// rewritten copies for the listed files.
	for dep, files := range srcRewrites {
		if dep == pkg {
			continue // rewritten above with the package's own sources
		}
		if err := materializeDep(gr, realRoot, dep, files); err != nil {
			return "", err
		}
	}
	return pkgDir, nil
}

// materializeDep turns a wholesale-symlinked dependency directory into a
// real one: each path segment becomes a real dir of symlinks, and files
// listed in the rewrite table copy in patched.
func materializeDep(gr, realRoot, dep string, files map[string][]rewrite) error {
	src := filepath.Join(gr, "src")
	parts := strings.Split(dep, "/")
	cur := src
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(cur); err != nil {
				return err
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		leaf := i+1 == len(parts)
		if err := os.MkdirAll(cur, 0o755); err != nil {
			return err
		}
		sub := filepath.Join(realRoot, "src", strings.Join(parts[:i+1], "/"))
		sents, err := os.ReadDir(sub)
		if err != nil {
			return err
		}
		for _, e := range sents {
			if !leaf && e.Name() == parts[i+1] {
				continue // the next materialized segment
			}
			dst := filepath.Join(cur, e.Name())
			if leaf {
				if rws, ok := files[e.Name()]; ok {
					// dst may already exist as a symlink created by the
					// target package's parent-dir loop (e.g. -pkg
					// text/template/parse vs dep text/template): writing
					// through it would patch the real GOROOT, so remove
					// it first.
					if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
						return err
					}
					if err := copyTestFile(filepath.Join(sub, e.Name()), dst, rws); err != nil {
						return err
					}
					continue
				}
			}
			if _, err := os.Lstat(dst); err == nil {
				continue // already linked by the target package's parent dirs
			}
			if err := os.Symlink(filepath.Join(sub, e.Name()), dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func renamedTest(name string) string {
	return strings.TrimSuffix(name, "_test.go") + "_srctest.go"
}

func copyTestFile(srcPath, dst string, rws []rewrite) error {
	b, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	s := string(b)
	for _, rw := range rws {
		s = strings.ReplaceAll(s, rw.old, rw.new)
	}
	return os.WriteFile(dst, []byte(s), 0o644)
}

func copyTree(srcDir, dst string) error {
	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		return os.Symlink(p, to)
	})
}

var testFuncRe = regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\)`)

// exampleFuncRe finds `func ExampleXxx()` — examples take no arguments,
// unlike TestXxx which receives *testing.T.
var exampleFuncRe = regexp.MustCompile(`(?m)^func (Example\w*)\(\s*\)\s*{`)

// exampleSpec is one example's expected output, parsed from its trailing
// `// Output:` or `// Unordered output:` comment — the pieces go test
// compares against the example's stdout.
type exampleSpec struct {
	want      string
	unordered bool
}

// scanExamples extracts each Example func's output spec from a copied test
// file. go test only executes examples carrying an Output comment, so a
// func without one never lands in the returned map.
func scanExamples(b []byte) map[string]exampleSpec {
	out := map[string]exampleSpec{}
	var cur string
	var collecting bool
	var unordered bool
	var want []string
	finish := func() {
		if cur != "" && collecting {
			out[cur] = exampleSpec{want: strings.Join(want, "\n"), unordered: unordered}
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m := exampleFuncRe.FindStringSubmatch(line); m != nil {
			finish()
			cur, collecting, unordered = m[1], false, false
			want = want[:0]
			continue
		}
		if strings.HasPrefix(line, "func ") {
			finish()
			cur = ""
			continue
		}
		if cur == "" {
			continue
		}
		trim := strings.TrimSpace(line)
		if line == "}" {
			// only the column-0 brace ends the func — indented } lines
			// close inner blocks and must not cut tracking short.
			finish()
			cur = ""
			continue
		}
		if strings.HasPrefix(trim, "//") {
			body := strings.TrimPrefix(trim, "//")
			mark := strings.TrimSpace(body)
			if strings.HasPrefix(mark, "Output:") {
				collecting, unordered = true, false
				want = want[:0]
				if rest := strings.TrimSpace(strings.TrimPrefix(mark, "Output:")); rest != "" {
					want = append(want, rest)
				}
				continue
			}
			if strings.HasPrefix(mark, "Unordered output:") {
				collecting, unordered = true, true
				want = want[:0]
				continue
			}
			if collecting {
				// one space after // is comment syntax; further indent is
				// meaningful output and kept.
				want = append(want, strings.TrimPrefix(body, " "))
			}
			continue
		}
		// the output block must be the func's trailing comment group —
		// any other source line ends it.
		if trim != "" {
			collecting = false
		}
	}
	finish()
	return out
}

// genDriver writes zzz_driver.go into pkgDir and returns the test names.
func genDriver(pkgDir, pkg string, only *regexp.Regexp) ([]string, error) {
	var names []string
	ents, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_srctest.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, m := range testFuncRe.FindAllSubmatch(b, -1) {
			if only != nil && !only.MatchString(string(m[1])) {
				continue
			}
			names = append(names, string(m[1]))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nvar srcTests = map[string]func(*testing.T){\n", filepath.Base(pkg))
	for _, n := range names {
		fmt.Fprintf(&b, "\t%q: %s,\n", n, n)
	}
	b.WriteString("}\n\n" + driverSrc)
	b.WriteString(driverExtra[pkg])
	if err := os.WriteFile(filepath.Join(pkgDir, "zzz_driver.go"), []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	return names, nil
}

// genExampleDriver writes zzz_driver.go into extDir (the <pkg>_test dir)
// and returns the runnable example names with their parsed output specs.
// Examples without an Output comment are compile-only under go test and
// are not listed.
func genExampleDriver(extDir, pkg string, only *regexp.Regexp) ([]string, map[string]exampleSpec, error) {
	var names []string
	specs := map[string]exampleSpec{}
	ents, err := os.ReadDir(extDir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_srctest.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(extDir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		found := scanExamples(b)
		for _, m := range exampleFuncRe.FindAllSubmatch(b, -1) {
			name := string(m[1])
			spec, ok := found[name]
			if !ok {
				continue
			}
			if only != nil && !only.MatchString(name) {
				continue
			}
			names = append(names, name)
			specs[name] = spec
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s_test\n\nimport \"fmt\"\n\nvar srcExamples = map[string]func(){\n", filepath.Base(pkg))
	for _, n := range names {
		fmt.Fprintf(&b, "\t%q: %s,\n", n, n)
	}
	b.WriteString("}\n\n" + exampleDriverSrc)
	if err := os.WriteFile(filepath.Join(extDir, "zzz_driver.go"), []byte(b.String()), 0o644); err != nil {
		return nil, nil, err
	}
	return names, specs, nil
}

const exampleDriverSrc = `
func RunAll(names []string) {
	for _, name := range names {
		RunExample(name)
	}
}

// RunExample runs one example between EXOUT markers. The host tool
// extracts the process stdout between BEGIN and END and compares it with
// the // Output: comment itself — an interpreted program cannot capture
// its own os.Stdout the way go test does, so the check lives outside.
func RunExample(name string) {
	f, ok := srcExamples[name]
	if !ok {
		fmt.Printf("RESULT %s NOTEST\n", name)
		return
	}
	fmt.Printf("EXOUT %s BEGIN\n", name)
	var r any
	func() {
		defer func() { r = recover() }()
		f()
	}()
	fmt.Printf("EXOUT %s END\n", name)
	if r != nil {
		fmt.Printf("RESULT %s PANIC %v\n", name, r)
		return
	}
	fmt.Printf("RESULT %s DONE\n", name)
}
`

const driverSrc = `
func RunAll(names []string) {
	for _, name := range names {
		Run1(name)
	}
}

func Run1(name string) {
	f, ok := srcTests[name]
	if !ok {
		fmt.Printf("RESULT %s NOTEST\n", name)
		return
	}
	t := testing.NewT(name)
	var r any
	func() {
		defer func() { r = recover() }()
		// cleanups run even when f panics / calls Fatal or Skip, as in go test.
		defer t.RunCleanups()
		f(t)
	}()
	if msg, ok := testing.IsSkip(r); ok {
		fmt.Printf("RESULT %s SKIP %s\n", name, msg)
		return
	}
	if r != nil && !testing.IsFailNow(r) {
		fmt.Printf("RESULT %s PANIC %v\n", name, r)
		return
	}
	if t.Failed() {
		fmt.Printf("RESULT %s FAIL %s\n", name, t.FirstError())
		return
	}
	fmt.Printf("RESULT %s PASS\n", name)
}
`

// runOne runs a single test in a fresh minigo process and returns its
// verdict line. A minigo trap kills the process, so traps are per-test.
// spec is non-nil for example runs: the RESULT line then only reports
// that the example finished, and the // Output: comparison happens here.
func runOne(minigo, mainDir, cwd, goroot, srcList, name string, timeout time.Duration, spec *exampleSpec) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, minigo, "run", mainDir, "--src", srcList, "--", name)
	cmd.Dir = cwd
	// a pre-set GOROOT must not shadow the scratch root: drop it first.
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOROOT=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "GOROOT="+goroot)
	var sb, eb strings.Builder
	cmd.Stdout, cmd.Stderr = &sb, &eb
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errTimeout
	}
	stdout, stderr := sb.String(), eb.String()
	if spec != nil {
		return scoreExample(name, spec, stdout, stderr, err)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "RESULT ") {
			return line
		}
	}
	if err == errTimeout {
		return fmt.Sprintf("RESULT %s HANG", name)
	}
	detail := firstLine(stderr)
	if detail == "" {
		detail = firstLine(stdout)
	}
	return fmt.Sprintf("RESULT %s TRAP %s", name, detail)
}

// scoreExample turns an example run into a verdict line: DONE means the
// example returned and its captured stdout is compared with the //
// Output: spec (go test semantics). A missing RESULT line means the
// process died first — a runtime trap reports TRAP, anything else (e.g.
// os.Exit under log.Fatal) failed the run.
func scoreExample(name string, spec *exampleSpec, stdout, stderr string, err error) string {
	var result string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "RESULT ") {
			result = line
			break
		}
	}
	switch {
	case result == "RESULT "+name+" DONE":
		// fall through to the output comparison
	case result != "":
		return result // NOTEST, PANIC
	case err == errTimeout:
		return fmt.Sprintf("RESULT %s HANG", name)
	default:
		detail := firstLine(stderr)
		if detail == "" {
			detail = firstLine(stdout)
		}
		if strings.Contains(stderr, "runtime trap:") {
			return fmt.Sprintf("RESULT %s TRAP %s", name, detail)
		}
		return fmt.Sprintf("RESULT %s FAIL %s", name, detail)
	}
	got := exoutBetween(stdout, name)
	if exampleOutputEqual(got, spec.want, spec.unordered) {
		return fmt.Sprintf("RESULT %s PASS", name)
	}
	return fmt.Sprintf("RESULT %s FAIL output mismatch\n  got: %s\n want: %s",
		name, indentBlock(got, "    "), indentBlock(spec.want, "    "))
}

// exoutBetween extracts the stdout between the driver's EXOUT markers.
// The END marker can be glued to a last output line without a newline;
// whatever precedes it on that line still belongs to the output.
func exoutBetween(stdout, name string) string {
	begin := "EXOUT " + name + " BEGIN\n"
	i := strings.Index(stdout, begin)
	if i < 0 {
		return ""
	}
	rest := stdout[i+len(begin):]
	if j := strings.Index(rest, "EXOUT "+name+" END"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// exampleOutputEqual compares captured stdout with the // Output: text
// like go test does: both sides trimmed, per-line trailing space dropped,
// lines sorted when the comment says `// Unordered output:`.
func exampleOutputEqual(got, want string, unordered bool) bool {
	gl, wl := outputLines(got), outputLines(want)
	if unordered {
		sort.Strings(gl)
		sort.Strings(wl)
	}
	if len(gl) != len(wl) {
		return false
	}
	for i := range gl {
		if gl[i] != wl[i] {
			return false
		}
	}
	return true
}

func outputLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return lines
}

func indentBlock(s, prefix string) string {
	if s == "" {
		return "(empty)"
	}
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

var errTimeout = fmt.Errorf("timeout")

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "Traceback") && !strings.HasPrefix(l, "File ") {
			return l
		}
	}
	return ""
}
