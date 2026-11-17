package minigo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// writePinPkg writes the session-isolation fixture and returns its dir.
func writePinPkg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writePinSrc(t, dir, 1)
	return dir
}

func writePinSrc(t *testing.T, dir string, version int) {
	t.Helper()
	src := "package pinpkg\n\nvar N = 0\n\nfunc Inc() int {\n\tN = N + 1\n\treturn N\n}\n\nfunc Version() int {\n\treturn " +
		string(rune('0'+version)) + "\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callPinInt(t *testing.T, e *Engine, ctx context.Context, dir, name string) int64 {
	t.Helper()
	p, err := e.Package(ctx, dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	v, err := e.Call(ctx, p, name)
	if err != nil {
		t.Fatalf("Call(%s): %v", name, err)
	}
	return v.(int64)
}

// TestSessionIsolationPin pins the current session behavior (M0-5):
// sessions forked from one engine hold independent package state, and
// after a source edit an existing session keeps running the version it
// already loaded while a fresh session sees the edit.
func TestSessionIsolationPin(t *testing.T) {
	ctx := context.Background()

	t.Run("CountersAreIndependent", func(t *testing.T) {
		dir := writePinPkg(t)
		base := NewEngine(dir)
		s1 := base.NewSession()
		s2 := base.NewSession()

		got := []int64{
			callPinInt(t, s1, ctx, dir, "Inc"), // 1
			callPinInt(t, s1, ctx, dir, "Inc"), // 2
			callPinInt(t, s2, ctx, dir, "Inc"), // 1: s2 has its own N
			callPinInt(t, s1, ctx, dir, "Inc"), // 3: s1 unaffected by s2
			callPinInt(t, s2, ctx, dir, "Inc"), // 2
		}
		if diff := cmp.Diff([]int64{1, 2, 1, 3, 2}, got); diff != "" {
			t.Errorf("counter sequence mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("EditKeepsOldSessionNewSessionSeesNew", func(t *testing.T) {
		dir := writePinPkg(t)
		base := NewEngine(dir)
		s1 := base.NewSession()

		if diff := cmp.Diff(int64(1), callPinInt(t, s1, ctx, dir, "Version")); diff != "" {
			t.Fatalf("Version before edit mismatch (-want +got):\n%s", diff)
		}

		// edit the source: Version now returns 2
		writePinSrc(t, dir, 2)

		if diff := cmp.Diff(int64(1), callPinInt(t, s1, ctx, dir, "Version")); diff != "" {
			t.Errorf("Version in existing session after edit mismatch (-want +got):\n%s", diff)
		}
		s2 := base.NewSession()
		if diff := cmp.Diff(int64(2), callPinInt(t, s2, ctx, dir, "Version")); diff != "" {
			t.Errorf("Version in fresh session after edit mismatch (-want +got):\n%s", diff)
		}
	})
}
