package storybook_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/podhmo/minigo/storybook"
)

// The reload guarantee: every Render reads the current file contents —
// a source edit or a //go:embed'ed file edit shows up on the next call
// with no restart and no new base engine.
func TestRenderReadsCurrentFiles(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod":   "module x\n\ngo 1.26\n",
		"page.txt": "one\n",
		"story.go": `//go:build story

package app

import (
	_ "embed"
	"fmt"
)

//go:embed page.txt
var page string

// StoryX prints the embedded file.
func StoryX() { fmt.Print(page) }
`,
	})
	b := &storybook.Book{Dir: dir, Tag: "story"}
	render := func() string {
		t.Helper()
		res, err := b.Render(context.Background(), "StoryX")
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Body)
	}
	if got := render(); got != "one\n" {
		t.Fatalf("first render: %q", got)
	}
	writeFile(t, filepath.Join(dir, "page.txt"), "two\n")
	if got := render(); got != "two\n" {
		t.Errorf("embedded file edit: %q", got)
	}
	writeFile(t, filepath.Join(dir, "story.go"), `//go:build story

package app

import "fmt"

// StoryX prints the embedded file.
func StoryX() { fmt.Print("three") }
`)
	if got := render(); got != "three" {
		t.Errorf("story source edit: %q", got)
	}
}

// Without Watch the live-reload endpoints answer 404, no page gets a
// script tag, and bodies pass through untouched.
func TestLiveReloadOff(t *testing.T) {
	b := &storybook.Book{Dir: t.TempDir()}
	if b.ScriptTag() != "" {
		t.Error("want no script tag without Watch")
	}
	mux := http.NewServeMux()
	b.MountLiveReload(mux)
	for _, p := range []string{storybook.LiveReloadEventsPath, storybook.LiveReloadScriptPath} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
	if got := b.AppendLiveReload("text/html; charset=utf-8", []byte("<p>x</p>")); string(got) != "<p>x</p>" {
		t.Errorf("body must pass through while off: %q", got)
	}
}

// Watching serves the script, stamps HTML bodies, and publishes a
// reload event when a file under the book's directory changes.
func TestLiveReload(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod":   "module x\n\ngo 1.26\n",
		"app.go":   "package app\n",
		"story.go": "//go:build story\n\npackage app\n\nfunc StoryX() {}\n",
	})
	b := &storybook.Book{Dir: dir, Tag: "story"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Watch(ctx, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if b.ScriptTag() == "" {
		t.Fatal("want a script tag while watching")
	}
	mux := http.NewServeMux()
	b.MountLiveReload(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res, err := http.Get(ts.URL + storybook.LiveReloadScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if ct := res.Header.Get("Content-Type"); ct != "text/javascript; charset=utf-8" || !strings.Contains(string(body), "EventSource") {
		t.Errorf("script: Content-Type %q, body %.40q", ct, body)
	}

	// AppendLiveReload stamps HTML bodies only.
	if got := string(b.AppendLiveReload("text/html; charset=utf-8", []byte("<p>x</p>"))); !strings.Contains(got, "_livereload.js") {
		t.Errorf("html body misses the script: %q", got)
	}
	if got := string(b.AppendLiveReload("text/plain; charset=utf-8", []byte("x"))); got != "x" {
		t.Errorf("non-HTML body must pass through: %q", got)
	}

	// A file change publishes a reload event to subscribed pages.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+storybook.LiveReloadEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("events: Content-Type %q", ct)
	}
	r := bufio.NewReader(res.Body)
	// The first message means the handler has subscribed.
	if got := readEvent(t, r); got != "retry: 1000\n" {
		t.Fatalf("first message %q", got)
	}
	writeFile(t, filepath.Join(dir, "story.go"), "//go:build story\n\npackage app\n\nfunc StoryX() {}\nfunc StoryY() {}\n")
	if got := readEvent(t, r); got != "event: reload\ndata: \n" {
		t.Errorf("event %q", got)
	}
}

// readEvent reads one SSE message, up to the blank line that ends it.
func readEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			return b.String()
		}
		b.WriteString(line)
	}
}

func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
