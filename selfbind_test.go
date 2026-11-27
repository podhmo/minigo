package minigo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// TestSelfBoundEngineNewSession pins the embedding API inside a script:
// importing github.com/podhmo/minigo must use the host embedding API
// rather than recursively interpreting the engine implementation. The
// NewSession call exercises the construction invariants missing in the
// storybook regression.
func TestSelfBoundEngineNewSession(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedSession")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("ok", got); diff != "" {
		t.Fatalf("NestedSession() mismatch (-want +got):\n%s", diff)
	}
}

// TestSelfBoundEngineCallKeepsScriptRecorder pins Engine.Call's args as
// pass-through values: marshaling the script recorder into a data
// projection must not invoke Result early, or the first empty snapshot
// would hide the handler's later status and headers.
func TestSelfBoundEngineCallKeepsScriptRecorder(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedRecorder")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("404|yes|x", got); diff != "" {
		t.Fatalf("NestedRecorder() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineCallSnapshotsScriptContainers(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedSliceIsolation")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("orig", got); diff != "" {
		t.Fatalf("NestedSliceIsolation() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundScriptValuesCrossEmbeddingOptions(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedOutputWriter")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("9", got); diff != "" {
		t.Errorf("NestedOutputWriter() mismatch (-want +got):\n%s", diff)
	}
	got, err = e.Run(context.Background(), "./testdata/selfbind", "NestedHostPolicyScriptFunc")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("denied", got); diff != "" {
		t.Errorf("NestedHostPolicyScriptFunc() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundScriptWriterSurvivesNestedEngineReuse(t *testing.T) {
	e := NewEngine(".")
	for i, want := range []string{"ok", "9", "18"} {
		name := []string{"Setup", "Use", "Use"}[i]
		got, err := e.Run(context.Background(), "./testdata/selfbindreuse", name)
		if err != nil {
			t.Fatalf("%s() %d: %v", name, i, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("%s() %d mismatch (-want +got):\n%s", name, i, diff)
		}
	}
}

func TestSelfBoundScriptHostPolicySurvivesNestedEngineReuse(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbindreuse", "SetupPolicy")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("ok", got); diff != "" {
		t.Fatalf("SetupPolicy() mismatch (-want +got):\n%s", diff)
	}
	for i := 0; i < 2; i++ {
		got, err := e.Run(context.Background(), "./testdata/selfbindreuse", "UsePolicy")
		if err != nil {
			t.Fatalf("UsePolicy() %d: %v", i, err)
		}
		if diff := cmp.Diff("denied", got); diff != "" {
			t.Fatalf("UsePolicy() %d mismatch (-want +got):\n%s", i, diff)
		}
	}
}

func TestSelfBoundNilEmbeddedWriteStringDoesNotFallBack(t *testing.T) {
	e := NewEngine(".")
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedNilEmbeddedWriteString")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("error", got); diff != "" {
		t.Fatalf("NestedNilEmbeddedWriteString() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineInheritsHostPolicy(t *testing.T) {
	e := NewEngine(".", WithHostPolicy(func(path, sym string) bool {
		return !(path == "os" && sym == "Getenv")
	}))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedHostPolicy")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("denied", got); diff != "" {
		t.Fatalf("NestedHostPolicy() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineUsesParentWorkingDir(t *testing.T) {
	mod := t.TempDir()
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/childmod\n\ngo 1.23\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mod, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "app", "main.go"), []byte("package app\n\nfunc Answer() string { return \"ok\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(".", WithWorkingDir(mod))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedModule")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("ok", got); diff != "" {
		t.Fatalf("NestedModule() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineInheritsModeDeny(t *testing.T) {
	e := NewEngine(".", WithPackageModes(map[string]PackageMode{"strings": ModeDeny}))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedModeDeny")
	if err != nil {
		t.Fatal(err)
	}
	gotText, ok := got.(string)
	if !ok {
		t.Fatalf("NestedModeDeny() = %v (%T), want string", got, got)
	}
	if !strings.Contains(gotText, `import of "strings" denied by package policy`) {
		t.Fatalf("NestedModeDeny() = %q, want strings package denial", gotText)
	}
}

func TestSelfBoundEngineModeDenyPositiveControls(t *testing.T) {
	e := NewEngine(".")
	for _, fn := range []string{"NestedModeDeny", "NestedResetPackageModes"} {
		got, err := e.Run(context.Background(), "./testdata/selfbind", fn)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("allowed", got); diff != "" {
			t.Errorf("%s() mismatch (-want +got):\n%s", fn, diff)
		}
	}
}

func TestSelfBoundEngineModeDenySurvivesPostConstructionOption(t *testing.T) {
	e := NewEngine(".", WithPackageModes(map[string]PackageMode{"strings": ModeDeny}))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedResetPackageModes")
	if err != nil {
		t.Fatal(err)
	}
	gotText, ok := got.(string)
	if !ok {
		t.Fatalf("NestedResetPackageModes() = %v (%T), want string", got, got)
	}
	if !strings.Contains(gotText, `import of "strings" denied by package policy`) {
		t.Fatalf("NestedResetPackageModes() = %q, want strings package denial", gotText)
	}
}

func TestSelfBoundEnginePolicySurvivesPostConstructionOption(t *testing.T) {
	e := NewEngine(".", WithHostPolicy(func(path, sym string) bool {
		return !(path == "os" && sym == "Getenv")
	}))
	got, err := e.Run(context.Background(), "./testdata/selfbind", "NestedResetHostPolicy")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("denied", got); diff != "" {
		t.Fatalf("NestedResetHostPolicy() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineSessionKeepsExplicitBind(t *testing.T) {
	e := NewEngine(".")
	e.Bind("github.com/podhmo/minigo", map[string]runtime.Value{
		"Marker": func() string { return "custom" },
	})
	got, err := e.NewSession().Run(context.Background(), "./testdata/selfbind", "NestedMarker")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("custom", got); diff != "" {
		t.Fatalf("NestedMarker() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelfBoundEngineDeniedUnderAllowedRoots(t *testing.T) {
	root, err := filepath.Abs("./testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(".", WithAllowedRoots(root))
	p := e.BoundOf("github.com/podhmo/minigo")
	if p == nil {
		t.Fatal("restricted self-import must still be a bound package")
	}
	if _, ok := p.Globals.Get("NewEngine"); ok {
		t.Fatal("NewEngine must be absent under AllowedRoots")
	}
	_, err = e.Run(context.Background(), "./testdata/selfbind", "NestedSession")
	if err == nil || !strings.Contains(err.Error(), "NewEngine") {
		t.Fatalf("minigo.NewEngine must be unbound under AllowedRoots, got %v", err)
	}
}
