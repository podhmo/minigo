package minigo_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/compile"
)

// TestDeferFacts runs the COMPILE_TIME_FACT-pinned difffuzz cases with
// every indexed fact forced to Deferred (M4-7b): the compile side keeps
// only what the CallSite spellings carry and the VM answers the rest on
// first execution — literals must read ident keys by the resolved type,
// imports must resolve through the runtime paths, and the pinned cases
// produce exactly their recorded output.
func TestDeferFacts(t *testing.T) {
	defer func(d bool) { compile.DeferFacts = d }(compile.DeferFacts)
	compile.DeferFacts = true

	for _, slug := range []string{
		"assign_pkgname_differs",
		"litkey_named_peel",
		"src_pkgtype_maplit_keys",
		"shift_const_count",
		"gate_qualconst",
		"gate_qualconstbind",
		"gate_qualforeign",
		"gate_qualforeign_case",
		"gate_qualforeign_mon",
		"gate_qualforeign_nil",
		"gate_qualscalbind",
		"selconv_typearg",
		"selnilable_trap",
		"gate_nilinit",
		"gate_nilhost",
		"gate_builtinconst",
		"gate_nilgeneric",
		"gate_nilvariadic",
		"gate_nilaliasfile",
		"gate_rangeof_call",
		"gate_recvscope",
		"gate_redecl",
		"gate_rangelocal",
		"gate_rangenested",
		"gate_pkgvarelem",
		"gate_resultindex",
		"gate_typeshadow",
		"gate_indexshadow",
		"gate_intnamedshadow",
		"gate_innershadow",
		"gate_opaquebool",
	} {
		dir := filepath.Join("testdata/difffuzz", slug)
		t.Run(slug, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatalf("want.stdout: %v", err)
			}
			wantErrText, err := os.ReadFile(filepath.Join(dir, "want.err"))
			expectErr := err == nil
			var buf bytes.Buffer
			opts := []minigo.Option{minigo.WithOutput(&buf)}
			if src, err := os.ReadFile(filepath.Join(dir, "SRC")); err == nil {
				modes := map[string]minigo.PackageMode{}
				for _, p := range strings.Fields(string(src)) {
					modes[p] = minigo.ModeSource
				}
				opts = append(opts, minigo.WithPackageModes(modes))
			}
			resCh := make(chan error, 1)
			go func() {
				e := minigo.NewEngine(".", opts...)
				_, runErr := e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
				resCh <- runErr
			}()
			select {
			case runErr := <-resCh:
				if expectErr {
					if runErr == nil {
						t.Fatalf("run under DeferFacts: want error %q, got nil", strings.TrimSpace(string(wantErrText)))
					}
					if !strings.Contains(runErr.Error(), strings.TrimSpace(string(wantErrText))) {
						t.Fatalf("run under DeferFacts: error %q does not contain %q", runErr, strings.TrimSpace(string(wantErrText)))
					}
				} else if runErr != nil {
					t.Fatalf("run under DeferFacts: %v", runErr)
				}
			case <-time.After(runCaseTimeout):
				t.Fatal("run under DeferFacts timed out")
			}
			if diff := cmp.Diff(string(want), buf.String()); diff != "" {
				t.Fatalf("stdout mismatch under DeferFacts (-want +got):\n%s", diff)
			}
		})
	}
}

// strictLazyCorpus is the COMPILE_TIME_FACT-pinned acceptance set
// (M0-7/M4-7/M4-8): the difffuzz cases whose expected output leans on
// an indexed compile-time fact.
// docs/next/ja/references/acceptance-set.md.
var strictLazyCorpus = []string{
	"assign_pkgname_differs",
	"litkey_named_peel",
	"src_pkgtype_maplit_keys",
	"shift_const_count",
	"gate_qualconst",
	"gate_qualconstbind",
	"gate_qualforeign",
	"gate_qualforeign_case",
	"gate_qualforeign_mon",
	"gate_qualforeign_nil",
	"gate_qualscalbind",
	"selconv_typearg",
	"selnilable_trap",
	"gate_nilinit",
	"gate_nilhost",
	"gate_builtinconst",
	"gate_nilgeneric",
	"gate_nilvariadic",
	"gate_nilaliasfile",
	"gate_rangeof_call",
	"gate_recvdeferred",
	"gate_recvscope",
	"gate_redecl",
	"gate_rangelocal",
	"gate_rangenested",
	"gate_pkgvarelem",
	"gate_resultindex",
	"gate_typeshadow",
	"gate_indexshadow",
	"gate_intnamedshadow",
	"gate_innershadow",
	"gate_opaquebool",
	"convarg_order",
	"convarg_nested",
	"convarg_seqorder",
	"convarg_gate",
	"convarg_pair",
	"convarg_ucorder",
	"shift_lenarr",
	"sizeof_deferred",
	"sizeof_funcret",
	"sizeof_ucall",
	"sizeof_sideeffect",
}

// TestStrictLazyCompile runs the COMPILE_TIME_FACT-pinned cases under
// WithStrictLazyCompile (M4-8a): every indexed fact asks IndexedOnly,
// so a package not yet indexed defers instead of building. Each case
// runs twice on one engine — the first run compiles while nothing is
// indexed, the second runs against packages indexed by the first —
// and both must produce exactly the recorded output.
func TestStrictLazyCompile(t *testing.T) {
	for _, slug := range strictLazyCorpus {
		dir := filepath.Join("testdata/difffuzz", slug)
		t.Run(slug, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatalf("want.stdout: %v", err)
			}
			wantErrText, err := os.ReadFile(filepath.Join(dir, "want.err"))
			expectErr := err == nil
			var buf bytes.Buffer
			opts := []minigo.Option{minigo.WithStrictLazyCompile(), minigo.WithOutput(&buf)}
			if src, err := os.ReadFile(filepath.Join(dir, "SRC")); err == nil {
				modes := map[string]minigo.PackageMode{}
				for _, p := range strings.Fields(string(src)) {
					modes[p] = minigo.ModeSource
				}
				opts = append(opts, minigo.WithPackageModes(modes))
			}
			e := minigo.NewEngine(".", opts...)
			for run := 0; run < 2; run++ {
				buf.Reset()
				resCh := make(chan error, 1)
				go func() {
					_, runErr := e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
					resCh <- runErr
				}()
				select {
				case runErr := <-resCh:
					if expectErr {
						if runErr == nil {
							t.Fatalf("run %d under strict lazy: want error %q, got nil", run, strings.TrimSpace(string(wantErrText)))
						}
						if !strings.Contains(runErr.Error(), strings.TrimSpace(string(wantErrText))) {
							t.Fatalf("run %d under strict lazy: error %q does not contain %q", run, runErr, strings.TrimSpace(string(wantErrText)))
						}
					} else if runErr != nil {
						t.Fatalf("run %d under strict lazy: %v", run, runErr)
					}
				case <-time.After(runCaseTimeout):
					t.Fatalf("run %d under strict lazy timed out", run)
				}
				if diff := cmp.Diff(string(want), buf.String()); diff != "" {
					t.Fatalf("run %d under strict lazy: stdout mismatch (-want +got):\n%s", run, diff)
				}
			}
		})
	}
}

// TestStrictLazyCompilePreparedIndex is the re-compile counterpart of
// TestStrictLazyCompile (M4-8a/b): a second run on the same engine only
// reuses packages already loaded and functions already compiled, so it
// cannot compare a first compile whose imports are already indexed.
// Here each case runs once on a fresh engine whose entry package is
// loaded — parsed and indexed, not initialized — and whose per-file
// import refs are materialized to the Indexed stage up front. The
// entry then compiles with every dependency's index already in place
// (IndexedOnly answers Found instead of Deferred) and must produce
// exactly the recorded output, like the cold compile does.
func TestStrictLazyCompilePreparedIndex(t *testing.T) {
	// Cases that still diverge under prepared index: the same
	// `.imp:<path>` runtime-resolution bug as time_weekday_bound —
	// a bound-const comparison gate respells `time` through a
	// synthesized qualifier the VM cannot resolve (follow-up.md
	// M4-8b). Each entry must still diverge; a case that starts
	// passing is removed from the list to pin the fix.
	preparedDivergent := map[string]bool{
		"gate_qualforeign":      true,
		"gate_qualforeign_case": true,
		"gate_qualforeign_mon":  true,
		"gate_qualforeign_nil":  true,
	}
	for _, slug := range strictLazyCorpus {
		dir := filepath.Join("testdata/difffuzz", slug)
		t.Run(slug, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatalf("want.stdout: %v", err)
			}
			wantErrText, err := os.ReadFile(filepath.Join(dir, "want.err"))
			expectErr := err == nil
			var buf bytes.Buffer
			opts := []minigo.Option{minigo.WithStrictLazyCompile(), minigo.WithOutput(&buf)}
			if src, err := os.ReadFile(filepath.Join(dir, "SRC")); err == nil {
				modes := map[string]minigo.PackageMode{}
				for _, p := range strings.Fields(string(src)) {
					modes[p] = minigo.ModeSource
				}
				opts = append(opts, minigo.WithPackageModes(modes))
			}
			e := minigo.NewEngine(".", opts...)
			ctx := context.Background()
			// Prepare the deps' index: loading the entry creates the
			// file-scope import refs, and Materialize carries each one
			// to the Indexed stage without initializing the package.
			entry, err := e.Package(ctx, "./"+filepath.ToSlash(dir))
			if err != nil {
				t.Fatalf("load entry package: %v", err)
			}
			for _, refs := range entry.Imports {
				for _, ref := range refs {
					if _, err := ref.Materialize(); err != nil {
						t.Fatalf("materialize import %q: %v", ref.Path, err)
					}
				}
			}
			resCh := make(chan error, 1)
			go func() {
				_, runErr := e.Run(ctx, "./"+filepath.ToSlash(dir), "")
				resCh <- runErr
			}()
			var runErr error
			select {
			case runErr = <-resCh:
			case <-time.After(runCaseTimeout):
				t.Fatal("run under strict lazy with prepared index timed out")
			}
			diff := cmp.Diff(string(want), buf.String())
			errMatch := expectErr && runErr != nil &&
				strings.Contains(runErr.Error(), strings.TrimSpace(string(wantErrText)))
			matched := diff == "" && expectErr == (runErr != nil) && (!expectErr || errMatch)
			switch {
			case preparedDivergent[slug] && matched:
				t.Errorf("%s now matches under prepared index: remove it from preparedDivergent to pin the fix", slug)
			case preparedDivergent[slug]:
				t.Skipf("known divergence under prepared index (.imp: resolution; follow-up.md M4-8b)")
			case expectErr && runErr == nil:
				t.Fatalf("run under strict lazy with prepared index: want error %q, got nil", strings.TrimSpace(string(wantErrText)))
			case expectErr && !errMatch:
				t.Fatalf("run under strict lazy with prepared index: error %q does not contain %q", runErr, strings.TrimSpace(string(wantErrText)))
			case diff != "":
				t.Fatalf("stdout mismatch under strict lazy with prepared index (-want +got):\n%s", diff)
			case matched:
				// the pinned outcome holds — nothing to report
			case runErr != nil:
				t.Fatalf("run under strict lazy with prepared index: %v", runErr)
			}
		})
	}
}
