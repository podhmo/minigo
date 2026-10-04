// Package script is the body of the gen-sync tool, executed by the minigo
// interpreter (see ../main.go). It walks a package through the inspect
// API, infers which declarations want generation tooling, and
// rewrites each file's managed //go:generate block so it stays in sync.
//
// The //go:generate lines are the OUTPUT of this tool: targets are
// inferred from the declarations themselves (type shapes, struct tags,
// method sets, names), never from magic comments. Scan mechanics live in
// the scanx package — this file keeps only the policy: which signal
// wants which directive.
package script

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/examples/gen-sync/scanx"
	"github.com/podhmo/minigo/inspect"
)

// Main scans dir (a package directory) and syncs each file's managed
// directives. With deps it also follows imports inside the scanned
// package's subtree transitively. With check it only reports drift and
// writes nothing. It returns the number of files changed — or, in check
// mode, the number drifting — plus an error joining every file the run
// could not account for. A nonzero error means the reported count did
// not see the whole picture; success is never faked.
func Main(dir string, check bool, deps bool) (int, error) {
	wd, _ := os.Getwd()
	rootAbs, err := filepath.Abs(dir)
	if err != nil {
		return 0, fmt.Errorf("gen-sync: resolve %s: %w", dir, err)
	}
	plans, warns, errs := collect(dir, deps)
	for _, w := range warns {
		fmt.Println("gen-sync:", w)
	}
	if len(errs) > 0 {
		// the scan itself is degraded (a file it should see is
		// unreadable, or the dir has no module context) — any managed
		// block written now could regress, so refuse to write at all.
		return 0, errors.Join(errs...)
	}
	changed := 0
	for _, p := range plans {
		c, err := syncFile(p, check, wd, rootAbs)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if c {
			changed++
		}
	}
	return changed, errors.Join(errs...)
}

// filePlan pairs a file with the directives its decls want — computed
// for every file before any write, so decl positions always refer to
// the pre-sync snapshot.
type filePlan struct {
	file     *inspect.File
	expected []string
}

// pkgScan is one package's contribution to the run: the files that may
// be synced and the decls that feed inference rules.
type pkgScan struct {
	path  string
	files []*inspect.File
	decls []*inspect.Decl
}

// collect builds the sync plan for the scanned package. It always walks
// the in-subtree import closure — the implementer search space and the
// reference-exploration scope both see the whole subtree regardless of
// deps — but only the scanned package's own files become sync targets
// unless deps is set. Edges that leave the subtree (e.g. app -> the
// tool's own scanx helper) are never followed: external packages are
// neither read nor written.
//
// Alongside the plans it reports warnings (files skipped the way `go
// build` would skip them — the run may continue) and errors (the scan
// is degraded — the caller refuses to write from it).
func collect(dir string, deps bool) ([]filePlan, []string, []error) {
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, []error{fmt.Errorf("gen-sync: resolve %s: %w", dir, err)}
	}
	if moduleRoot(dirAbs) == "" {
		// outside any module the import path is synthetic:
		// cross-package references never resolve, so requiredgen /
		// -variants inference would silently shrink and the regressed
		// output would be written. Fail instead.
		return nil, nil, []error{fmt.Errorf("gen-sync: %s is outside any Go module; references cannot resolve and the scan would silently degrade", dir)}
	}
	root := inspect.DirOf(dir)
	if strings.HasPrefix(inspect.Path(root), "<dir>") {
		// the same condition seen from the other side: the dir exists
		// inside a module tree but the resolver could not place it.
		return nil, nil, []error{fmt.Errorf("gen-sync: %s resolves to a synthetic package path %q; the scan would silently degrade", dir, inspect.Path(root))}
	}
	ex := scanx.NewExplorer(inspect.Path(root))
	prefix := inspect.Path(root) + "/"
	seen := map[string]bool{inspect.Path(root): true}
	queue := []string{}
	scans := []pkgScan{}
	visit := func(path string, files []*inspect.File) {
		decls := []*inspect.Decl{}
		for _, f := range files {
			decls = append(decls, inspect.Decls(f)...)
			for _, im := range inspect.Imports(f) {
				p := im.Path
				if strings.HasPrefix(p, prefix) && !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
		scans = append(scans, pkgScan{path, files, decls})
	}
	visit(inspect.Path(root), inspect.Files(root))
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		visit(path, inspect.Files(inspect.PackageOf(path)))
	}

	// A .go file the package index does not carry was dropped by
	// ctx.MatchFile — build constraints (fine, like `go build`) or an
	// unreadable file (not fine: its decls *and its import edges*
	// vanish, so directives elsewhere silently lose variants).
	warns := []string{}
	errs := []error{}
	for _, s := range scans {
		w, e := droppedFiles(s)
		warns = append(warns, w...)
		errs = append(errs, e...)
	}

	limit := len(scans)
	if !deps {
		limit = 1 // scans[0] is always the root package
	}
	plans := []filePlan{}
	for _, s := range scans[:limit] {
		for _, f := range s.files {
			expected := []string{}
			for _, d := range inspect.Decls(f) {
				expected = append(expected, directivesFor(ex, scans, s, d, f)...)
			}
			plans = append(plans, filePlan{f, scanx.Dedupe(expected)})
		}
	}
	return plans, warns, errs
}

// moduleRoot walks up from dir until a go.mod appears and returns its
// directory — "" when the tree is not a module (references cannot
// resolve there).
func moduleRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// droppedFiles lists the .go files present on disk in a scanned
// package's directory but absent from the package index. Unreadable
// ones are errors (the scan is missing decls it cannot even name);
// readable-but-excluded ones are warnings (build constraints — the same
// set `go build` would see, just made visible).
func droppedFiles(s pkgScan) ([]string, []error) {
	if len(s.files) == 0 {
		return nil, nil
	}
	d := filepath.Dir(s.files[0].Name)
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, []error{fmt.Errorf("gen-sync: scan %s: %w", d, err)}
	}
	indexed := map[string]bool{}
	for _, f := range s.files {
		indexed[f.Name] = true
	}
	warns := []string{}
	errs := []error{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(d, name)
		if indexed[path] {
			continue
		}
		data, rerr := os.ReadFile(path)
		switch {
		case rerr != nil:
			errs = append(errs, fmt.Errorf("gen-sync: %s: %w", path, rerr))
		case exclusionVisible(string(data), name):
			warns = append(warns, path+": not in the package index (excluded by build constraints?)")
		default:
			// go/build drops a file not only on a constraint miss but
			// also when MatchFile cannot parse its constraint lines —
			// that is a broken file masquerading as an exclusion, and
			// its decls vanish all the same.
			errs = append(errs, fmt.Errorf("gen-sync: %s: skipped: the Go build system could not parse it (malformed build constraint?)", path))
		}
	}
	return warns, errs
}

// exclusionVisible reports whether a readable file missing from the
// package index carries a legible reason for exclusion — a well-formed
// //go:build or // +build marker, or a _GOOS/_GOARCH filename suffix.
// A marker that does not parse is not an exclusion: it is the error
// that made go/build drop the file.
func exclusionVisible(src, name string) bool {
	marker := false
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "//go:build") {
			marker = true
			if !goBuildExprOK(strings.TrimSpace(t[len("//go:build"):])) {
				return false
			}
		}
		if strings.HasPrefix(t, "// +build") {
			marker = true
			if !plusBuildExprOK(strings.TrimSpace(t[len("// +build"):])) {
				return false
			}
		}
	}
	return marker || platformSuffixExcluded(name)
}

// goBuildExprOK validates a //go:build constraint expression — the
// grammar go/build enforces: ||, &&, !, parens and tag literals.
func goBuildExprOK(s string) bool {
	bs := []byte(s)
	pos := 0
	pos, ok := buildOr(bs, pos)
	if !ok {
		return false
	}
	pos = buildSkipWS(bs, pos)
	return pos == len(bs)
}

func buildOr(bs []byte, pos int) (int, bool) {
	pos, ok := buildAnd(bs, pos)
	if !ok {
		return pos, false
	}
	for {
		save := pos
		pos = buildSkipWS(bs, pos)
		if pos+1 < len(bs) && bs[pos] == '|' && bs[pos+1] == '|' {
			pos, ok = buildAnd(bs, pos+2)
			if !ok {
				return pos, false
			}
			continue
		}
		return save, true
	}
}

func buildAnd(bs []byte, pos int) (int, bool) {
	pos, ok := buildAtom(bs, pos)
	if !ok {
		return pos, false
	}
	for {
		save := pos
		pos = buildSkipWS(bs, pos)
		if pos+1 < len(bs) && bs[pos] == '&' && bs[pos+1] == '&' {
			pos, ok = buildAtom(bs, pos+2)
			if !ok {
				return pos, false
			}
			continue
		}
		return save, true
	}
}

func buildAtom(bs []byte, pos int) (int, bool) {
	pos = buildSkipWS(bs, pos)
	if pos < len(bs) && bs[pos] == '!' {
		return buildAtom(bs, pos+1)
	}
	if pos < len(bs) && bs[pos] == '(' {
		pos, ok := buildOr(bs, pos+1)
		if !ok {
			return pos, false
		}
		pos = buildSkipWS(bs, pos)
		if pos >= len(bs) || bs[pos] != ')' {
			return pos, false
		}
		return pos + 1, true
	}
	return buildTag(bs, pos)
}

func buildSkipWS(bs []byte, pos int) int {
	for pos < len(bs) && (bs[pos] == ' ' || bs[pos] == '\t') {
		pos++
	}
	return pos
}

func buildTag(bs []byte, pos int) (int, bool) {
	start := pos
	for pos < len(bs) && isTagChar(bs[pos]) {
		pos++
	}
	return pos, pos > start
}

func isTagChar(c byte) bool {
	return c == '_' || c == '.' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isTag(s string) bool {
	bs := []byte(s)
	for _, c := range bs {
		if !isTagChar(c) {
			return false
		}
	}
	return len(bs) > 0
}

// plusBuildExprOK validates a legacy // +build line: space-separated
// OR fields of comma-separated AND tags, each optionally !-negated.
func plusBuildExprOK(s string) bool {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		for _, tag := range strings.Split(f, ",") {
			if strings.HasPrefix(tag, "!") {
				tag = tag[1:]
			}
			if !isTag(tag) {
				return false
			}
		}
	}
	return true
}

// platformSuffixExcluded reports whether the filename itself explains
// the exclusion — a _GOOS, _GOARCH, or _GOOS_GOARCH suffix before .go.
var knownPlatforms = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mipsle": true, "mips64": true, "mips64le": true,
	"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "sparc64": true, "wasm": true,
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true,
}

func platformSuffixExcluded(name string) bool {
	base, _ := strings.CutSuffix(name, ".go")
	parts := strings.Split(base, "_")
	if knownPlatforms[parts[len(parts)-1]] {
		return true
	}
	return len(parts) >= 3 && knownPlatforms[parts[len(parts)-2]]
}

// syncFile rewrites the file's managed region to the plan's expected
// directives, returning whether the file changed (or would, in check
// mode). Failures — an unreadable file, an unwritable one, a file that
// resolves outside the scanned directory — are returned, not printed:
// a file the tool could not sync must never look like "no change".
func syncFile(p filePlan, check bool, wd, rootAbs string) (bool, error) {
	f := p.file
	path := f.Name
	shown := displayPath(wd, path)
	// The plan's file names come from the package index, which keys on
	// import paths: a dir arg whose import path is claimed by another
	// tree (module shadowing, a stale cache) would silently rewrite
	// files the caller never pointed at. Refuse to leave rootAbs.
	if rel, err := filepath.Rel(rootAbs, path); err != nil || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, `..\`) || filepath.IsAbs(rel) {
		return false, fmt.Errorf("gen-sync: %s: refusing to write outside the scanned directory %s", shown, rootAbs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("gen-sync: %s: %w", shown, err)
	}
	src := string(data)
	lines, ends, sep := splitLines(src)
	expected := p.expected

	midx := scanx.FindSentinel(lines)
	if midx < 0 && len(expected) == 0 {
		return false, nil // nothing to manage here
	}

	// A file another generator owns ("// Code generated ... DO NOT
	// EDIT.") is not ours to edit — the next regen would discard the
	// block and check mode would report drift forever. Its decls still
	// feed inference; the write is what is refused.
	if hasGeneratedMarker(lines) {
		return false, fmt.Errorf("gen-sync: %s: refusing to manage a file marked \"// Code generated ... DO NOT EDIT.\"", shown)
	}

	// out/outEnds are parallel: untouched lines keep their own line
	// endings (mixed-EOL files stay mixed), only lines the tool writes
	// take the file's dominant separator.
	var out []string
	var outEnds []string
	push := func(ls, es []string) {
		out = append(out, ls...)
		outEnds = append(outEnds, es...)
	}
	pushNew := func(ls ...string) {
		for _, l := range ls {
			out = append(out, l)
			outEnds = append(outEnds, sep)
		}
	}
	inserted := false
	var dropped, added []string
	if midx >= 0 {
		// the managed region is the sentinel plus the run of
		// //go:generate lines directly under it: regenerate the run,
		// and leave everything past it — including hand-written
		// directives — alone.
		push(lines[:midx+1], ends[:midx+1])
		pushNew(expected...)
		rest := lines[midx+1:]
		runEnd := scanx.GenerateRunEnd(rest)
		// keep duplicates: a doubled directive is a line the rewrite
		// removes, so it must surface in the diff and the count.
		have := nonBlank(rest[:runEnd])
		dropped = missingFrom(have, expected)
		added = missingFrom(expected, have)
		// an empty tail means the managed run reached EOF: the
		// directives' own line ends already yield the file's trailing
		// newline — a separator here would leave a stray blank line.
		if runEnd < len(rest) {
			pushNew("")
		}
		push(rest[runEnd:], ends[midx+1+runEnd:])
	} else {
		// no managed region yet: insert sentinel + block after the
		// package clause and imports.
		anchor := scanx.InsertAnchor(lines)
		push(lines[:anchor], ends[:anchor])
		if anchor > 0 && strings.TrimSpace(lines[anchor-1]) != "" {
			pushNew("")
		}
		pushNew(scanx.Sentinel)
		pushNew(expected...)
		added = expected
		k := anchor
		for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
			k++ // single blank line between block and decls
		}
		if k < len(lines) {
			pushNew("")
		}
		push(lines[k:], ends[k:])
		inserted = true
	}

	newsrc := joinLines(out, outEnds)
	if newsrc == src {
		fmt.Println("gen-sync:", shown, "up to date")
		return false, nil
	}
	// say which directives the change drops and adds — "rewrote (5
	// directive(s))" alone cannot distinguish a stale cleanup from a
	// regression.
	for _, l := range dropped {
		fmt.Println("gen-sync:", shown, "- "+l)
	}
	for _, l := range added {
		fmt.Println("gen-sync:", shown, "+ "+l)
	}
	delta := deltaNote(len(dropped), len(added))
	if check {
		fmt.Println("gen-sync:", shown, "drift:", len(expected), "directive(s) out of sync"+delta)
		return true, nil
	}
	if err := os.WriteFile(path, []byte(newsrc), 0644); err != nil {
		return false, fmt.Errorf("gen-sync: %s: %w", shown, err)
	}
	if inserted {
		fmt.Println("gen-sync:", shown, "inserted managed block ("+strconv.Itoa(len(expected)), "directive(s))")
	} else {
		fmt.Println("gen-sync:", shown, "rewrote managed block ("+strconv.Itoa(len(expected)), "directive(s))"+delta)
	}
	return true, nil
}

// splitLines splits src on \n like strings.Split but also reports the
// terminator that followed each line ("\r\n", "\n", or "" for the
// final fragment of a file without a trailing newline) plus the
// file's dominant separator — "\r\n" when any CRLF is present. A
// mixed-EOL file keeps its own endings on lines the tool never
// touched; only lines it writes take the dominant one.
func splitLines(src string) ([]string, []string, string) {
	sep := "\n"
	if strings.Contains(src, "\r\n") {
		sep = "\r\n"
	}
	lines := strings.Split(src, "\n")
	ends := make([]string, len(lines))
	for i, ln := range lines {
		switch {
		case i == len(lines)-1:
			ends[i] = ""
		case strings.HasSuffix(ln, "\r"):
			lines[i] = ln[:len(ln)-1]
			ends[i] = "\r\n"
		default:
			ends[i] = "\n"
		}
	}
	return lines, ends, sep
}

// joinLines concatenates each line with its recorded terminator — the
// inverse of splitLines.
func joinLines(lines, ends []string) string {
	out := ""
	for i, ln := range lines {
		out += ln + ends[i]
	}
	return out
}

// hasGeneratedMarker reports whether the file carries the Go
// convention's generated-file marker — a `// Code generated ... DO NOT
// EDIT.` comment line before the package clause.
func hasGeneratedMarker(lines []string) bool {
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "package ") {
			return false
		}
		if strings.HasPrefix(t, "// Code generated ") && strings.Contains(t, "DO NOT EDIT") {
			return true
		}
	}
	return false
}

// nonBlank drops empty entries from a managed run's lines.
func nonBlank(lines []string) []string {
	out := []string{}
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// missingFrom returns the elements of xs not covered by ys, in order,
// counting multiplicity: a line twice in xs and once in ys reports its
// extra copy — a duplicated managed directive the rewrite removes must
// show up as a drop, not vanish uncounted.
func missingFrom(xs, ys []string) []string {
	have := map[string]int{}
	for _, y := range ys {
		have[y]++
	}
	out := []string{}
	for _, x := range xs {
		if have[x] > 0 {
			have[x]--
			continue
		}
		out = append(out, x)
	}
	return out
}

// deltaNote renders the dropped/added counts a managed-block change
// made, e.g. "; dropped 1, added 2" — empty when nothing moved.
func deltaNote(dropped, added int) string {
	parts := []string{}
	if dropped > 0 {
		parts = append(parts, "dropped "+strconv.Itoa(dropped))
	}
	if added > 0 {
		parts = append(parts, "added "+strconv.Itoa(added))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, ", ")
}

// directivesFor infers the directives a declaration wants. Every rule is
// independent: a decl can earn several directives, or none.
func directivesFor(ex *scanx.Explorer, scans []pkgScan, s pkgScan, d *inspect.Decl, f *inspect.File) []string {
	out := []string{}
	if inspect.Kind(d) != "type" {
		return out
	}
	if inspect.IsAlias(d) {
		return out // an alias earns no directives of its own
	}
	name := d.Name
	def := inspect.Def(d)
	switch def.Kind {
	case "Ident":
		// enum-style: `type X int`/`string` with a const of X declared
		// anywhere in the package.
		if (def.Text == "int" || def.Text == "string") && len(inspect.EnumMembers(d)) > 0 {
			out = append(out, "//go:generate stringer -type="+name)
		}
	case "StructType":
		// field-tag inference, recursively: a struct opts into the
		// (hypothetical) generator when it — or any struct reachable
		// through its field types — requests the required check.
		if hasRequiredTag(d) || reachHasRequired(ex, d) {
			out = append(out, "//go:generate requiredgen -type="+name)
		}
	case "InterfaceType":
		// name inference: service-shaped interfaces get a mock.
		if isMockable(name) {
			base := filepath.Base(f.Name)
			out = append(out, "//go:generate mockgen -source="+base+" -destination=mock_"+base)
		}
	}
	// method-set inference: a concrete `Discriminator() string` marks a
	// oneOf variant; the same requirement on an interface marks the union
	// type itself, and collects its implementers as -variants=.
	if scanx.HasMethod(d, "Discriminator", "string") {
		out = append(out, "//go:generate oneofgen -type="+name)
	} else if scanx.RequiresMethod(d, "Discriminator", "func() string") {
		gen := "//go:generate oneofgen -type=" + name
		if vars := implementers(scans, s.path, d); len(vars) > 0 {
			gen += " -variants=" + strings.Join(vars, ",")
		}
		out = append(out, gen)
	}
	return out
}

// implementers lists the names of types across the walked import
// closure that satisfy the interface — `inspect.Implementers` is the
// index-level subtype lookup per scanned package; the name check and
// signature match (params, results, variadicity via SameType) all
// happen inside it. Interface decls are skipped: a variants list
// wants concrete types. Names outside the interface's own package are
// qualified by the package's clause name (a directory can spell a
// different name than the package it holds).
func implementers(scans []pkgScan, selfPath string, iface *inspect.Decl) []string {
	vars := []string{}
	for _, s := range scans {
		sp := inspect.SourceOf(s.path)
		for _, c := range inspect.Implementers(sp, iface) {
			def := inspect.Def(c)
			if def != nil && def.Kind == "InterfaceType" {
				continue // a variants list wants concrete types
			}
			if s.path == selfPath {
				vars = append(vars, c.Name)
			} else {
				vars = append(vars, inspect.Name(sp)+"."+c.Name)
			}
		}
	}
	sort.Strings(vars)
	return vars
}

// reachHasRequired reports whether some struct reachable from d's field
// types requests the required check. Exploration stays inside the
// scanned package's subtree (cross-package refs resolve through
// SourceOf — bound paths are entered via their source index, external
// ones are never entered), each shared package is read once, and type
// cycles terminate on the visited set.
func reachHasRequired(ex *scanx.Explorer, d *inspect.Decl) bool {
	found := false
	ex.Reach(d, func(nd *inspect.Decl) bool {
		def := inspect.Def(nd)
		if def != nil && def.Kind == "StructType" && hasRequiredTag(nd) {
			found = true
			return false // stop early
		}
		return true
	})
	return found
}

// hasRequiredTag reports whether any struct field's tag requests the
// required check: `required:"true"`, or `required` as a whole element
// of the validate/binding lists. Textual substrings — a `notrequired`
// key, `json:"required"`, `binding:"notrequired"` — do not count.
// Tags inside anonymous struct fields count: their fields are part of
// this decl's shape, so `F struct{ W string `+"`required:\"true\"`"+` }`
// marks the decl required-bearing.
func hasRequiredTag(d *inspect.Decl) bool {
	return fieldsWantRequired(inspect.Fields(d))
}

// fieldsWantRequired walks a field list — descending into anonymous
// struct spellings and the composites that can carry one ([]struct,
// map[string]struct, *struct).
func fieldsWantRequired(fs []*inspect.Field) bool {
	for _, fd := range fs {
		if wantsRequired(fd.Tag) {
			return true
		}
		if fd.Type != nil && typeHasRequiredField(fd.Type) {
			return true
		}
	}
	return false
}

// typeHasRequiredField reports whether a type expression is, or
// composes, an anonymous struct with a required-bearing field.
func typeHasRequiredField(te *inspect.TypeExpr) bool {
	if te.Kind == "StructType" {
		for _, fd := range inspect.TypeFields(te) {
			if wantsRequired(fd.Tag) || (fd.Type != nil && typeHasRequiredField(fd.Type)) {
				return true
			}
		}
		return false
	}
	for _, c := range inspect.Children(te) {
		if typeHasRequiredField(c) {
			return true
		}
	}
	return false
}

func wantsRequired(tag string) bool {
	if v, ok := scanx.LookupTag(tag, "required"); ok && v == "true" {
		return true
	}
	return scanx.TagHasElement(tag, "validate", "required") ||
		scanx.TagHasElement(tag, "binding", "required")
}

// isMockable reports whether an interface name looks like a service
// boundary worth a mock (Service/Store/Client/Repository suffixes).
func isMockable(name string) bool {
	for _, suf := range []string{"Service", "Store", "Client", "Repository"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func displayPath(wd, path string) string {
	rel, err := filepath.Rel(wd, path)
	if err == nil {
		return rel
	}
	return path
}
