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
	} {
		dir := filepath.Join("testdata/difffuzz", slug)
		t.Run(slug, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatalf("want.stdout: %v", err)
			}
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
				if runErr != nil {
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
