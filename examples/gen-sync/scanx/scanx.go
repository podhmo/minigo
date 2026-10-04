// Package scanx is the helper library gen-sync's script scans with:
// struct-tag lookups and sentinel-guided rewriting of //go:generate
// regions. It is ordinary Go; minigo interprets it too when the
// gen-sync script imports it, so it doubles as a demo of a tool
// growing its own module-local library.
package scanx

import (
	"strings"
)

// Sentinel is the comment line that opens a file's managed region:
// every real //go:generate line below it belongs to the tool.
const Sentinel = "// Code generated directives below are managed by gen-sync. DO NOT EDIT."

// TagField is one `key:"value"` entry of a struct tag.
type TagField struct {
	Key   string
	Value string
}

// ParseTag splits a raw struct tag (the text between backquotes) into
// its `key:"value"` fields. Values keep their escapes; a malformed tail
// is ignored, matching reflect.StructTag's tolerant behavior.
func ParseTag(tag string) []TagField {
	out := []TagField{}
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] != ':' && tag[i] != '"' && tag[i] != ' ' {
			i++
		}
		if i == len(tag) || tag[i] != ':' || i+1 == len(tag) || tag[i+1] != '"' {
			break
		}
		key := tag[:i]
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		out = append(out, TagField{Key: key, Value: tag[1:i]})
		tag = tag[i+1:]
	}
	return out
}

// LookupTag returns the value stored under key in a raw struct tag.
func LookupTag(tag, key string) (string, bool) {
	for _, f := range ParseTag(tag) {
		if f.Key == key {
			return f.Value, true
		}
	}
	return "", false
}

// TagHasElement reports whether the comma-separated value stored under
// key contains elem as a whole element: `validate:"required"` matches
// "required" while `binding:"notrequired"` does not.
func TagHasElement(tag, key, elem string) bool {
	v, ok := LookupTag(tag, key)
	if !ok {
		return false
	}
	for _, e := range strings.Split(v, ",") {
		if strings.TrimSpace(e) == elem {
			return true
		}
	}
	return false
}

// InsertAnchor locates the line after the package clause and import
// decls — where a fresh managed block goes. Package doc comments and
// build tags live above the clause; blank lines and comments between
// decls are skipped without moving the anchor.
func InsertAnchor(lines []string) int {
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
			if strings.Contains(t, ")") {
				anchor = i + 1 // single-line group: import ("fmt")
			} else {
				inImports = true
			}
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

// Dedupe removes duplicate strings, keeping first occurrences.
func Dedupe(xs []string) []string {
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

// lineScan is the cross-line lexer state used to decide which line
// contents are really code: /* */ comments and `...` raw strings span
// lines, and a //go:generate-looking line inside either is text, not a
// directive.
type lineScan int

const (
	code lineScan = iota
	inBlockComment
	inRawString
)

// leadingComment returns the // comment that leads a line in code
// position (only whitespace may precede it), trimming trailing
// whitespace. It reports false for lines entirely inside a block
// comment or raw string, and for lines led by a code token or a /*
// comment — go:generate-style directives must head their line, so
// `x := 1 //go:generate` and `/*c*/ //go:generate` don't count. The
// scan state is advanced across the whole line.
func leadingComment(ln string, st *lineScan) (string, bool) {
	clean := *st == code // nothing but whitespace seen so far in code
	i := 0
	for i < len(ln) {
		switch *st {
		case inBlockComment:
			j := strings.Index(ln[i:], "*/")
			if j < 0 {
				return "", false
			}
			i += j + 2
			*st = code
		case inRawString:
			j := strings.IndexByte(ln[i:], '`')
			if j < 0 {
				return "", false
			}
			i += j + 1
			*st = code
		default:
			c := ln[i]
			if c == ' ' || c == '\t' {
				i++
				continue
			}
			if c == '/' && i+1 < len(ln) && ln[i+1] == '/' {
				return strings.TrimRight(ln[i:], " \t"), clean
			}
			clean = false
			switch {
			case c == '/' && i+1 < len(ln) && ln[i+1] == '*':
				*st = inBlockComment
				i += 2
			case c == '`':
				*st = inRawString
				i++
			case c == '"' || c == '\'':
				i = skipQuoted(ln, i)
			default:
				i++
			}
		}
	}
	return "", false
}

// skipQuoted skips a "..." or '...' literal starting at i.
func skipQuoted(ln string, i int) int {
	q := ln[i]
	i++
	for i < len(ln) {
		c := ln[i]
		if c == '\\' {
			i += 2
			continue
		}
		i++
		if c == q {
			break
		}
	}
	return i
}

// FindSentinel reports the index of the file's sentinel line — an exact
// `//` comment match, so prose merely mentioning "managed by gen-sync"
// and the same text inside a /* */ comment or raw string do not count.
func FindSentinel(lines []string) int {
	st := code
	for i, ln := range lines {
		if c, ok := leadingComment(ln, &st); ok && c == Sentinel {
			return i
		}
	}
	return -1
}

// IsGenerateDirective reports whether a line is a real //go:generate
// directive: the trimmed line is a // comment starting with
// "//go:generate" followed by whitespace or end of line — so
// "//go:generate-not-directive" and `x := 1 //go:generate` don't count.
func IsGenerateDirective(ln string) bool {
	t := strings.TrimSpace(ln)
	if !strings.HasPrefix(t, "//go:generate") {
		return false
	}
	rest := t[len("//go:generate"):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// GenerateRunEnd returns the index into lines where the managed run
// ends: the run is the sentinel-adjacent block of //go:generate
// directives and blank lines. Everything past it — including
// hand-written //go:generate lines separated from the sentinel — is
// user content.
func GenerateRunEnd(lines []string) int {
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" || IsGenerateDirective(lines[i]) {
			i++
			continue
		}
		break
	}
	return i
}
