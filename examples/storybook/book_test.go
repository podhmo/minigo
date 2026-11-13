package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from minigo's rendering")

// storyTargets are the request URIs each golden story is rendered with.
var storyTargets = map[string]string{
	"StoryShowUser":         "/",
	"StoryShowUserNotFound": "/",
	"StoryUser":             "/",
	"StoryUserEscaped":      "/",
	"StoryUserFromQuery":    "/?name=Bob&tag=x&tag=%3Cy%3E",
}

// goldenText is the comparable form of a rendered story; both the minigo
// test and the native one (oracle_story_test.go) write it.
func goldenText(status int, h http.Header, body []byte) string {
	return fmt.Sprintf("%d %s\n\n%s", status, h.Get("Content-Type"), body)
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".txt")
}

func TestStories(t *testing.T) {
	b := &Book{Dir: "testdata/app", Tag: "story"}
	stories, err := b.Stories()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range stories {
		names = append(names, s.Name)
		if s.Doc == "" || !strings.HasPrefix(s.Pos, filepath.Join("testdata", "app", "story_user.go")+":") {
			t.Errorf("%s: want a doc line and a story_user.go position, got %+v", s.Name, s)
		}
	}
	want := []string{"StoryShowUser", "StoryShowUserNotFound", "StoryUser", "StoryUserEscaped", "StoryUserFromQuery"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
}

func TestStoriesRejectsBadShape(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n\ngo 1.26\n")
	write("app.go", "package x\n")
	write("story_x.go", "//go:build story\n\npackage x\n\nfunc StoryX() string { return \"\" }\n")
	_, err := (&Book{Dir: dir, Tag: "story"}).Stories()
	if err == nil || !strings.Contains(err.Error(), "StoryX must be func(http.ResponseWriter, *http.Request)") {
		t.Fatalf("want a shape error naming StoryX, got %v", err)
	}
}

func TestRenderGolden(t *testing.T) {
	b := &Book{Dir: "testdata/app", Tag: "story"}
	for name, target := range storyTargets {
		t.Run(name, func(t *testing.T) {
			res, err := b.Render(context.Background(), name, target)
			if err != nil {
				t.Fatal(err)
			}
			got := goldenText(res.Status, res.Header, res.Body)
			if *update {
				if err := os.WriteFile(goldenPath(name), []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), got); diff != "" {
				t.Errorf("minigo rendering (-golden +got):\n%s", diff)
			}
		})
	}
}

func TestRenderTrapReportsStory(t *testing.T) {
	b := &Book{Dir: "testdata/app", Tag: "story"}
	_, err := b.Render(context.Background(), "StoryMissing", "/")
	if err == nil || !strings.Contains(err.Error(), "StoryMissing") {
		t.Fatalf("want an error naming StoryMissing, got %v", err)
	}
}

// TestRenderRejectsNonStory pins that Render (and so /story/{name})
// only calls discovered stories, not any package-level func.
func TestRenderRejectsNonStory(t *testing.T) {
	b := &Book{Dir: "testdata/app", Tag: "story"}
	_, err := b.Render(context.Background(), "RenderUser", "/")
	if diff := cmp.Diff("RenderUser: not a story", fmt.Sprint(err)); diff != "" {
		t.Errorf("Render(RenderUser) error (-want +got):\n%s", diff)
	}
}

func TestExport(t *testing.T) {
	out := t.TempDir()
	b := &Book{Dir: "testdata/app", Tag: "story"}
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
			t.Errorf("index.html misses a link to %s", s.Name)
		}
		if _, err := os.Stat(filepath.Join(out, s.Name+".html")); err != nil {
			t.Error(err)
		}
	}
	if !bytes.Contains(index, []byte("StoryShowUserNotFound <small>404</small>")) {
		t.Errorf("index.html should show the 404 status:\n%s", index)
	}
}

func TestHandler(t *testing.T) {
	srv := httptest.NewServer((&Book{Dir: "testdata/app", Tag: "story"}).Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, `href="/story/StoryUser"`) {
		t.Errorf("index: %d\n%s", code, body)
	}
	if code, body := get("/story/StoryUserFromQuery?name=Carol"); code != 200 || !strings.Contains(body, "<title>Carol</title>") {
		t.Errorf("story with query: %d\n%s", code, body)
	}
	if code, _ := get("/story/StoryShowUserNotFound"); code != 404 {
		t.Errorf("not-found story: status %d, want 404", code)
	}
	if code, body := get("/story/StoryMissing"); code != 500 || !strings.Contains(body, "StoryMissing") {
		t.Errorf("missing story: %d\n%s", code, body)
	}
}
