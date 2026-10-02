package main

import (
	"context"
	"errors"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/imports"
)

// maxReportedErrors bounds a syntaxError report; the remainder is
// summarized as a count.
const maxReportedErrors = 10

// strictImports turns an import goimports had to add into an error;
// tests enable it so a missed ImportManager registration fails CI
// instead of being absorbed.
var strictImports = false

// formatCode runs goimports over the generated source. Syntax errors
// come back as a *syntaxError, which points each error at the
// converter and field that emitted it. goimports silently adding an
// import is also a generator bug (ImportManager missed a registration),
// so it is logged instead of being absorbed.
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
		if strictImports {
			return nil, fmt.Errorf("goimports added imports the generator did not register: %v", added)
		}
		slog.WarnContext(ctx, "goimports added imports the generator did not register (generator bug: ImportManager missed them)", "file", filename, "imports", added)
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

var (
	funcLine  = regexp.MustCompile(`^func (\w+)\(`)
	enterLine = regexp.MustCompile(`^\s*ec\.Enter\("([^"]+)"\)`)
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
			fmt.Fprintf(&b, "  emitted by: converter %s, field %s\n", orUnknown(conv), orUnknown(field))
		}
		b.WriteString(excerpt(lines, err.Pos.Line, 2))
	}
	return b.String()
}

// provenance finds the converter function and the field (its
// ec.Enter("Field") marker) enclosing a 1-based line.
func provenance(lines []string, line int) (conv, field string) {
	for i := min(line, len(lines)) - 1; i >= 0; i-- {
		if m := enterLine.FindStringSubmatch(lines[i]); m != nil && field == "" {
			field = m[1]
		}
		if m := funcLine.FindStringSubmatch(lines[i]); m != nil {
			return m[1], field
		}
	}
	return "", field
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

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
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
