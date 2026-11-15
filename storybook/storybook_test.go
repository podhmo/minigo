package storybook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/storybook"
)

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	return dir
}

func TestDiscover(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod": "module x\n\ngo 1.26\n",
		"app.go": "package app\n",
		"story.go": `//go:build story

package app

import "net/http"

// StoryB renders b.
func StoryB() { RenderB() }

// StoryA renders a.
func StoryA() { RenderA() }

// StoryHttpC drives the handler.
func StoryHttpC(w http.ResponseWriter, r *http.Request) { serve() }

func RenderA()  {}
func RenderB()  {}
func serve()    {}
func notStory() {}
`,
		"other.go":    "package app\n\nfunc StoryOther() {}\n",
		"app_test.go": "package app\n\nfunc StoryInTest() {}\n",
	})
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(stories))
	for _, s := range stories {
		names = append(names, s.Name)
	}
	if diff := cmp.Diff([]string{"StoryA", "StoryB", "StoryHttpC"}, names); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
	for _, s := range stories {
		if s.Doc == "" || !strings.HasPrefix(s.Pos, filepath.Join(dir, "story.go")) {
			t.Errorf("missing doc or position: %+v", s)
		}
	}
	byName := map[string]storybook.Story{}
	for _, s := range stories {
		byName[s.Name] = s
	}
	if byName["StoryA"].IsHTTP() || byName["StoryB"].IsHTTP() || !byName["StoryHttpC"].IsHTTP() {
		t.Errorf("kinds: %+v", stories)
	}
	if diff := cmp.Diff(map[string]string{
		"StoryA":     "RenderA",
		"StoryB":     "RenderB",
		"StoryHttpC": "",
	}, map[string]string{
		"StoryA":     byName["StoryA"].Group,
		"StoryB":     byName["StoryB"].Group,
		"StoryHttpC": byName["StoryHttpC"].Group,
	}); diff != "" {
		t.Errorf("groups (-want +got):\n%s", diff)
	}
}

// A story-prefixed func whose signature fits neither shape fails
// discovery instead of being silently skipped — a typo'd signature
// must not drop the story.
func TestDiscoverRejectsBadShape(t *testing.T) {
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
			dir := writePackage(t, map[string]string{
				"go.mod":   "module x\n\ngo 1.26\n",
				"app.go":   "package app\n",
				"story.go": "//go:build story\n\npackage app\n\nimport \"net/http\"\n\n" + tc.decl + "\n",
			})
			_, err := storybook.Discover(dir, "story")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want invalid signature error %q, got %v", tc.want, err)
			}
		})
	}
}

// Only print stories group. A handler story calling an unqualified
// helper must not end up pointing an index at a grid that cannot
// frame it.
func TestHTTPStoryStaysUngrouped(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod": "module x\n\ngo 1.26\n",
		"app.go": "package app\n",
		"story.go": `//go:build story

package app

import "net/http"

func StoryHttpX(w http.ResponseWriter, r *http.Request) { helper() }
func helper() {}
`,
	})
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("", stories[0].Group); diff != "" {
		t.Errorf("http story group (-want +got):\n%s", diff)
	}
}

// Files the plain build already selects are not story files, even when
// they name StoryXxx — discovery only counts what the tag brings in.
func TestDiscoverIgnoresPlainBuildFiles(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod":  "module x\n\ngo 1.26\n",
		"app.go":  "package app\n\nfunc StoryAlways() {}\n",
		"unix.go": "//go:build unix\n\npackage app\n\nfunc StoryUnix() {}\n",
		"story.go": `//go:build story

package app

func StoryTagged() {}
`,
	})
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"StoryTagged"}, []string{stories[0].Name}); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
}

// A package with no story funcs is an error, not an empty list —
// preview servers tolerate it explicitly, silent emptiness does not.
func TestDiscoverNoStories(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod": "module x\n\ngo 1.26\n",
		"app.go": "package app\n",
	})
	_, err := storybook.Discover(dir, "story")
	if err == nil || !strings.Contains(err.Error(), "no Story funcs") {
		t.Fatalf("want no-stories error, got %v", err)
	}
}
