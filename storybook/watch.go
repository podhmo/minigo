package storybook

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Live reload is opt-in through [Book.Watch]: a preview page embedding
// [Book.ScriptTag] reloads itself whenever a file under the watched
// directories changes — a story, a template, a //go:embed'ed asset, or
// a static file. Until Watch runs the endpoints answer 404 and
// ScriptTag is empty, so a book leaving the option off serves no extra
// JavaScript and polls nothing.
const (
	// LiveReloadEventsPath is the SSE endpoint pages subscribe to.
	LiveReloadEventsPath = "/_livereload"
	// LiveReloadScriptPath serves the client script subscribing to it.
	LiveReloadScriptPath = "/_livereload.js"
)

// liveReloadScript reloads the page on a "reload" event. It also
// reloads when the connection comes back, which happens when the
// server restarts.
const liveReloadScript = `(() => {
  let lost = false;
  const es = new EventSource("` + LiveReloadEventsPath + `");
  es.addEventListener("reload", () => location.reload());
  es.onerror = () => { lost = true; };
  es.onopen = () => { if (lost) location.reload(); };
})();
`

// Watch starts polling the book's Dir (plus extraDirs, for assets a
// consumer serves from outside the package directory) every interval
// until ctx is done. A file added, removed, or changed — measured by
// path, size, and modification time, so no file is read — publishes a
// reload to every page subscribed on LiveReloadEventsPath. Call it
// before serving: the first fingerprint is taken before Watch returns,
// so a change made afterwards is always noticed.
func (b *Book) Watch(ctx context.Context, interval time.Duration, extraDirs ...string) error {
	dir, err := filepath.Abs(b.Dir)
	if err != nil {
		return err
	}
	dirs := append([]string{dir}, extraDirs...)
	last, err := fingerprint(dirs)
	if err != nil {
		return err
	}
	b.reload = newBroadcaster()
	go b.poll(ctx, interval, last, dirs)
	return nil
}

// ScriptTag is the markup a preview page embeds to reload on changes:
// a script element loading LiveReloadScriptPath while Watch is running,
// "" otherwise, so templates can stamp it unconditionally.
func (b *Book) ScriptTag() template.HTML {
	if b.reload == nil {
		return ""
	}
	return `<script src="` + LiveReloadScriptPath + `" defer></script>`
}

// AppendLiveReload returns body with the reload script appended when
// the response is HTML and Watch is running, so a story page opened in
// its own tab reloads too. Anything else comes back unchanged.
func (b *Book) AppendLiveReload(contentType string, body []byte) []byte {
	if b.reload == nil || !strings.Contains(contentType, "html") {
		return body
	}
	return append(body, b.ScriptTag()...)
}

// LiveReloadHandler serves the live-reload endpoints: the SSE stream at
// LiveReloadEventsPath and the client script at LiveReloadScriptPath.
// It answers 404 until Watch is running.
func (b *Book) LiveReloadHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+LiveReloadScriptPath, b.handleLiveReloadScript)
	mux.HandleFunc("GET "+LiveReloadEventsPath, b.handleLiveReloadEvents)
	return mux
}

// MountLiveReload registers both live-reload endpoints on mux.
func (b *Book) MountLiveReload(mux *http.ServeMux) {
	h := b.LiveReloadHandler()
	mux.Handle("GET "+LiveReloadScriptPath, h)
	mux.Handle("GET "+LiveReloadEventsPath, h)
}

func (b *Book) handleLiveReloadScript(w http.ResponseWriter, r *http.Request) {
	if b.reload == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.Write([]byte(liveReloadScript))
}

// handleLiveReloadEvents streams a "reload" event each time a watched
// file changes, until the client goes away.
func (b *Book) handleLiveReloadEvents(w http.ResponseWriter, r *http.Request) {
	if b.reload == nil {
		http.NotFound(w, r)
		return
	}
	ch, cancel := b.reload.subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	// Sending something right away fires the client's onopen.
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if _, err := fmt.Fprint(w, "event: reload\ndata: \n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// poll republishes whenever the watched trees' fingerprint changes. A
// mid-edit tree that fails to fingerprint keeps the previous state and
// is retried on the next tick — it does not kill the watcher.
func (b *Book) poll(ctx context.Context, interval time.Duration, last string, dirs []string) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fp, err := fingerprint(dirs)
		if err != nil {
			slog.ErrorContext(ctx, "storybook: watch fingerprint", "err", err)
			continue
		}
		if fp == last {
			continue
		}
		last = fp
		b.reload.publish()
		slog.InfoContext(ctx, "storybook: files changed; reloading pages", "dir", b.Dir)
	}
}

// broadcaster wakes every subscriber on publish. A subscriber that has
// not caught up yet gets one wake-up, not one per publish.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[chan struct{}]struct{}{}}
}

func (b *broadcaster) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *broadcaster) publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// fingerprint describes every file under dirs by path, size, and
// modification time. It is cheap enough to poll: no file is read.
// Dot-prefixed entries (e.g. a .git inside the package directory) are
// skipped, and a directory that does not exist yet contributes nothing —
// its later appearance is itself a change.
func fingerprint(dirs []string) (string, error) {
	var sb strings.Builder
	seen := map[string]bool{}
	for i, dir := range dirs {
		err := walkFingerprint(&sb, i, dir, seen)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
	}
	return sb.String(), nil
}

// walkFingerprint records one tree. A symlink entry keeps its own line
// (relinking moves it) plus its target's — an asset reachable through a
// link updates like a real file. A linked directory is walked too, with
// seen guarding against link cycles; an alias naming a tree already
// walked adds nothing new to notice. A dangling link records just its
// own line, matching the server's 404.
func walkFingerprint(sb *strings.Builder, i int, root string, seen map[string]bool) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if seen[real] {
		return nil
	}
	seen[real] = true
	return filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && name != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fmt.Fprintf(sb, "%d:%s\t%d\t%d\n", i, name, info.Size(), info.ModTime().UnixNano())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Stat(name)
			if err != nil {
				return nil
			}
			if target.IsDir() {
				return walkFingerprint(sb, i, name, seen)
			}
			fmt.Fprintf(sb, "%d:%s!\t%d\t%d\n", i, name, target.Size(), target.ModTime().UnixNano())
		}
		return nil
	})
}
