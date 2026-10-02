package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/build/constraint"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// compilerErrorLine matches a `go build` diagnostic: "path.go:L:C: msg".
var compilerErrorLine = regexp.MustCompile(`^(\S+\.go):(\d+):(\d+): (.*)$`)

// checkGenerated type-checks the formatted output inside its package by
// running `go build` with an overlay, so nothing is written to output.
// Errors in the generated file are traced back to the converter/field
// that emitted them; errors anywhere else mean the check is
// inconclusive, because the package (or a dependency) is broken for
// reasons the generated code does not control.
func checkGenerated(ctx context.Context, output string, formatted []byte, buildTags string) error {
	abs, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("-check: %w", err)
	}
	tags, err := satisfyingTags(buildTags)
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "convert-define-check")
	if err != nil {
		return fmt.Errorf("-check: %w", err)
	}
	defer os.RemoveAll(tmp)
	genFile := filepath.Join(tmp, filepath.Base(abs))
	if err := os.WriteFile(genFile, formatted, 0o644); err != nil {
		return fmt.Errorf("-check: %w", err)
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {abs: genFile}})
	if err != nil {
		return fmt.Errorf("-check: %w", err)
	}
	overlayFile := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return fmt.Errorf("-check: %w", err)
	}

	// -gcflags=-e reports every error rather than stopping at 10;
	// -o discards the binary of a main package.
	args := []string{"build", "-overlay", overlayFile, "-gcflags=-e", "-o", os.DevNull}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, ".")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = filepath.Dir(abs)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err == nil {
		return nil
	} else if _, ok := err.(*exec.ExitError); !ok {
		return fmt.Errorf("-check: running go %s: %w", strings.Join(args, " "), err)
	}
	return buildFailure(abs, genFile, cmd.Dir, out.String(), formatted)
}

// buildFailure classifies `go build` output against the generated file,
// which the compiler names either by its output path or by the overlay
// file standing in for it.
func buildFailure(genAbs, overlaid, dir, out string, formatted []byte) error {
	var inGenerated scanner.ErrorList
	var elsewhere []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "\t") {
			continue // package headers and continuation lines
		}
		m := compilerErrorLine.FindStringSubmatch(line)
		if m == nil {
			elsewhere = append(elsewhere, line)
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		if file != genAbs && file != overlaid {
			elsewhere = append(elsewhere, line)
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		if strings.HasPrefix(m[4], "too many errors") {
			continue
		}
		inGenerated.Add(token.Position{Filename: genAbs, Line: ln, Column: col}, m[4])
	}
	if len(inGenerated) == 0 {
		if len(elsewhere) == 0 {
			elsewhere = []string{strings.TrimSpace(out)}
		}
		return &inconclusiveCheckError{lines: elsewhere}
	}
	inGenerated.RemoveMultiples()
	return &syntaxError{
		headline: fmt.Sprintf("-check: generated code does not compile (%d errors); no output was written.\n"+
			"Each error names the converter and field that emitted it. Fix the define file first (add a define.Rule for the type pair, c.Convert the field, or fix the c.Compute expression); if the define file is right, this is a convert-define generator bug.", len(inGenerated)),
		errs:       inGenerated,
		src:        formatted,
		provenance: true,
	}
}

// inconclusiveCheckError reports a `go build` failure that does not
// involve the generated file.
type inconclusiveCheckError struct {
	lines []string
}

func (e *inconclusiveCheckError) Error() string {
	var b strings.Builder
	b.WriteString("-check: inconclusive: the package does not build for reasons outside the generated file; no output was written.\n" +
		"Fix the input packages first (or rerun without -check to regenerate anyway):\n")
	for i, l := range e.lines {
		if i == maxReportedErrors {
			fmt.Fprintf(&b, "  ... and %d more lines\n", len(e.lines)-i)
			break
		}
		fmt.Fprintf(&b, "  %s\n", l)
	}
	return b.String()
}

// satisfyingTags returns a set of build tags under which the -tags
// expression holds, so `go build` includes the generated file.
func satisfyingTags(expr string) ([]string, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	x, err := constraint.Parse("//go:build " + expr)
	if err != nil {
		return nil, err // already rejected by buildConstraintHeader
	}
	names := tagNames(x, nil)
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > 12 {
		return nil, fmt.Errorf("-check: -tags %q names too many tags to choose a build configuration; rerun without -check", expr)
	}
	for mask := range 1 << len(names) {
		var set []string
		for i, n := range names {
			if mask&(1<<i) != 0 {
				set = append(set, n)
			}
		}
		if x.Eval(func(tag string) bool { return slices.Contains(set, tag) }) {
			return set, nil
		}
	}
	return nil, fmt.Errorf("-check: -tags %q is never satisfied, so the generated file never builds. Fix the command-line arguments", expr)
}

// tagNames lists every tag in x (Eval short-circuits, so it cannot).
func tagNames(x constraint.Expr, acc []string) []string {
	switch x := x.(type) {
	case *constraint.TagExpr:
		return append(acc, x.Tag)
	case *constraint.NotExpr:
		return tagNames(x.X, acc)
	case *constraint.AndExpr:
		return tagNames(x.Y, tagNames(x.X, acc))
	case *constraint.OrExpr:
		return tagNames(x.Y, tagNames(x.X, acc))
	}
	return acc
}
