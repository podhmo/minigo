// Command todo-gc moves completed entries from TODO.md to TODO-archive.md.
//
// Every archived item becomes one physical line that starts with its
// ancestor chain, so a grep hit on any word names the whole path:
//
//   - [x] Parent > Child > **Grandchild**: text joined from wrapped lines
//
// A completed item is archived together with its children, each on its own
// line, unless one of them is still open; such a subtree is reported and
// left in TODO.md. Notes without a checkbox (e.g. "完了の条件: ...") follow
// their owner and get the chain too, without a checkbox.
//
// The archive itself is normalized on every run: entries written before
// the one-line rule (wrapped lines, nested children, "> parent: A › B"
// context lines) are rewritten into the same form. Running twice changes
// nothing.
//
// Without -w the result is only described and the exit status is 1 when
// any file would change. With -w both files are rewritten.
//
// The tool is its own module inside the todo-md skill directory, so the
// argument is the repository root relative to this directory. From the
// repository root:
//
//	go -C .claude/skills/todo-md/todo-gc run . ../../../..
//	go -C .claude/skills/todo-md/todo-gc run . -w ../../../..
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

func main() {
	write := flag.Bool("w", false, "rewrite TODO.md and TODO-archive.md")
	flag.Parse()
	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	if err := run(root, *write); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, write bool) error {
	todoPath := filepath.Join(root, "TODO.md")
	archivePath := filepath.Join(root, "TODO-archive.md")
	todo, err := os.ReadFile(todoPath)
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	res := GC(string(todo), string(archive))
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	changed := res.TODO != string(todo) || res.Archive != string(archive)
	fmt.Fprintf(os.Stderr, "%d item(s) moved to the archive\n", res.Moved)
	if !write {
		if changed {
			return fmt.Errorf("TODO.md or TODO-archive.md would change; rerun with -w")
		}
		return nil
	}
	if err := os.WriteFile(todoPath, []byte(res.TODO), 0o644); err != nil {
		return err
	}
	return os.WriteFile(archivePath, []byte(res.Archive), 0o644)
}

// Result is the outcome of one GC pass.
type Result struct {
	TODO     string
	Archive  string
	Moved    int // archived lines that came from TODO.md
	Warnings []string
}

// GC moves the completed items of todo to the end of archive and
// normalizes archive into one line per item.
func GC(todo, archive string) Result {
	var res Result

	todoDoc := parse(todo)
	var moved []string
	todoDoc.items = sweep(todoDoc.items, &moved, &res.Warnings)
	res.Moved = len(moved)
	res.TODO = squeezeBlank(todoDoc.render())

	archiveLines := normalizeArchive(archive)
	if len(moved) > 0 {
		for len(archiveLines) > 0 && archiveLines[len(archiveLines)-1] == "" {
			archiveLines = archiveLines[:len(archiveLines)-1]
		}
		archiveLines = append(archiveLines, "")
		archiveLines = append(archiveLines, moved...)
	}
	res.Archive = strings.Join(archiveLines, "\n") + "\n"
	return res
}

// node is one list item: its marker line, wrapped continuation lines, and
// nested items, all kept raw so untouched items render back unchanged.
type node struct {
	indent   int
	raw      []string // the marker line followed by continuation lines
	children []*node
}

// segment is either a run of passthrough lines or one top-level item.
type segment struct {
	lines []string
	item  *node
}

type document struct {
	items []segment
}

var markerRE = regexp.MustCompile(`^( *)- (.*)$`)

func parse(src string) *document {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	doc := &document{}
	var stack []*node
	for _, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		// An indented marker with nothing open starts a top-level item too:
		// older archive entries were copied with their original indentation.
		if markerRE.MatchString(line) {
			n := &node{indent: indent, raw: []string{line}}
			for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				doc.items = append(doc.items, segment{item: n})
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
			continue
		}
		if strings.TrimSpace(line) != "" && indent > 0 && len(stack) > 0 {
			for len(stack) > 1 && stack[len(stack)-1].indent+2 > indent {
				stack = stack[:len(stack)-1]
			}
			top := stack[len(stack)-1]
			top.raw = append(top.raw, line)
			continue
		}
		stack = nil
		if k := len(doc.items); k > 0 && doc.items[k-1].item == nil {
			doc.items[k-1].lines = append(doc.items[k-1].lines, line)
		} else {
			doc.items = append(doc.items, segment{lines: []string{line}})
		}
	}
	return doc
}

func (d *document) render() string {
	var out []string
	for _, s := range d.items {
		if s.item == nil {
			out = append(out, s.lines...)
			continue
		}
		out = s.item.appendRaw(out)
	}
	return strings.Join(out, "\n") + "\n"
}

func (n *node) appendRaw(out []string) []string {
	out = append(out, n.raw...)
	for _, c := range n.children {
		out = c.appendRaw(out)
	}
	return out
}

type state int

const (
	note state = iota // "- text" without a checkbox
	open              // "- [ ]" or "- [-]"
	done              // "- [x]"
)

var checkboxRE = regexp.MustCompile(`^\[([ x-])\] `)

// split returns the state of n and its text with the wrapped lines joined.
func (n *node) split() (state, string) {
	text := markerRE.FindStringSubmatch(n.raw[0])[2]
	for _, l := range n.raw[1:] {
		text = joinWrapped(text, strings.TrimSpace(l))
	}
	st := note
	if m := checkboxRE.FindStringSubmatch(text); m != nil {
		text = text[len(m[0]):]
		st = open
		if m[1] == "x" {
			st = done
		}
	}
	return st, text
}

// joinWrapped joins two halves of a wrapped line. Between two non-ASCII
// characters (Japanese), or next to Japanese punctuation, the line break
// stood for nothing; elsewhere it stood for a space.
func joinWrapped(a, b string) string {
	last, _ := utf8.DecodeLastRuneInString(a)
	first, _ := utf8.DecodeRuneInString(b)
	if (last >= utf8.RuneSelf && first >= utf8.RuneSelf) ||
		strings.ContainsRune(closingPunct, last) || strings.ContainsRune(openingPunct, first) {
		return a + b
	}
	return a + " " + b
}

const (
	closingPunct = "、。，．）」』】"
	openingPunct = "（「『【"
)

var boldRE = regexp.MustCompile(`^\*\*(.+?)\*\*`)

// title is how an item is named in its descendants' chains: the bold
// title, else the text before the first colon, else the whole text.
func title(text string) string {
	if m := boldRE.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	for _, sep := range []string{": ", "："} {
		if i := strings.Index(text, sep); i > 0 {
			return text[:i]
		}
	}
	return text
}

func hasOpen(n *node) bool {
	for _, c := range n.children {
		if st, _ := c.split(); st == open || hasOpen(c) {
			return true
		}
	}
	return false
}

// flatten renders n and its descendants as one line each.
func flatten(n *node, chain []string, out []string) []string {
	st, text := n.split()
	prefix := "- "
	switch st {
	case done:
		prefix = "- [x] "
	case open:
		prefix = "- [ ] "
	}
	line := prefix + text
	if len(chain) > 0 {
		line = prefix + strings.Join(chain, " > ") + " > " + text
	}
	out = append(out, line)
	sub := append(append([]string(nil), chain...), title(text))
	for _, c := range n.children {
		out = flatten(c, sub, out)
	}
	return out
}

// sweep removes the completed subtrees under items and appends their
// flattened lines to moved.
func sweep(items []segment, moved *[]string, warnings *[]string) []segment {
	var kept []segment
	for _, s := range items {
		if s.item != nil && sweepNode(s.item, nil, moved, warnings) {
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// sweepNode reports whether n itself was archived.
func sweepNode(n *node, chain []string, moved *[]string, warnings *[]string) bool {
	st, text := n.split()
	if st == done {
		if hasOpen(n) {
			*warnings = append(*warnings, fmt.Sprintf("%q is [x] but has open children; left in place", title(text)))
			return false
		}
		*moved = flatten(n, chain, *moved)
		return true
	}
	sub := append(append([]string(nil), chain...), title(text))
	var kept []*node
	for _, c := range n.children {
		if cst, _ := c.split(); cst == note {
			kept = append(kept, c)
			continue
		}
		if !sweepNode(c, sub, moved, warnings) {
			kept = append(kept, c)
		}
	}
	n.children = kept
	return false
}

var parentRE = regexp.MustCompile(`^> parent: (.+)$`)

// normalizeArchive rewrites every archive entry into one line per item.
// A "> parent: A › B" context line becomes the chain of the entry below it.
func normalizeArchive(src string) []string {
	doc := parse(src)
	var out []string
	var chain []string
	for _, s := range doc.items {
		if s.item == nil {
			for _, l := range s.lines {
				if m := parentRE.FindStringSubmatch(l); m != nil {
					chain = strings.Split(m[1], " › ")
					continue
				}
				out = append(out, l)
			}
			continue
		}
		out = flatten(s.item, chain, out)
		chain = nil
	}
	return out
}

// squeezeBlank collapses runs of blank lines left behind by removed items.
func squeezeBlank(s string) string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n") + "\n"
}
