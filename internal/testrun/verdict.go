package testrun

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/podhmo/minigo/internal/testrun/mtesting"
	"github.com/podhmo/minigo/runtime"
)

// classifyCall folds an Engine.Call error into the test's verdict.
func classifyCall(t *mtesting.T, err error) {
	if err == nil {
		setFlagVerdict(t)
		return
	}
	if st, ok := mtesting.StopOf(err); ok && st == t {
		setFlagVerdict(t)
		return
	}
	if _, ok := err.(*runtime.Panic); ok {
		t.SetVerdict(mtesting.Panic, err.Error())
		return
	}
	t.SetVerdict(mtesting.Trap, err.Error())
}

// setFlagVerdict derives the verdict from the T's own flags after a
// normally-returned (or own-sentinel) call.
func setFlagVerdict(t *mtesting.T) {
	switch {
	case t.Failed():
		t.SetVerdict(mtesting.Fail, "")
	case t.Skipped():
		t.SetVerdict(mtesting.Skip, "")
	default:
		t.SetVerdict(mtesting.Pass, "")
	}
}

// compileRun splits -run into per-level unanchored regexps.
func compileRun(run string) ([]*regexp.Regexp, error) {
	if run == "" {
		return nil, nil
	}
	var out []*regexp.Regexp
	for _, pat := range splitSlash(run) {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid -run %q: %w", run, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// splitSlash splits a -run pattern on unescaped "/" — go's level
// separator.
func splitSlash(s string) []string {
	var out []string
	var cur []rune
	esc := false
	for _, r := range s {
		if esc {
			cur = append(cur, r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			cur = append(cur, r)
			continue
		}
		if r == '/' {
			out = append(out, string(cur))
			cur = nil
			continue
		}
		cur = append(cur, r)
	}
	return append(out, string(cur))
}

// pathOfDir is the package spelling for the trailer line: the
// directory's slash-cleaned absolute path.
func pathOfDir(dir string) string {
	return filepath.ToSlash(dir)
}

// relPath spells a scanned file like declPos does — relative to the
// package dir when it nests inside it.
func relPath(dir, file string) string {
	if rel, err := filepath.Rel(dir, file); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(file)
}
