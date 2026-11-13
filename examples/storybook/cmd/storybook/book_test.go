package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const appDir = "../../app"

var storyNames = []string{
	"StoryListUsers", "StoryListUsersByTag", "StoryShowUser", "StoryShowUserNotFound",
	"StoryUser", "StoryUserEscaped", "StoryUserFromQuery",
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(appDir, "testdata", "golden", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStories(t *testing.T) {
	b := &Book{Dir: appDir, Tag: "story"}
	stories, err := b.Stories()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range stories {
		names = append(names, s.Name)
		if s.Doc == "" || !strings.HasPrefix(s.Pos, filepath.Join(appDir, "story_user.go")+":") {
			t.Errorf("missing doc or position: %+v", s)
		}
	}
	if diff := cmp.Diff(storyNames, names); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
}

func TestStoriesRejectsBadShape(t *testing.T) {
	for _, decl := range []string{
		"func StoryX(w http.ResponseWriter, r *http.Request) {}",
		"func StoryX() string { return \"\" }",
		"func StoryX[T any]() {}",
	} {
		t.Run(decl, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.26\n")
			writeFile(t, filepath.Join(dir, "app.go"), "package app\n")
			writeFile(t, filepath.Join(dir, "story.go"), "//go:build story\n\npackage app\n\n"+decl+"\n")
			_, err := (&Book{Dir: dir, Tag: "story"}).Stories()
			if err == nil || !strings.Contains(err.Error(), "StoryX must be func()") {
				t.Fatalf("want invalid signature error, got %v", err)
			}
		})
	}
}

// The direct minigo CLI, not the optional Book helper, is the primitive.
// Its stdout and the native test both match the same body-only goldens.
func TestDirectCLIStdout(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "minigo")
	build := exec.Command("go", "build", "-o", cli, "./")
	build.Dir = "../../../../cmd/minigo"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build minigo: %v\n%s", err, out)
	}
	dir, err := filepath.Abs(appDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range storyNames {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(cli, "run", "--tags", "story", dir, name)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			got, err := cmd.Output()
			if err != nil {
				t.Fatalf("direct CLI: %v\n%s", err, &stderr)
			}
			if diff := cmp.Diff(string(golden(t, name)), string(got)); diff != "" {
				t.Errorf("CLI stdout (-golden +got):\n%s", diff)
			}
		})
	}
}

func TestRenderGolden(t *testing.T) {
	b := &Book{Dir: appDir, Tag: "story"}
	for _, name := range storyNames {
		t.Run(name, func(t *testing.T) {
			got, err := b.Render(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(golden(t, name)), string(got)); diff != "" {
				t.Errorf("helper stdout (-golden +got):\n%s", diff)
			}
		})
	}
}

// Neither an importable package nor a successful native build is needed.
func TestRenderMainPackage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"handler.go", "story_user.go"} {
		src, err := os.ReadFile(filepath.Join(appDir, name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name), string(bytes.Replace(src, []byte("package app"), []byte("package main"), 1)))
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/server\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main() { panic(\"server must not start\") }\nfunc unfinished() { undefinedFunction() }\n")
	got, err := (&Book{Dir: dir, Tag: "story"}).Render(context.Background(), "StoryUser")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(golden(t, "StoryUser")), string(got)); diff != "" {
		t.Errorf("package main (-golden +got):\n%s", diff)
	}
}

func TestRenderRejectsNonStory(t *testing.T) {
	for _, name := range []string{"StoryMissing", "RenderUser"} {
		_, err := (&Book{Dir: appDir, Tag: "story"}).Render(context.Background(), name)
		if err == nil || !strings.Contains(err.Error(), "not a story") {
			t.Fatalf("want non-story error, got %v", err)
		}
	}
}

func TestExport(t *testing.T) {
	out := t.TempDir()
	b := &Book{Dir: appDir, Tag: "story"}
	stories, err := b.Stories()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Export(context.Background(), out, stories); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stories {
		if !bytes.Contains(index, []byte(`href="`+s.Name+`.html"`)) {
			t.Errorf("index misses %s", s.Name)
		}
		got, err := os.ReadFile(filepath.Join(out, s.Name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(string(golden(t, s.Name)), string(got)); diff != "" {
			t.Errorf("export (-golden +got):\n%s", diff)
		}
	}
}

func TestHandler(t *testing.T) {
	b := &Book{Dir: appDir, Tag: "story"}
	for _, name := range storyNames {
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/story/"+name+"?name=Ignored", nil))
		if diff := cmp.Diff(http.StatusOK, rec.Code); diff != "" {
			t.Errorf("preview status (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("text/html; charset=utf-8", rec.Header().Get("Content-Type")); diff != "" {
			t.Errorf("content type (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(string(golden(t, name)), rec.Body.String()); diff != "" {
			t.Errorf("preview (-golden +got):\n%s", diff)
		}
	}
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `href="/story/StoryUser"`) {
		t.Fatal("index misses story link")
	}
	rec = httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/story/StoryMissing", nil))
	if diff := cmp.Diff(http.StatusInternalServerError, rec.Code); diff != "" {
		t.Errorf("missing story status (-want +got):\n%s", diff)
	}
}

func TestHandlerReloadsSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/preview\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(dir, "app.go"), "package app\n")
	handler := (&Book{Dir: dir, Tag: "story"}).Handler()
	for _, text := range []string{"before", "after"} {
		writeFile(t, filepath.Join(dir, "story.go"), "//go:build story\n\npackage app\nimport \"fmt\"\nfunc StoryPage() { fmt.Print(\"<p>"+text+"</p>\") }\n")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/story/StoryPage", nil))
		if diff := cmp.Diff("<p>"+text+"</p>", rec.Body.String()); diff != "" {
			t.Errorf("reload (-want +got):\n%s", diff)
		}
	}
}

func TestRunPrintsStory(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{appDir, "StoryUser"}, &out); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(golden(t, "StoryUser")), out.String()); diff != "" {
		t.Errorf("command stdout (-golden +got):\n%s", diff)
	}
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}
