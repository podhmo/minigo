package generator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestPackageIdents pins the syntax-only collection: top-level types,
// funcs (not methods), vars and consts — including from a file that
// does not parse and one behind a build tag.
func TestPackageIdents(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.go":      "package p\n\ntype item struct{}\n\nfunc (item) m() {}\n\nfunc helper() {}\n\nvar s, _ = 1, 2\n\nconst (\n\tv = 1\n)\n",
		"broken.go": "package p\n\ntype key int\n\nfunc oops( {\n",
		"tagged.go": "//go:build codegen\n\npackage p\n\nfunc main() {}\n",
		"notes.txt": "type ignored int\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"helper", "item", "key", "main", "oops", "s", "v"}
	if diff := cmp.Diff(want, PackageIdents(context.Background(), dir)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
