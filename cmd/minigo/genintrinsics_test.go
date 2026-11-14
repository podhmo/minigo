package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podhmo/minigo"
)

// TestGenIntrinsicsBindAs: a package whose declared name differs from its
// last import-path element gets a BindAs option in the generated table, so
// scripts can refer to it by its declared name.
func TestGenIntrinsicsBindAs(t *testing.T) {
	for _, tc := range []struct {
		dir, pkg string
		bindAs   bool
	}{
		{dir: "goldmark-alert", pkg: "alert", bindAs: true},
		{dir: "plain", pkg: "plain", bindAs: false},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.24\n")
			writeFile(t, filepath.Join(root, tc.dir, "lib.go"), "package "+tc.pkg+"\n\nfunc Hello() string { return \"hi\" }\n")
			out := t.TempDir()
			e := minigo.NewEngine(root)
			if err := genOne(context.Background(), e, out, filepath.Join(root, tc.dir)); err != nil {
				t.Fatalf("genOne: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(out, "example.com", "m", tc.dir, "install.go"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(string(b), `minigo.BindAs("`+tc.pkg+`")`)
			if got != tc.bindAs {
				t.Fatalf("BindAs emitted = %v, want %v:\n%s", got, tc.bindAs, b)
			}
		})
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
