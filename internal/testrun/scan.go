// Package testrun implements `minigo test`: the package test-variant
// build (non-test files + internal *_test.go files), test discovery,
// the per-test Engine.Call loop, and the `go test -v` shaped report.
package testrun

import (
	"errors"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/podhmo/minigo/resolve"
)

// scanFiles is a directory's *_test.go files classified for one build
// configuration, like [resolve.ReadPackageFiles] does for the non-test
// half: MatchFile applies the same //go:build/_GOOS/_GOARCH/tag rules.
type scanFiles struct {
	// internal are `package <pkg>` test files — they merge into the
	// test-variant build.
	internal []string
	// external are `package <pkg>_test` files — v1 cannot build them;
	// the runner reports them loudly.
	external []string
	// other are test files with a third package clause — a build error
	// in go too, reported as a problem.
	other []string
	// rejected are files MatchFile could not read or parse — go build
	// fails on them, so they are problems, not skipped.
	rejected []string
	// cgoSkipped are test files importing "C" — dropped like the
	// non-test walk drops cgo files, but reported so no test vanishes
	// silently.
	cgoSkipped []string
	// pkgName is the package clause used for the internal/external
	// split (the non-test files' clause, or the _test-stripped clause
	// of a test-only directory).
	pkgName string
	// all is every matched *_test.go path (internal ∪ external ∪ other):
	// the --tagged gate compares file sets, not classifications.
	all []string
}

// scannedFile is one matched, parsed test file awaiting
// classification — pkgName is not known until the scan finishes.
type scannedFile struct {
	abs    string
	clause string
}

// scanTests lists the *_test.go files of dir under cfg's build
// constraints and classifies them by package clause. dir must be
// absolute. Files importing "C" are skipped like ReadPackageFiles does.
func scanTests(dir string, cfg resolve.BuildConfig) (*scanFiles, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	bctx := build.Default
	if cfg.GOOS != "" {
		bctx.GOOS = cfg.GOOS
	}
	if cfg.GOARCH != "" {
		bctx.GOARCH = cfg.GOARCH
	}
	bctx.BuildTags = append(slices.Clone(bctx.BuildTags), cfg.Tags...)

	// the package clause the non-test files declare — test files split
	// on <name> vs <name>_test. If no non-test file exists (a test-only
	// directory), the test files' own clauses name the package below.
	pkgName, clauseErr := packageClause(dir, cfg)

	out := &scanFiles{}
	fset := token.NewFileSet()
	var files []scannedFile
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, "_test.go") ||
			strings.HasPrefix(fname, "_") || strings.HasPrefix(fname, ".") {
			continue
		}
		match, err := bctx.MatchFile(dir, fname)
		if err != nil {
			out.rejected = append(out.rejected, err.Error())
			continue
		}
		if !match {
			continue
		}
		abs := filepath.Join(dir, fname)
		f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil || f == nil {
			out.rejected = append(out.rejected, abs)
			continue
		}
		if resolve.ImportsC(f) {
			out.cgoSkipped = append(out.cgoSkipped, abs)
			continue
		}
		files = append(files, scannedFile{abs: abs, clause: f.Name.Name})
	}
	if pkgName == "" {
		if clauseErr != nil && len(files) == 0 {
			if errors.Is(clauseErr, resolve.ErrNoBuildableGoFiles) {
				return out, nil
			}
			return nil, clauseErr
		}
		pkgName = baseClause(files)
	}
	for _, f := range files {
		out.all = append(out.all, f.abs)
		switch {
		case f.clause == pkgName:
			out.internal = append(out.internal, f.abs)
		case f.clause == pkgName+"_test":
			out.external = append(out.external, f.abs)
		default:
			out.other = append(out.other, f.abs)
		}
	}
	out.pkgName = pkgName
	sort.Strings(out.internal)
	sort.Strings(out.external)
	sort.Strings(out.other)
	sort.Strings(out.all)
	sort.Strings(out.rejected)
	sort.Strings(out.cgoSkipped)
	return out, nil
}
