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

// collect builds the sync plan for the scanned package, BFSing into
// same-subtree imports when deps is set: only imports under the scanned
// package's own path are followed — an edge that leaves the subtree
// (e.g. app -> the tool's own scanx helper) is external to the run and
// must not be rewritten by it.
func collect(dir string, deps bool) []filePlan {
	plans := []filePlan{}
	var ex *scanx.Explorer
	visit := func(files []*inspect.File) {
		decls := []*inspect.Decl{}
		for _, f := range files {
			decls = append(decls, inspect.Decls(f)...)
		}
		for _, f := range files {
			expected := []string{}
			for _, d := range inspect.Decls(f) {
				expected = append(expected, directivesFor(ex, d, f, decls)...)
			}
			plans = append(plans, filePlan{f, scanx.Dedupe(expected)})
		}
	}

	root := inspect.DirOf(dir)
	ex = scanx.NewExplorer(inspect.Path(root))
	files := inspect.Files(root)
	visit(files)
	if !deps {
		return plans
	}
	prefix := inspect.Path(root) + "/"
	seen := map[string]bool{inspect.Path(root): true}
	queue := []string{}
	enqueue := func(fs []*inspect.File) {
		for _, f := range fs {
			for _, im := range inspect.Imports(f) {
				p := im.Path
				if strings.HasPrefix(p, prefix) && !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
	}
	enqueue(files)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		p := inspect.PackageOf(path)
		fs := inspect.Files(p)
		visit(fs)
		enqueue(fs)
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
func directivesFor(ex *scanx.Explorer, d *inspect.Decl, f *inspect.File, decls []*inspect.Decl) []string {
	out := []string{}
	if inspect.Kind(d) != "type" {
		return out
	}
	if scanx.IsAlias(d) {
		return out // an alias earns no directives of its own
	}
	name := d.Name
	def := inspect.Def(d)
	switch def.Kind {
	case "Ident":
		// enum-style: `type X int`/`string` with a const block of X
		// anywhere in the package.
		if (def.Text == "int" || def.Text == "string") && scanx.HasConstOfType(decls, name) {
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
	// oneOf variant; the same requirement on an interface marks the
	// union type itself.
	if scanx.HasMethod(d, "Discriminator", "string") ||
		scanx.RequiresMethod(d, "Discriminator", "func() string") {
		out = append(out, "//go:generate oneofgen -type="+name)
	}
	return out
}

// reachHasRequired reports whether some struct reachable from d's field
// types requests the required check. Exploration stays inside the
// scanned package's subtree (cross-package refs resolve, external and
// bound paths are never entered), each shared package is read once, and
// type cycles terminate on the visited set.
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
func hasRequiredTag(d *inspect.Decl) bool {
	for _, fd := range inspect.Fields(d) {
		if wantsRequired(fd.Tag) {
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
