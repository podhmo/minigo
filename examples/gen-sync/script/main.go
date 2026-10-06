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
func Main(dir string, check bool, deps bool, explain bool) (int, error) {
	wd, _ := os.Getwd()
	rootAbs, err := filepath.Abs(dir)
	if err != nil {
		return 0, fmt.Errorf("gen-sync: resolve %s: %w", dir, err)
	}
	plans, warns, errs := collect(dir, deps, wd)
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
	var fileErrs []error
	for _, p := range plans {
		c, err := syncFile(p, check, explain, wd, rootAbs)
		if err != nil {
			fileErrs = append(fileErrs, err)
			continue
		}
		if c {
			changed++
		}
	}
	if len(fileErrs) > 0 {
		// a run that synced nine files and failed the tenth must not
		// read as a clean pass — say how many failed, not just which.
		action := "failed to write"
		if check {
			action = "could not be checked"
		}
		fmt.Printf("gen-sync: %d file(s) %s\n", len(fileErrs), action)
	}
	return changed, errors.Join(fileErrs...)
}

// filePlan pairs a file with the directives its decls want — computed
// for every file before any write, so decl positions always refer to
// the pre-sync snapshot.
type filePlan struct {
	file     *inspect.File
	expected []directive
}

// directive pairs an inferred //go:generate line with the reason it was
// inferred — `-explain` prints the reason back in input vocabulary.
type directive struct {
	line   string
	reason string
}

// dedupeDirectives drops repeat lines keeping the first occurrence —
// two rules can name the same tool line. Their reasons are kept too:
// a shared line collates every reason it earned (joined with "; ") so
// -explain does not report only the first rule's reasoning.
func dedupeDirectives(ds []directive) []directive {
	index := map[string]int{}
	out := []directive{}
	for _, d := range ds {
		if i, ok := index[d.line]; ok {
			if r := d.reason; r != "" && !strings.Contains(out[i].reason, r) {
				out[i].reason += "; " + r
			}
			continue
		}
		index[d.line] = len(out)
		out = append(out, d)
	}
	return out
}

// pkgScan is one package's contribution to the run: the files that may
// be synced and the decls that feed inference rules. foreign names the
// files whose package clause differs from the package's own — the index
// merges them (a dir of `package app` next to `package other` is one
// package to the index, while `go build` rejects it) so they must not
// feed inference or be written to either.
type pkgScan struct {
	path    string
	files   []*inspect.File
	decls   []*inspect.Decl
	foreign map[string]bool
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
func collect(dir string, deps bool, wd string) ([]filePlan, []string, []error) {
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
	warns := []string{}
	errs := []error{}
	visit := func(path string) {
		p := inspect.PackageOf(path)
		// A file whose package clause differs from the package's own is
		// foreign to the directory — `go build` reports "found packages
		// app and other" and rejects the whole dir, so silently indexing
		// it (and writing a managed block into it) misleads twice. Warn
		// and skip: no decls, no import edges, no sync target.
		pkgName := inspect.Name(p)
		decls := []*inspect.Decl{}
		files := []*inspect.File{}
		foreign := map[string]bool{}
		for _, f := range inspect.Files(p) {
			if f.PkgName != "" && f.PkgName != pkgName {
				foreign[f.Name] = true
				warns = append(warns, fmt.Sprintf("%s declares package %s, but the directory's package is %s — skipping (go build would reject the directory)", displayPath(wd, f.Name), f.PkgName, pkgName))
				continue
			}
			files = append(files, f)
			decls = append(decls, inspect.Decls(f)...)
			for _, im := range inspect.Imports(f) {
				ip := im.Path
				if strings.HasPrefix(ip, prefix) && !seen[ip] {
					seen[ip] = true
					queue = append(queue, ip)
				}
			}
		}
		scans = append(scans, pkgScan{path, files, decls, foreign})
	}
	visit(inspect.Path(root))
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		visit(path)
	}

	// A .go file the package index does not carry was dropped by
	// ctx.MatchFile — build constraints (fine, like `go build`) or an
	// unreadable file (not fine: its decls *and its import edges*
	// vanish, so directives elsewhere silently lose variants).
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
			expected := []directive{}
			for _, d := range inspect.Decls(f) {
				expected = append(expected, directivesFor(ex, scans, s, d, f, dirAbs)...)
			}
			plans = append(plans, filePlan{f, dedupeDirectives(expected)})
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
// set `go build` would see, just made visible). Foreign-package files
// count as indexed: visit already warned about them.
func droppedFiles(s pkgScan) ([]string, []error) {
	d := ""
	if len(s.files) > 0 {
		d = filepath.Dir(s.files[0].Name)
	} else {
		// every indexed file was foreign — the dir is still worth
		// checking for unreadable files the index never saw.
		for name := range s.foreign {
			d = filepath.Dir(name)
			break
		}
	}
	if d == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, []error{fmt.Errorf("gen-sync: scan %s: %w", d, err)}
	}
	indexed := map[string]bool{}
	for _, f := range s.files {
		indexed[f.Name] = true
	}
	for name := range s.foreign {
		indexed[name] = true
	}
	warns := []string{}
	errs := []error{}
	for _, e := range entries {
		name := e.Name()
		// _- and .-prefixed files are invisible to the Go build system
		// entirely — same ignore rule as go/build's MatchFile.
		if e.IsDir() || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(d, name)
		if indexed[path] {
			continue
		}
		if _, rerr := os.ReadFile(path); rerr != nil {
			// an unreadable file loses decls AND import edges — the scan
			// is degraded, so the caller refuses to write from it.
			errs = append(errs, fmt.Errorf("gen-sync: %s: %w", path, rerr))
		} else {
			// readable but outside the index: build-constraint exclusion
			// (or, rarely, a constraint that does not parse). go/build
			// would skip it the same way, so a warning is honest.
			warns = append(warns, path+": not in the package index (excluded by build constraints?)")
		}
	}
	return warns, errs
}

// syncFile rewrites the file's managed region to the plan's expected
// directives, returning whether the file changed (or would, in check
// mode). Failures — an unreadable file, an unwritable one, a file that
// resolves outside the scanned directory — are returned, not printed:
// a file the tool could not sync must never look like "no change".
func syncFile(p filePlan, check bool, explain bool, wd, rootAbs string) (bool, error) {
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
	expectedLines := make([]string, len(expected))
	for i, d := range expected {
		expectedLines[i] = d.line
	}

	// explain fires for every file whose decls earned directives,
	// written or not — a generated file's unwritten directives are
	// exactly the ones worth explaining.
	if explain {
		for _, d := range expected {
			fmt.Println("gen-sync:", shown, "explain:", d.line, "—", d.reason)
		}
	}

	// A file another generator owns ("// Code generated ... DO NOT
	// EDIT.") is not ours to edit — the next regen would discard the
	// block. Its decls still feed inference; the write is skipped
	// with a warning, not an error — generated files living inside a
	// scanned package is the normal case, not a breakage. Checked
	// before the nothing-to-do return: a generated file with no
	// directives still earns the line saying it was seen and skipped.
	if hasGeneratedMarker(lines) {
		fmt.Println("gen-sync:", shown, "skipping: another generator owns this file (// Code generated ... DO NOT EDIT.)")
		return false, nil
	}

	midx := scanx.FindSentinel(lines)
	if midx < 0 && len(expected) == 0 {
		return false, nil // nothing to manage here
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
		pushNew(expectedLines...)
		rest := lines[midx+1:]
		runEnd := scanx.GenerateRunEnd(rest)
		// keep duplicates: a doubled directive is a line the rewrite
		// removes, so it must surface in the diff and the count.
		have := nonBlank(rest[:runEnd])
		dropped = missingFrom(have, expectedLines)
		added = missingFrom(expectedLines, have)
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
		pushNew(expectedLines...)
		added = expectedLines
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
// EDIT.` comment line before the package clause. gen-sync's own
// sentinel matches that pattern; it is never a refusal.
func hasGeneratedMarker(lines []string) bool {
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == scanx.Sentinel {
			continue
		}
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
// independent: a decl can earn several directives, or none. Each line
// carries its reason — the inference path `-explain` prints.
func directivesFor(ex *scanx.Explorer, scans []pkgScan, s pkgScan, d *inspect.Decl, f *inspect.File, rootAbs string) []directive {
	out := []directive{}
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
		// anywhere in the package — counting only consts declared in
		// this package's own files (a foreign-package file's consts
		// cannot legally type against the enum).
		members := []*inspect.Decl{}
		for _, m := range inspect.EnumMembers(d) {
			if !s.foreign[m.File] {
				members = append(members, m)
			}
		}
		if (def.Text == "int" || def.Text == "string") && len(members) > 0 {
			out = append(out, directive{
				line:   "//go:generate stringer -type=" + name,
				reason: fmt.Sprintf("enum: defined type + %d const member(s), first in %s", len(members), displayPath(rootAbs, members[0].File)),
			})
		}
	case "StructType":
		// field-tag inference, recursively: a struct opts into the
		// (hypothetical) generator when it — or any struct reachable
		// through its field types — requests the required check.
		if hasRequiredTag(d) {
			out = append(out, directive{
				line:   "//go:generate requiredgen -type=" + name,
				reason: "struct: field tag requests required in " + displayPath(rootAbs, f.Name),
			})
		} else if reachHasRequired(ex, d) {
			out = append(out, directive{
				line:   "//go:generate requiredgen -type=" + name,
				reason: "struct: a reachable field type requests required",
			})
		}
	case "InterfaceType":
		// name inference: service-shaped interfaces get a mock.
		if isMockable(name) {
			base := filepath.Base(f.Name)
			out = append(out, directive{
				line:   "//go:generate mockgen -source=" + base + " -destination=mock_" + base,
				reason: "interface: name " + name + " matches the service suffixes",
			})
		}
	}
	// method-set inference: a concrete `Discriminator() string` marks a
	// oneOf variant; the same requirement on an interface marks the union
	// type itself, and collects its implementers as -variants=.
	if m := scanx.MethodNamed(d, s.foreign, "Discriminator", "string"); m != nil {
		reason := "method set carries Discriminator() string"
		if m.Via != nil {
			reason += ", promoted from " + m.Via.Name
		}
		if m.Decl != nil {
			reason += ", declared in " + displayPath(rootAbs, m.Decl.File)
		}
		out = append(out, directive{
			line:   "//go:generate oneofgen -type=" + name,
			reason: reason,
		})
	} else if scanx.RequiresMethod(d, "Discriminator", "func() string") {
		gen := "//go:generate oneofgen -type=" + name
		reason := "interface requires Discriminator() in " + displayPath(rootAbs, f.Name)
		if vars := implementers(scans, s.path, d); len(vars) > 0 {
			gen += " -variants=" + strings.Join(vars, ",")
			reason += fmt.Sprintf("; %d implementer(s) found in the scanned subtree", len(vars))
		} else {
			reason += "; no implementers found"
		}
		out = append(out, directive{line: gen, reason: reason})
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
			if s.foreign[c.File] {
				continue // declared in a foreign-package file
			}
			if !scanx.HasMethod(c, s.foreign, "Discriminator", "string") {
				// qualifies only through a method declared in a foreign
				// file — inspect.Implementers reads the merged method
				// table, which a skipped file still feeds. The caller
				// gates on RequiresMethod(Discriminator), so the union
				// marker is what a candidate must carry on its own.
				continue
			}
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
