package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRunProfiles: --cpuprofile / --memprofile write gzipped pprof
// profiles, also when the script ends with os.Exit(N).
func TestRunProfiles(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"ok", "", ""},
		{"exit", "os.Exit(3)", "exit status 3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			src := "package main\n\nimport \"os\"\n\nvar _ = os.Args\n\nfunc main() {\n\ts := 0\n\tfor i := range 1000 {\n\t\ts += i\n\t}\n\t_ = s\n\t" + c.body + "\n}\n"
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module prof\n\ngo 1.26.0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			cpu, mem := filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "mem.pprof")
			err := run(context.Background(), []string{dir, "--cpuprofile", cpu, "--memprofile=" + mem})
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if c.wantErr == "" && gotErr != "" || !strings.Contains(gotErr, c.wantErr) {
				t.Fatalf("run error = %q, want %q", gotErr, c.wantErr)
			}
			for _, p := range []string{cpu, mem} {
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				// pprof profiles are gzip-compressed protobufs
				if diff := cmp.Diff(true, bytes.HasPrefix(b, []byte{0x1f, 0x8b})); diff != "" {
					t.Errorf("%s is not a gzipped profile (-want +got):\n%s", filepath.Base(p), diff)
				}
			}
		})
	}
}
