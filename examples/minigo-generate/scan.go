package main

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/podhmo/minigo/syntax"
)

// marker is the directive prefix this tool executes — deliberately not
// //go:generate: a repo keeps running go generate for its existing tools
// and opts individual lines into interpreted execution.
const marker = "//minigo:generate"

// directive is one //minigo:generate line found in a scanned file.
type directive struct {
	file string // absolute path of the file holding the directive
	line int    // directive line in file ($GOLINE)
	pkg  string // the file's package clause name ($GOPACKAGE)
	text string // everything after the marker, trimmed (-run and -x see this)
	ref  string // tool reference: a package dir relative to file's directory
	args []string
}

// scan finds every //minigo:generate directive in the .go files of dir,
// in filename+position order. Directives are read through the parsed
// file's comment table: the marker counts as a Go directive comment, so
// CommentGroup.Text() drops it like //go:generate — but the raw
// *ast.Comment entries keep it. Marker text spelled inside a /* */
// block or a string literal is not a line comment and opens nothing.
func scan(dir string) ([]directive, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, en := range ents {
		name := en.Name()
		// the Go package convention: no dirs, no non-Go files, no
		// _ or . prefixed files.
		if en.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	var out []directive
	for _, name := range names {
		path := filepath.Join(abs, name)
		sf, err := syntax.ParseFile(fset, path, nil)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
		for _, cg := range sf.AST.Comments {
			for _, c := range cg.List {
				// c.Text keeps the comment markers, so a /* */ block
				// spelling the marker fails this prefix check on its own.
				if !strings.HasPrefix(c.Text, marker) {
					continue
				}
				rest := c.Text[len(marker):]
				if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
					continue // e.g. //minigo:generatex — the word must end
				}
				pos := fset.Position(c.Slash)
				args, err := splitArgs(strings.TrimSpace(rest), path, pos.Line)
				if err != nil {
					return nil, err
				}
				if len(args) == 0 {
					return nil, fmt.Errorf("%s:%d: minigo:generate directive needs a tool reference", path, pos.Line)
				}
				out = append(out, directive{
					file: path, line: pos.Line, pkg: sf.AST.Name.Name,
					text: strings.TrimSpace(rest),
					ref:  args[0], args: args[1:],
				})
			}
		}
	}
	return out, nil
}

// splitArgs splits a directive line into space-separated tokens. A
// double-quoted span groups into one token (spaces inside survive); no
// backslash or variable expansion — the line never reaches a shell.
func splitArgs(s, file string, line int) ([]string, error) {
	var out []string
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out, nil
		}
		var tok string
		if s[0] == '"' {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unterminated quoted string in directive", file, line)
			}
			tok = s[1 : 1+end]
			s = s[1+end+1:]
		} else {
			end := strings.IndexAny(s, " \t")
			if end < 0 {
				tok, s = s, ""
			} else {
				tok, s = s[:end], s[end:]
			}
		}
		out = append(out, tok)
	}
}
