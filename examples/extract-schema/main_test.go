package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "update golden files")

// TestExtractSchema runs the host run() against the bundled fixture
// app/ in both modes and diffs the rendered document against golden
// files. The walk itself is an interpreted script — these tests are
// the contract between host and script, not a reimplementation.
func TestExtractSchema(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		plugins []string
		type_   string
		stdlib  string
		golden  string
	}{
		{
			name:   "type User",
			type_:  "User",
			stdlib: "leaf",
			golden: "testdata/User.golden",
		},
		{
			name:   "whole package",
			type_:  "",
			stdlib: "leaf",
			golden: "testdata/package.golden",
		},
		{
			name:    "plugin time override",
			plugins: []string{"./plugins/time"},
			type_:   "User",
			stdlib:  "leaf",
			golden:  "testdata/User-plugin.golden",
		},
		{
			name:    "plugin enumcode override under ,string",
			plugins: []string{"./plugins/enumcode"},
			type_:   "Stringified",
			stdlib:  "leaf",
			golden:  "testdata/Stringified-enumcode.golden",
		},
		{
			name:   "stdlib walk",
			type_:  "",
			stdlib: "walk",
			golden: "testdata/stdlib-walk.golden",
		},
		{
			name:    "desc-rules plugin",
			plugins: []string{"./plugins/desc"},
			type_:   "Commented",
			stdlib:  "leaf",
			golden:  "testdata/desc-rules.golden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(ctx, ".", "./script", tc.plugins, "./app", false, tc.type_, tc.stdlib, true, "", "")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if *update {
				if err := os.WriteFile(tc.golden, []byte(got+"\n"), 0o644); err != nil {
					t.Fatalf("update %s: %v", tc.golden, err)
				}
			}
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatalf("read %s (run with -update to create): %v", tc.golden, err)
			}
			if diff := cmp.Diff(string(want), got+"\n"); diff != "" {
				t.Errorf("%s mismatch (-want +got):\n%s", tc.golden, diff)
			}
		})
	}
}

// TestPkgMode exercises -pkg resolution: an import path names the same
// package as its directory, so both spellings render the identical
// document — and a bound stdlib path retries through the source layer,
// so its decls render instead of an empty-defs doc or a trap inside
// DirOf on the non-directory target string.
func TestPkgMode(t *testing.T) {
	ctx := context.Background()
	dirDoc, err := run(ctx, ".", "./script", nil, "./app", false, "", "leaf", true, "", "")
	if err != nil {
		t.Fatalf("dir mode: %v", err)
	}
	pkgDoc, err := run(ctx, ".", "./script", nil, "github.com/podhmo/minigo/examples/extract-schema/app", true, "", "leaf", true, "", "")
	if err != nil {
		t.Fatalf("pkg mode: %v", err)
	}
	if diff := cmp.Diff(dirDoc, pkgDoc); diff != "" {
		t.Errorf("-pkg doc differs from dir doc (-dir +pkg):\n%s", diff)
	}
	// a bound path retries through source, so whole-package listing now
	// walks real decls — and fails honestly on the first unrenderable
	// one (Timer's chan field) rather than emitting an empty $defs doc.
	if _, err := run(ctx, ".", "./script", nil, "time", true, "", "leaf", true, "", ""); err == nil {
		t.Error("-pkg time: expected an unsupported-shape error, got nil")
	}
	// a bound path resolved from source answers single-type lookups too
	// (corpus m05: `-pkg time Time` used to fail "type not found").
	timeTypeDoc, err := run(ctx, ".", "./script", nil, "time", true, "Time", "leaf", true, "", "")
	if err != nil {
		t.Fatalf("-pkg time Time: %v", err)
	}
	if !strings.Contains(timeTypeDoc, `"type": "object"`) {
		t.Errorf("-pkg time Time produced no object schema: %q", timeTypeDoc)
	}
	// a target string that names no package is a clean error, not a
	// runtime trap
	if _, err := run(ctx, ".", "./script", nil, "example.com/gone", true, "", "leaf", true, "", ""); err == nil {
		t.Error("-pkg on an unresolvable path: expected an error, got nil")
	}
}

// TestMissingType fails loudly: a typo'd type name must not render an
// empty document.
func TestMissingType(t *testing.T) {
	_, err := run(context.Background(), ".", "./script", nil, "./app", false, "Nope", "leaf", true, "", "")
	if err == nil {
		t.Fatal("expected an error for a missing type")
	}
}

// TestErrorPath checks the CLI exit code wiring without spawning a
// subprocess: a bad flag is 2, a run failure is 1.
func TestErrorPath(t *testing.T) {
	if code := runMain(context.Background(), []string{"-stdlib", "bogus", "./app"}); code != 2 {
		t.Errorf("bad flag: want exit 2, got %d", code)
	}
	if code := runMain(context.Background(), []string{filepath.Join(t.TempDir(), "nonexistent")}); code != 1 {
		t.Errorf("missing target: want exit 1, got %d", code)
	}
}
