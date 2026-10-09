// Package scanx is the helper library gen-sync's script scans with:
// sentinel-guided rewriting of //go:generate regions. It is ordinary
// Go; minigo interprets it too when the gen-sync script imports it,
// so it doubles as a demo of a tool growing its own module-local
// library.
package scanx

import (
	"strings"
)

// Sentinel is the comment line that opens a file's managed region:
// every real //go:generate line below it belongs to the tool.
const Sentinel = "// Code generated directives below are managed by gen-sync. DO NOT EDIT."

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
				// trim \r too: a CRLF line ends in "\r\n" and the split
				// leaves the \r attached — without it the sentinel never
				// matches and a second managed block gets inserted.
				return strings.TrimRight(ln[i:], " \t\r"), clean
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
