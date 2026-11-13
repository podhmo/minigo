//go:build story

package app

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var htmlDir = flag.String("html-dir", "", "also write native Story output as HTML files in this directory")

// TestStories is a Go-only way to call stories and optionally save
// their HTML for browser inspection. This explicit list is only for Go tests.
func TestStories(t *testing.T) {
	stories := map[string]func(){
		"StoryListUsers":        StoryListUsers,
		"StoryListUsersByTag":   StoryListUsersByTag,
		"StoryShowUser":         StoryShowUser,
		"StoryShowUserNotFound": StoryShowUserNotFound,
		"StoryUser":             StoryUser,
		"StoryUserEscaped":      StoryUserEscaped,
		"StoryUserFromQuery":    StoryUserFromQuery,
	}
	if *htmlDir != "" {
		if err := os.MkdirAll(*htmlDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, story := range stories {
		t.Run(name, func(t *testing.T) {
			got := captureStory(t, story)
			want, err := os.ReadFile(filepath.Join("testdata", "golden", name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), string(got)); diff != "" {
				t.Errorf("native stdout (-golden +got):\n%s", diff)
			}
			if *htmlDir != "" {
				if err := os.WriteFile(filepath.Join(*htmlDir, name+".html"), got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Native stories print to os.Stdout. Capture serially in a file to avoid
// pipe-buffer limits; restore stdout even if a story panics.
func captureStory(t *testing.T, story func()) []byte {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "story-*.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stdout := os.Stdout
	defer func() { os.Stdout = stdout }()
	os.Stdout = f
	story()
	os.Stdout = stdout
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
