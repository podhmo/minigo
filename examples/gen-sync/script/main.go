// Package script is the body of the gen-sync tool, executed by the minigo
// interpreter (see ../main.go). It walks a package through the inspect
// index layer, infers which declarations want generation tooling, and
// rewrites each file's managed //go:generate block so it stays in sync.
//
// The //go:generate lines are the OUTPUT of this tool: targets are
// inferred from the declarations themselves (type shapes, struct tags,
// method sets, names), never from magic comments.
package script

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/inspect"
)

// marker identifies the sentinel comment that opens a file's managed
// region: every //go:generate line below it belongs to gen-sync.
const marker = "managed by gen-sync"

const sentinel = "// Code generated directives below are managed by gen-sync. DO NOT EDIT."

// Main scans dir (a package directory) and syncs each file's managed
// directives. With deps it also follows same-module imports transitively.
// With check it only reports drift and writes nothing. It returns the
// number of files changed — or, in check mode, the number drifting.
func Main(dir string, check bool, deps bool) int {
	wd, _ := os.Getwd()
	files := collectFiles(dir, deps)
	changed := 0
	for _, f := range files {
		if syncFile(f, check, wd) {
			changed++
		}
	}
	return changed
}

// collectFiles lists the package's files, BFSing into same-module imports
// when deps is set. The module subtree root is the parent of the scanned
// package's own import path.
func collectFiles(dir string, deps bool) []*inspect.File {
	root := inspect.DirOf(dir)
	files := inspect.Files(root)
	if !deps {
		return files
	}
	prefix := filepath.Dir(inspect.Path(root)) + "/"
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
		files = append(files, fs...)
		enqueue(fs)
	}
	return files
}

// syncFile computes the file's expected directives and rewrites the
// managed region, returning whether the file changed (or would, in
// check mode).
func syncFile(f *inspect.File, check bool, wd string) bool {
	path := f.Name
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("gen-sync:", path, ":", err)
		return false
	}
	src := string(data)
	lines := strings.Split(src, "\n")
	decls := inspect.Decls(f)

	expected := []string{}
	for _, d := range decls {
		expected = append(expected, directivesFor(d, f, lines, decls)...)
	}
	expected = dedupe(expected)

	midx := -1
	for i, ln := range lines {
		if strings.Contains(ln, marker) {
			midx = i
			break
		}
	}
	if midx < 0 && len(expected) == 0 {
		return false // nothing to manage here
	}

	var out []string
	inserted := false
	if midx >= 0 {
		// the managed region: keep everything through the marker, drop
		// every //go:generate below it, then write the expected set.
		out = append(out, lines[:midx+1]...)
		out = append(out, expected...)
		for _, ln := range lines[midx+1:] {
			if strings.HasPrefix(strings.TrimSpace(ln), "//go:generate") {
				continue
			}
			out = append(out, ln)
		}
	} else {
		// no managed region yet: insert sentinel + block after the
		// package clause and imports.
		anchor := insertAnchor(lines)
		out = append(out, lines[:anchor]...)
		if anchor > 0 && strings.TrimSpace(lines[anchor-1]) != "" {
			out = append(out, "")
		}
		out = append(out, sentinel)
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
func directivesFor(d *inspect.Decl, f *inspect.File, lines []string, decls []*inspect.Decl) []string {
	out := []string{}
	if inspect.Kind(d) != "type" {
		return out
	}
	name := d.Name
	def := inspect.Def(d)
	switch def.Kind {
	case "Ident":
		// enum-style: `type X int`/`string` with a const block of X.
		if (def.Text == "int" || def.Text == "string") && !isAlias(d, lines) && hasConstOfType(decls, name, lines) {
			out = append(out, "//go:generate stringer -type="+name)
		}
	case "StructType":
		// field-tag inference: any field tag mentioning `required` opts
		// the struct into the (hypothetical) required-check generator.
		if hasRequiredTag(d) {
			out = append(out, "//go:generate requiredgen -type="+name)
		}
	case "InterfaceType":
		// name inference: service-shaped interfaces get a mock.
		if isMockable(name) {
			base := filepath.Base(f.Name)
			out = append(out, "//go:generate mockgen -source="+base+" -destination=mock_"+base)
		}
	}
	// method-set inference: types carrying OpenAPI-style
	// Discriminator() are oneOf variants for the unmarshal generator.
	if hasDiscriminator(d) {
		out = append(out, "//go:generate oneofgen -type="+name)
	}
	return out
}

// hasConstOfType reports whether some const spec in the file is declared
// with the type name — the enum marker the index layer can't see
// (ValueSpec types are not exposed on the Decl view, so the raw lines
// are checked).
func hasConstOfType(decls []*inspect.Decl, name string, lines []string) bool {
	for _, c := range decls {
		if inspect.Kind(c) != "const" {
			continue
		}
		n := posLine(c)
		if n <= 0 || n > len(lines) {
			continue
		}
		if specHasType(lines[n-1], name) {
			return true
		}
		// specs that omit the type inherit it from the block's first
		// spec — scan up to the enclosing `const (`.
		if !strings.Contains(lines[n-1], "=") {
			for j := n - 2; j >= 0; j-- {
				t := strings.TrimSpace(lines[j])
				if strings.HasPrefix(t, "const (") || t == "const(" {
					return specHasType(lines[j+1], name)
				}
				if t == ")" || strings.HasPrefix(t, "const ") {
					break
				}
			}
		}
	}
	return false
}

// specHasType reports whether a const spec line declares the given type
// name before its `=` (e.g. `StatusOpen Status = iota` or
// `const A, B Status = 1, 2`).
func specHasType(line string, name string) bool {
	for i, w := range strings.Fields(line) {
		if w == "=" || strings.HasPrefix(w, "=") {
			return false
		}
		if i > 0 && w == name {
			return true
		}
	}
	return false
}

// isAlias reports whether the type decl is an alias (`type X = int`)
// rather than a defined type — read off the decl's own source line.
func isAlias(d *inspect.Decl, lines []string) bool {
	n := posLine(d)
	if n <= 0 || n > len(lines) {
		return false
	}
	line := lines[n-1]
	if i := strings.Index(line, "`"); i >= 0 {
		line = line[:i] // ignore `=` inside struct tags
	}
	return strings.Contains(line, "=")
}

// hasRequiredTag reports whether any struct field carries a `required`
// tag (e.g. `required:"true"` or `validate:"required"`).
func hasRequiredTag(d *inspect.Decl) bool {
	for _, fd := range inspect.Fields(d) {
		if strings.Contains(fd.Tag, "required") {
			return true
		}
	}
	return false
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

// hasDiscriminator reports whether the type declares a Discriminator
// method — the oneOf-variant convention (a signature check is possible
// via inspect.Signature; the name alone is distinctive enough here).
func hasDiscriminator(d *inspect.Decl) bool {
	for _, m := range inspect.Methods(d) {
		if m.Name == "Discriminator" {
			return true
		}
	}
	return false
}

// insertAnchor locates the line after the package clause and import
// decls — where a fresh managed block goes. Package doc comments and
// build tags live above the clause; blank lines and comments between
// decls are skipped without moving the anchor.
func insertAnchor(lines []string) int {
	anchor := -1
	inImports := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if inImports {
			if t == ")" {
				anchor = i + 1
				inImports = false
			}
			continue
		}
		switch {
		case strings.HasPrefix(t, "package "):
			anchor = i + 1
		case anchor < 0:
			// still before the package clause
		case strings.HasPrefix(t, "import (") || t == "import(":
			inImports = true
		case strings.HasPrefix(t, "import "):
			anchor = i + 1
		case t == "" || strings.HasPrefix(t, "//"):
			// blank lines and comments don't end the search
		default:
			return anchor
		}
	}
	if anchor < 0 {
		return len(lines)
	}
	return anchor
}

// posLine parses the line number out of a decl's "file:line:col" pos.
func posLine(d *inspect.Decl) int {
	parts := strings.Split(inspect.Pos(d), ":")
	if len(parts) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-2])
	return n
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func displayPath(wd, path string) string {
	rel, err := filepath.Rel(wd, path)
	if err == nil {
		return rel
	}
	return path
}
