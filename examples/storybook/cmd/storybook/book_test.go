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

// Print stories run under the direct minigo CLI too; handler-shaped
// (StoryHttp*) stories need this helper's recorder drive.
var (
	printNames = []string{
		"StoryUser", "StoryUserEscaped", "StoryUserList", "StoryUserListEmpty", "StoryUserLongName",
	}
	httpNames = []string{
		"StoryHttpListUsersByTag", "StoryHttpShowUser", "StoryHttpShowUserNotFound",
	}
)

func storyNames() []string {
	return append(append([]string{}, httpNames...), printNames...)
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
		if s.Doc == "" || !strings.HasPrefix(s.Pos, filepath.Join(appDir, "story_")) {
			t.Errorf("missing doc or position: %+v", s)
		}
		if want := strings.HasPrefix(s.Name, "StoryHttp"); s.IsHTTP() != want {
			t.Errorf("%s: IsHTTP %v, want %v", s.Name, s.IsHTTP(), want)
		}
	}
	if diff := cmp.Diff(storyNames(), names); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
}

func TestStoriesRejectsBadShape(t *testing.T) {
	for _, tc := range []struct {
		decl, want string
	}{
		{"func StoryX(w http.ResponseWriter, r *http.Request) {}", "StoryX must be func()"},
		{"func StoryX() string { return \"\" }", "StoryX must be func()"},
		{"func StoryX[T any]() {}", "StoryX must be func()"},
		{"func StoryHttpX() {}", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
		{"func StoryHttpX(w, extra http.ResponseWriter, r *http.Request) {}", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
		{"func StoryHttpX(w http.ResponseWriter, r *http.Request) error { return nil }", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
	} {
		t.Run(tc.decl, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.26\n")
			writeFile(t, filepath.Join(dir, "app.go"), "package app\n")
			writeFile(t, filepath.Join(dir, "story.go"), "//go:build story\n\npackage app\n\nimport \"net/http\"\n\n"+tc.decl+"\n")
			_, err := (&Book{Dir: dir, Tag: "story"}).Stories()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want invalid signature error %q, got %v", tc.want, err)
			}
		})
	}
}

// The direct minigo CLI, not the optional Book helper, is the primitive.
// Its stdout and the native test both match the same body-only goldens.
// It calls a story with no arguments, so it covers the print kind only.
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
	for _, name := range printNames {
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
	// Handler stories replay the status and headers their recorder captured.
	wantStatus := map[string]int{
		"StoryHttpListUsersByTag":   http.StatusOK,
		"StoryHttpShowUser":         http.StatusOK,
		"StoryHttpShowUserNotFound": http.StatusNotFound,
	}
	for _, name := range storyNames() {
		t.Run(name, func(t *testing.T) {
			got, err := b.Render(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(golden(t, name)), string(got.Body)); diff != "" {
				t.Errorf("helper output (-golden +got):\n%s", diff)
			}
			want := wantStatus[name]
			if want == 0 {
				want = http.StatusOK
			}
			if diff := cmp.Diff(want, got.Status); diff != "" {
				t.Errorf("status (-want +got):\n%s", diff)
			}
		})
	}
	// The list handler reports the match count in a response header;
	// only the HTTP kind surfaces it.
	res, err := b.Render(context.Background(), "StoryHttpListUsersByTag")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"2"}, res.Header["X-Total-Count"]); diff != "" {
		t.Errorf("X-Total-Count (-want +got):\n%s", diff)
	}
}

// Neither an importable package nor a successful native build is needed.
func TestRenderMainPackage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"handler.go", "story_page.go"} {
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
	if diff := cmp.Diff(string(golden(t, "StoryUser")), string(got.Body)); diff != "" {
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
	// The snapshot ships the app's assets too, so serving the export root
	// resolves /static/ the way the live preview does.
	if _, err := os.Stat(filepath.Join(out, "static", "app.css")); err != nil {
		t.Errorf("export misses static assets: %v", err)
	}
}

// A minimal app with one story and one static asset.
func staticStories(t *testing.T) (string, []Story) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/staticx\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(dir, "app.go"), "package app\n")
	writeFile(t, filepath.Join(dir, "story.go"), "//go:build story\n\npackage app\n\nfunc StoryX() {}\n")
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "static", "app.css"), "a{}\n")
	b := &Book{Dir: dir, Tag: "story"}
	stories, err := b.Stories()
	if err != nil {
		t.Fatal(err)
	}
	return dir, stories
}

// http.FileServer serves a symlinked asset, so the snapshot carries its
// contents too.
func TestExportFollowsStaticSymlinks(t *testing.T) {
	dir, stories := staticStories(t)
	if err := os.Symlink("app.css", filepath.Join(dir, "static", "linked.css")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := (&Book{Dir: dir, Tag: "story"}).Export(context.Background(), out, stories); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "static", "linked.css"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("a{}\n", string(got)); diff != "" {
		t.Errorf("symlinked asset (-want +got):\n%s", diff)
	}
}

// The output root must not live inside the static directory: copying it
// would recurse into itself.
func TestExportRejectsStaticNesting(t *testing.T) {
	dir, stories := staticStories(t)
	err := (&Book{Dir: dir, Tag: "story"}).Export(context.Background(), filepath.Join(dir, "static", "nested"), stories)
	if err == nil {
		t.Error("export into the static directory must fail")
	}
}

func TestHandler(t *testing.T) {
	b := &Book{Dir: appDir, Tag: "story"}
	wantStatus := map[string]int{
		"StoryHttpListUsersByTag":   http.StatusOK,
		"StoryHttpShowUser":         http.StatusOK,
		"StoryHttpShowUserNotFound": http.StatusNotFound,
	}
	for _, name := range storyNames() {
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/story/"+name+"?name=Ignored", nil))
		want := wantStatus[name]
		if want == 0 {
			want = http.StatusOK
		}
		if diff := cmp.Diff(want, rec.Code); diff != "" {
			t.Errorf("preview status for %s (-want +got):\n%s", name, diff)
		}
		wantType := "text/html; charset=utf-8"
		if name == "StoryHttpShowUserNotFound" {
			wantType = "text/plain; charset=utf-8"
		}
		if diff := cmp.Diff(wantType, rec.Header().Get("Content-Type")); diff != "" {
			t.Errorf("content type for %s (-want +got):\n%s", name, diff)
		}
		if diff := cmp.Diff(string(golden(t, name)), rec.Body.String()); diff != "" {
			t.Errorf("preview for %s (-golden +got):\n%s", name, diff)
		}
	}
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `href="?s=StoryUser"`) {
		t.Error("index misses story link")
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Error("index misses new-tab link for http stories")
	}
	if !strings.Contains(body, `?w=360`) {
		t.Error("index misses viewport widths")
	}
	rec = httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/story/StoryMissing", nil))
	if diff := cmp.Diff(http.StatusInternalServerError, rec.Code); diff != "" {
		t.Errorf("missing story status (-want +got):\n%s", diff)
	}
	// The app's stylesheet is served at its original path.
	rec = httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if diff := cmp.Diff(http.StatusOK, rec.Code); diff != "" {
		t.Errorf("static status (-want +got):\n%s", diff)
	}
	if !strings.Contains(rec.Body.String(), ".avatar") {
		t.Error("static body misses app.css")
	}
}

// A discovery failure still serves: the index shows the error instead of
// crashing, so stories can be added to an empty package live.
func TestHandlerToleratesNoStories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/empty\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(dir, "app.go"), "package app\n")
	rec := httptest.NewRecorder()
	(&Book{Dir: dir, Tag: "story"}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if diff := cmp.Diff(http.StatusOK, rec.Code); diff != "" {
		t.Errorf("index status (-want +got):\n%s", diff)
	}
	if !strings.Contains(rec.Body.String(), "no Story funcs") {
		t.Error("index misses the discovery error")
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
