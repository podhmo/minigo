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
// mode, the number drifting.
func Main(dir string, check bool, deps bool) int {
	wd, _ := os.Getwd()
	plans := collect(dir, deps)
	changed := 0
	for _, p := range plans {
		if syncFile(p, check, wd) {
			changed++
		}
	}
	return changed
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
func collect(dir string, deps bool) []filePlan {
	root := inspect.DirOf(dir)
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
	return plans
}

// syncFile rewrites the file's managed region to the plan's expected
// directives, returning whether the file changed (or would, in check
// mode).
func syncFile(p filePlan, check bool, wd string) bool {
	f := p.file
	path := f.Name
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("gen-sync:", path, ":", err)
		return false
	}
	src := string(data)
	lines := strings.Split(src, "\n")
	expected := p.expected

	midx := scanx.FindSentinel(lines)
	if midx < 0 && len(expected) == 0 {
		return false // nothing to manage here
	}

	var out []string
	inserted := false
	if midx >= 0 {
		// the managed region is the sentinel plus the run of
		// //go:generate lines directly under it: regenerate the run,
		// and leave everything past it — including hand-written
		// directives — alone.
		out = append(out, lines[:midx+1]...)
		out = append(out, expected...)
		out = append(out, "")
		rest := lines[midx+1:]
		tail := rest[scanx.GenerateRunEnd(rest):]
		if len(tail) == 0 {
			tail = []string{""}
		}
		out = append(out, tail...)
	} else {
		// no managed region yet: insert sentinel + block after the
		// package clause and imports.
		anchor := scanx.InsertAnchor(lines)
		out = append(out, lines[:anchor]...)
		if anchor > 0 && strings.TrimSpace(lines[anchor-1]) != "" {
			out = append(out, "")
		}
		out = append(out, scanx.Sentinel)
		out = append(out, expected...)
		out = append(out, "")
		tail := lines[anchor:]
		for len(tail) > 0 && strings.TrimSpace(tail[0]) == "" {
			tail = tail[1:] // single blank line between block and decls
		}
		out = append(out, tail...)
		inserted = true
	}

	newsrc := strings.Join(out, "\n")
	shown := displayPath(wd, path)
	if newsrc == src {
		fmt.Println("gen-sync:", shown, "up to date")
		return false
	}
	if check {
		fmt.Println("gen-sync:", shown, "drift:", len(expected), "directive(s) out of sync")
		return true
	}
	if err := os.WriteFile(path, []byte(newsrc), 0644); err != nil {
		fmt.Println("gen-sync:", path, ":", err)
		return false
	}
	if inserted {
		fmt.Println("gen-sync:", shown, "inserted managed block ("+strconv.Itoa(len(expected)), "directive(s))")
	} else {
		fmt.Println("gen-sync:", shown, "rewrote managed block ("+strconv.Itoa(len(expected)), "directive(s))")
	}
	return true
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
		if vars := implementers(scans, s.path); len(vars) > 0 {
			gen += " -variants=" + strings.Join(vars, ",")
		}
		out = append(out, gen)
	}
	return out
}

// implementers lists the names of types across the walked import
// closure whose method set carries `Discriminator() string` — the
// requirement the interface spells, flattened over embeds. Names
// outside the interface's own package are qualified with the
// package's base name.
func implementers(scans []pkgScan, selfPath string) []string {
	vars := []string{}
	for _, s := range scans {
		for _, c := range s.decls {
			if !scanx.HasMethod(c, "Discriminator", "string") {
				continue
			}
			if s.path == selfPath {
				vars = append(vars, c.Name)
			} else {
				vars = append(vars, filepath.Base(s.path)+"."+c.Name)
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
