package minigo_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

// TestDiffRegressions runs every case emitted by `tools/difffuzz gen -emit`
// (testdata/difffuzz/<slug>/{main.go,want.stdout}). want.stdout is the go
// toolchain's output, so a case needs no hand-written expectation.
//
// A case with a PENDING file is a known, unfixed divergence: it must still
// diverge. Once minigo matches go, the test fails asking to delete PENDING,
// so a fix is never left unpinned.
func TestDiffRegressions(t *testing.T) {
	dirs, err := filepath.Glob("testdata/difffuzz/*/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range dirs {
		dir := filepath.Dir(f)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(dir, "want.stdout"))
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(filepath.Join(dir, "PENDING"))
			pending := statErr == nil

			var buf bytes.Buffer
			e := minigo.NewEngine(".", minigo.WithOutput(&buf))
			_, runErr := e.Run(context.Background(), "./"+filepath.ToSlash(dir), "")
			diff := cmp.Diff(string(want), buf.String())
			switch {
			case pending && runErr == nil && diff == "":
				t.Errorf("%s now matches go: delete %s/PENDING to pin the fix", dir, dir)
			case pending:
				t.Skipf("known divergence (see %s/PENDING)", dir)
			case runErr != nil:
				t.Errorf("run: %v", runErr)
			case diff != "":
				t.Errorf("output differs from go (-go +minigo):\n%s", diff)
			}
		})
	}
}
