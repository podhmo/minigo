package main

import (
	"context"
	"errors"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/imports"
)

// maxReportedErrors bounds a syntaxError report; the remainder is
// summarized as a count.
const maxReportedErrors = 10

// formatCode runs goimports over the generated source. Syntax errors
// come back as a *syntaxError, which points each error at the
// converter and field that emitted it. goimports having to add an
// import is also a generator bug (ImportManager missed a registration):
// absorbing it hides the bug, and the guessed package can be the wrong
// one, so it fails the run too.
func formatCode(ctx context.Context, filename string, src []byte) ([]byte, error) {
	formatted, err := imports.Process(filename, src, nil)
	if err != nil {
		var list scanner.ErrorList
		if errors.As(err, &list) {
			list.RemoveMultiples()
			return nil, &syntaxError{
				headline:   fmt.Sprintf("generated code does not parse (%d errors). This is a convert-define generator bug, not a problem in the define file.\nRerun with -log-level debug to dump the full unformatted source.", len(list)),
				errs:       list,
				src:        src,
				provenance: true,
			}
		}
		return nil, fmt.Errorf("goimports failed: %w", err)
	}
	if added := addedImports(src, formatted); len(added) > 0 {
		return nil, &missingImportsError{added: added, src: src, formatted: formatted}
	}
	return formatted, nil
}

// syntaxError reports Go syntax errors: every error (bounded), each
// with a numbered excerpt, under a headline that says who has to act.
// For generated code that is the generator (c.Compute expressions are
// re-printed from a parsed AST, so broken syntax is never the user's),
// and provenance names the converter/field each error falls in — the
// generator decision to look at. For the define file it is the user.
// Constructors drop parser cascades on the same line (RemoveMultiples,
// as gofmt does), so the count reflects distinct sites.
type syntaxError struct {
	headline   string
	errs       scanner.ErrorList
	src        []byte
	provenance bool
}

// defineSyntaxError wraps a parse failure of the define file.
func defineSyntaxError(filename string, list scanner.ErrorList, src []byte) *syntaxError {
	list.RemoveMultiples()
	return &syntaxError{
		headline: fmt.Sprintf("define file %s does not parse (%d errors). Fix the define file; no code was generated.", filename, len(list)),
		errs:     list,
		src:      src,
	}
}

// missingImportsError reports imports goimports had to add — paths the
// generator used without registering them with its ImportManager.
type missingImportsError struct {
	added     []string
	src       []byte // as generated
	formatted []byte // after goimports, to learn the names it chose
}

func (e *missingImportsError) Error() string {
	lines := strings.Split(strings.TrimSuffix(string(e.src), "\n"), "\n")
	names := importNames(e.formatted)
	var b strings.Builder
	fmt.Fprintf(&b, "generated code uses %d package(s) the generator did not import. This is a convert-define generator bug (ImportManager missed a registration), not a problem in the define file; no output was written.\n", len(e.added))
	for _, path := range e.added {
		name := names[path]
		fmt.Fprintf(&b, "\nmissing import %q (referenced as %s.)\n", path, name)
		if line := firstUse(lines, name); line > 0 {
			conv, field := provenance(lines, line)
			fmt.Fprintf(&b, "  first used by: %s\n", site(conv, field))
			b.WriteString(excerpt(lines, line, 1))
		}
	}
	return b.String()
}

// firstUse returns the 1-based line of the first `name.` selector.
func firstUse(lines []string, name string) int {
	use := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\.`)
	for i, l := range lines {
		if use.MatchString(l) {
			return i + 1
		}
	}
	return 0
}

var (
	funcLine       = regexp.MustCompile(`^func (\w+)\(`)
	fieldEnterLine = regexp.MustCompile(`^\s*ec\.Enter\("([^"]+)"\)`)
	anyEnterLine   = regexp.MustCompile(`^\s*ec\.Enter\(`)
	leaveLine      = regexp.MustCompile(`^\s*ec\.Leave\(\)`)
)

func (e *syntaxError) Error() string {
	lines := strings.Split(strings.TrimSuffix(string(e.src), "\n"), "\n")
	var b strings.Builder
	b.WriteString(e.headline + "\n")
	for i, err := range e.errs {
		if i == maxReportedErrors {
			fmt.Fprintf(&b, "... and %d more errors\n", len(e.errs)-i)
			break
		}
		fmt.Fprintf(&b, "\n%s\n", err)
		if e.provenance {
			conv, field := provenance(lines, err.Pos.Line)
			fmt.Fprintf(&b, "  emitted by: %s\n", site(conv, field))
		}
		b.WriteString(excerpt(lines, err.Pos.Line, 2))
	}
	return b.String()
}

// provenance finds the converter function and the field enclosing a
// 1-based line. A field spans ec.Enter("Field") .. its matching
// ec.Leave(); element segments (ec.Enter(fmt.Sprintf(...))) nest inside
// it, so the backward scan balances Enter/Leave and only attributes a
// field whose span is still open at line.
func provenance(lines []string, line int) (conv, field string) {
	depth := 0 // Leaves seen minus Enters seen, scanning backward
	for i := min(line, len(lines)) - 2; i >= 0; i-- {
		l := lines[i]
		switch {
		case leaveLine.MatchString(l):
			depth++
		case anyEnterLine.MatchString(l):
			if depth > 0 {
				depth--
				continue
			}
			if m := fieldEnterLine.FindStringSubmatch(l); m != nil && field == "" {
				field = m[1]
			}
		}
		if m := funcLine.FindStringSubmatch(l); m != nil {
			return m[1], field
		}
	}
	return "", field
}

// site renders a provenance pair for a report.
func site(conv, field string) string {
	if conv == "" {
		return "(unknown converter)"
	}
	if field == "" {
		return "converter " + conv + " (outside any field)"
	}
	return "converter " + conv + ", field " + field
}

// excerpt renders lines [line-ctx, line+ctx] numbered, marking line.
func excerpt(lines []string, line, ctx int) string {
	var b strings.Builder
	width := len(strconv.Itoa(min(line+ctx, len(lines))))
	for n := max(1, line-ctx); n <= min(line+ctx, len(lines)); n++ {
		mark := " "
		if n == line {
			mark = ">"
		}
		fmt.Fprintf(&b, "  %s %*d | %s\n", mark, width, n, lines[n-1])
	}
	return b.String()
}

// buildConstraintHeader validates -tags as a build constraint
// expression and renders the //go:build header. A malformed expression
// is rejected here: written out, it is only a comment to the parser and
// would surface later as an unrelated-looking build failure.
func buildConstraintHeader(tags string) (string, error) {
	if tags == "" {
		return "", nil
	}
	expr, err := constraint.Parse("//go:build " + tags)
	if err != nil {
		return "", fmt.Errorf("invalid -tags %q: %v. Fix the command-line arguments: -tags takes a build constraint expression, e.g. -tags e2e or -tags 'linux && !cgo'", tags, err)
	}
	return fmt.Sprintf("\n//go:build %s\n\n", expr), nil
}

// addedImports lists import paths present in formatted but not in src.
func addedImports(src, formatted []byte) []string {
	before, after := importPaths(src), importPaths(formatted)
	var added []string
	for p := range after {
		if !before[p] {
			added = append(added, p)
		}
	}
	slices.Sort(added)
	return added
}

// importNames maps each import path to the name it is referenced by
// (the explicit alias, else the last path element).
func importNames(src []byte) map[string]string {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	for _, s := range f.Imports {
		p, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		names[p] = path.Base(p)
		if s.Name != nil {
			names[p] = s.Name.Name
		}
	}
	return names
}

func importPaths(src []byte) map[string]bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	paths := map[string]bool{}
	for _, s := range f.Imports {
		if p, err := strconv.Unquote(s.Path.Value); err == nil {
			paths[p] = true
		}
	}
	return paths
}
