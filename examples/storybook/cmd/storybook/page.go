package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// entry is one row of the index page.
type entry struct {
	Story
	Href   string
	Target string // "story" in the static export, "_blank" for served http stories
	Err    string
}

// group is one render function's set of stories in the index. Ungrouped
// entries (handler stories) sit in the trailing group with an empty
// Name and no grid view: they are not variants of one template.
type group struct {
	Name    string
	Href    string // the side-by-side grid view
	Target  string
	Entries []entry
}

// groupEntries buckets entries by their story's render function
// (Story.Group), ordered by group name; ungrouped entries keep their
// place in a trailing unnamed group.
func groupEntries(entries []entry) []group {
	var order []string
	byName := map[string]*group{}
	var rest []entry
	for _, e := range entries {
		if e.Group == "" {
			rest = append(rest, e)
			continue
		}
		g, ok := byName[e.Group]
		if !ok {
			g = &group{Name: e.Group}
			byName[e.Group] = g
			order = append(order, e.Group)
		}
		g.Entries = append(g.Entries, e)
	}
	slices.Sort(order)
	groups := make([]group, 0, len(order)+1)
	for _, n := range order {
		groups = append(groups, *byName[n])
	}
	if len(rest) > 0 {
		groups = append(groups, group{Entries: rest})
	}
	return groups
}

// gridRows filters a group's entries down to what the grid can frame:
// handler stories replay their app's real response headers, which can
// deny framing, so they stay index links only.
func gridRows(g group) []entry {
	var rows []entry
	for _, e := range g.Entries {
		if !e.IsHTTP() {
			rows = append(rows, e)
		}
	}
	return rows
}

// chromeTokens is the color-scheme token block shared by indexTmpl and
// gridTmpl: a neutral light/dark scheme, so the chrome follows the OS
// theme instead of boxing a dark story in light UI. Stories render
// inside their own iframe documents, so these tokens never reach the
// previewed page.
const chromeTokens = `
:root {
  color-scheme: light dark;
  --bg-light: #f6f8fa; --surface-light: #fff; --surface-hover-light: #fbfcfd;
  --border-light: #d0d7de; --border-muted-light: #e4e9ee;
  --text-light: #1f2328; --text-muted-light: #656d76;
  --accent-light: #0969da; --accent-soft-light: #ddf4ff; --danger-light: #cf222e;
  --bg-dark: #0d1117; --surface-dark: #151b23; --surface-hover-dark: #1b222c;
  --border-dark: #3d444d; --border-muted-dark: #2a313c;
  --text-dark: #f0f6fc; --text-muted-dark: #9198a1;
  --accent-dark: #4493f8; --accent-soft-dark: #0c2d4f; --danger-dark: #f85149;
  --bg: var(--bg-light); --surface: var(--surface-light); --surface-hover: var(--surface-hover-light);
  --border: var(--border-light); --border-muted: var(--border-muted-light);
  --text: var(--text-light); --text-muted: var(--text-muted-light);
  --accent: var(--accent-light); --accent-soft: var(--accent-soft-light); --danger: var(--danger-light);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: var(--bg-dark); --surface: var(--surface-dark); --surface-hover: var(--surface-hover-dark);
    --border: var(--border-dark); --border-muted: var(--border-muted-dark);
    --text: var(--text-dark); --text-muted: var(--text-muted-dark);
    --accent: var(--accent-dark); --accent-soft: var(--accent-soft-dark); --danger: var(--danger-dark);
  }
}
@supports (color: light-dark(#000, #fff)) {
  :root {
    --bg: light-dark(var(--bg-light), var(--bg-dark));
    --surface: light-dark(var(--surface-light), var(--surface-dark));
    --surface-hover: light-dark(var(--surface-hover-light), var(--surface-hover-dark));
    --border: light-dark(var(--border-light), var(--border-dark));
    --border-muted: light-dark(var(--border-muted-light), var(--border-muted-dark));
    --text: light-dark(var(--text-light), var(--text-dark));
    --text-muted: light-dark(var(--text-muted-light), var(--text-muted-dark));
    --accent: light-dark(var(--accent-light), var(--accent-dark));
    --accent-soft: light-dark(var(--accent-soft-light), var(--accent-soft-dark));
    --danger: light-dark(var(--danger-light), var(--danger-dark));
  }
}
`

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>stories: {{.Dir}}</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
<style>` + chromeTokens + `
body { margin: 0; display: flex; height: 100vh; height: 100dvh; font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); }
nav { width: 17rem; flex: none; display: flex; flex-direction: column; background: var(--surface); border-right: 1px solid var(--border); }
nav > header { padding: .75rem .875rem; border-bottom: 1px solid var(--border-muted); }
.brand { font-size: .85rem; font-weight: 600; }
.dir { margin-top: .15rem; font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); word-break: break-all; }
.err { color: var(--danger); }
.viewport { display: flex; gap: .25rem; margin-top: .625rem; font-size: .78rem; }
.viewport a { flex: 1; padding: .2rem .4rem; text-align: center; color: var(--text-muted); text-decoration: none; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; }
.viewport a:hover { color: var(--accent); border-color: var(--accent); }
.viewport a.active { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); font-weight: 600; }
.list { flex: 1; overflow-y: auto; padding: .375rem .5rem .75rem; container-type: scroll-state; }
.group { display: block; margin-top: .75rem; padding: .25rem .5rem; font-size: .72rem; font-weight: 600; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); text-decoration: none; }
.group:first-child { margin-top: .375rem; }
.group:hover { color: var(--accent); }
.story { display: block; padding: .3rem .5rem; border-radius: 6px; color: inherit; text-decoration: none; line-height: 1.4; overflow-wrap: anywhere; }
.story:hover { background: var(--surface-hover); }
.story.active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
.story .ext { color: var(--text-muted); font-size: .8em; }
.story small { display: block; font-size: .75rem; font-weight: 400; color: var(--text-muted); }
.list::before, .list::after { content: ""; display: block; position: sticky; block-size: .5rem; z-index: 1; pointer-events: none; opacity: 0; transition: opacity .2s; }
.list::before { inset-block-start: 0; margin-block-end: -.5rem; background: radial-gradient(farthest-side at 50% 0, rgb(0 0 0 / .18), transparent); }
.list::after { inset-block-end: 0; margin-block-start: -.5rem; background: radial-gradient(farthest-side at 50% 100%, rgb(0 0 0 / .18), transparent); }
@container scroll-state(scrollable: block-start) { .list::before { opacity: 1; } }
@container scroll-state(scrollable: block-end) { .list::after { opacity: 1; } }
@supports not (container-type: scroll-state) {
  .list::before, .list::after { animation: edge-shadow linear both; animation-timeline: scroll(); }
  .list::before { animation-range: 0 2rem; }
  .list::after { animation-direction: reverse; animation-range: calc(100% - 2rem) 100%; }
}
@keyframes edge-shadow { to { opacity: 1; } }
iframe { flex: 1; border: 0; background: var(--surface); }
iframe.sized { flex: none; margin-inline: auto; border-inline: 1px dashed var(--border); }
</style></head>
<body>
<nav>
<header>
<div class="brand">stories</div>
<div class="dir">{{.Dir}}</div>
{{if .Err}}<small class="err">{{.Err}}</small>{{end}}
<div class="viewport">{{range .Widths}}<a href="?w={{.}}&s={{$.S}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}full{{end}}</a>{{end}}</div>
</header>
<div class="list">
{{range .Groups}}{{if .Name}}<a class="group" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}} title="variants side by side">{{.Name}} &#10514;</a>{{end}}
{{range .Entries}}<a class="story{{if eq .Name $.S}} active{{end}}" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}}{{if eq .Name $.S}} aria-current="true"{{end}}>{{.Name}}{{if .IsHTTP}}<span class="ext" title="opens in a new tab">&#8599;</span>{{end}}
{{if .Doc}}<small>{{.Doc}}</small>{{end}}{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}{{end}}
</div>
</nav>
<iframe name="story"{{if .S}} src="/story/{{.S}}"{{else}}{{if .Default}} src="{{.Default}}"{{end}}{{end}}{{if .W}} class="sized" style="width: {{.W}}px"{{end}}></iframe>
</body></html>
`))

// gridTmpl lays one render function's stories side by side, so a CSS or
// template edit can be checked against every data state at once. Each
// variant is an iframe at the chosen width (?w=, flexible when unset).
var gridTmpl = template.Must(template.New("grid").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>{{.Name}} variants</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
<style>` + chromeTokens + `
body { margin: 0; display: flex; flex-direction: column; height: 100vh; height: 100dvh; font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); }
header { display: flex; gap: .875rem; align-items: baseline; padding: .5rem .875rem; background: var(--surface); border-bottom: 1px solid var(--border); }
header .back { color: var(--text-muted); text-decoration: none; font-size: .85rem; }
header .back:hover { color: var(--accent); }
header strong { font-size: .85rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
header .viewport { margin-inline-start: auto; }
.viewport { display: flex; gap: .25rem; font-size: .78rem; }
.viewport a { padding: .2rem .4rem; text-align: center; color: var(--text-muted); text-decoration: none; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; }
.viewport a:hover { color: var(--accent); border-color: var(--accent); }
.viewport a.active { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); font-weight: 600; }
.row { flex: 1; display: flex; overflow-x: auto; }
figure { flex: 1 1 0; min-width: 0; margin: 0; display: flex; flex-direction: column; background: var(--surface); }
figure.sized { flex: none; }
figure + figure { border-left: 1px solid var(--border); }
figure iframe { flex: 1; border: 0; width: 100%; }
figcaption { padding: .3rem .6rem; font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); border-top: 1px solid var(--border-muted); }
</style></head>
<body>
<header><a class="back" href="{{.Index}}"{{if .IndexTarget}} target="{{.IndexTarget}}"{{end}}>&#8592; index</a><strong>{{.Name}}</strong>
<div class="viewport">{{range .Widths}}<a href="?w={{.}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}flex{{end}}</a>{{end}}</div>
</header>
<div class="row">
{{range .Stories}}<figure{{if $.W}} class="sized"{{end}}><iframe src="{{.Href}}"{{if $.W}} style="width: {{$.W}}px"{{end}}></iframe><figcaption>{{.Name}}</figcaption></figure>
{{end}}</div>
</body></html>
`))

// Export renders every story into dir: index.html plus <Story>.html and
// a copy of the static directory, so serving the export root resolves
// /static/ the same way the live preview does. A failing story still
// gets its row (with the error) so the rest of the book is written, but
// Export then reports the failures.
func (b *Book) Export(ctx context.Context, dir string, stories []Story) error {
	// The containment checks below compare against the absolute static
	// root, so the output must be absolute too — a relative -o would
	// otherwise let dst compare unequal to src and RemoveAll it.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = absDir
	// Fail fast on pathological layouts before writing anything: the
	// snapshot must neither write inside the static source (recursive
	// self-copy) nor contain it (the re-export cleanup would erase it).
	src := b.staticRoot()
	srcOK := false
	if src != "" {
		// The checks run whenever a static root is named — not only
		// when it is a directory: a `static` that is a regular file or
		// a path os.Stat cannot read must not pass the checks and reach
		// the unconditional cleanup below.
		dst := filepath.Join(dir, "static")
		// Compare link-resolved paths: a symlinked static root can
		// hide containment that the literal paths do not show. The
		// literal check still matters — a source reached through a
		// link inside the output dies with the re-export cleanup.
		realSrc, realDst := resolvePath(src), resolvePath(dst)
		inside := func(a, b string) bool { // a inside-or-equal b
			rel, err := filepath.Rel(b, a)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if inside(dst, src) || inside(realDst, realSrc) {
			return fmt.Errorf("export output must not be inside the static directory: %s", dst)
		}
		if inside(src, dst) || inside(realSrc, realDst) {
			return fmt.Errorf("static directory must not be inside the export output: %s", src)
		}
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			srcOK = true
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var errs []error
	entries := make([]entry, len(stories))
	for i, s := range stories {
		entries[i] = entry{Story: s, Href: s.Name + ".html", Target: "story"}
		res, err := b.Render(ctx, s.Name)
		if err != nil {
			entries[i].Err = firstLine(err.Error())
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, s.Name+".html"), res.Body, 0o644); err != nil {
			return err
		}
	}
	// The variants grid exports too: one page per render function whose
	// iframes point at the exported story pages.
	groups := groupEntries(entries)
	for i := range groups {
		if groups[i].Name == "" {
			continue
		}
		groups[i].Href = "grid_" + groups[i].Name + ".html"
		groups[i].Target = "story"
		f, err := os.Create(filepath.Join(dir, groups[i].Href))
		if err != nil {
			return err
		}
		err = gridTmpl.Execute(f, map[string]any{
			"Index": "index.html",
			// The grid renders inside the story iframe; the back link
			// must leave it, not nest a second index inside the first.
			"IndexTarget": "_top",
			"Name":        groups[i].Name,
			"Stories":     gridRows(groups[i]),
		})
		f.Close()
		if err != nil {
			return err
		}
	}
	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	def := ""
	for _, e := range entries {
		if !e.IsHTTP() {
			def = e.Href
			break
		}
	}
	if err := indexTmpl.Execute(f, map[string]any{"Dir": b.Dir, "Groups": groups, "Default": def}); err != nil {
		return err
	}
	// The snapshot mirrors the static source: a fresh copy drops assets
	// deleted upstream since the last export, and a vanished source
	// means no static in the output either. The containment checks
	// above keep the removal safe.
	dst := filepath.Join(dir, "static")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if srcOK {
		if err := copyTree(src, dst); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// copyTree copies regular files under src into dst, preserving the
// layout. Symlinks resolve the way http.FileServer resolves them: linked
// assets land in the snapshot, and an alias naming a directory already
// copied is copied again under its own name. What is refused is a link
// cycle — a directory on its own copy path (self -> .) — tracked by the
// real paths currently on the copy stack, not by every directory seen;
// a dangling link, which the live server would answer 404, is skipped.
func copyTree(src, dst string) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyDir(real, dst, map[string]bool{real: true})
}

func copyDir(dir, dst string, active map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		to := filepath.Join(dst, e.Name())
		mode := e.Type()
		if mode&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			info, err := os.Stat(target)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if active[target] {
					continue
				}
				active[target] = true
				err := copyDir(target, to, active)
				delete(active, target)
				if err != nil {
					return err
				}
				continue
			}
			mode = info.Mode().Type()
		}
		if e.IsDir() {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			active[real] = true
			err = copyDir(real, to, active)
			delete(active, real)
			if err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Handler serves the index at /, each story at /story/<Name>, and the
// application's static assets under /static/ (default <pkgdir>/static),
// so previews pick up stylesheet edits on a plain browser reload. A fresh
// interpretation on each request reflects source and template edits;
// CSS needs no re-execution at all. Story failures answer 500. Print
// stories answer 200; handler-shaped stories replay the status and
// headers their recorder captured.
func (b *Book) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		stories, err := b.Stories()
		if err != nil {
			// No stories yet (or a malformed one): show the error in the
			// nav rather than failing, so the book is usable while the
			// stories are written.
			if err := indexTmpl.Execute(w, map[string]any{
				"Dir":       b.Dir,
				"Err":       err.Error(),
				"ScriptTag": b.ScriptTag(),
			}); err != nil {
				slog.ErrorContext(r.Context(), "index", "err", err)
			}
			return
		}
		wpx := r.URL.Query().Get("w")
		entries := make([]entry, len(stories))
		for i, s := range stories {
			if s.IsHTTP() {
				// Handler stories replay the app's real response headers,
				// which can include frame-ancestors 'none' / X-Frame-Options:
				// DENY — they may not render inside the iframe, so they
				// open in a new tab with the headers intact.
				entries[i] = entry{Story: s, Href: "/story/" + s.Name, Target: "_blank"}
				continue
			}
			href := "?s=" + s.Name
			if wpx != "" {
				href += "&w=" + wpx
			}
			entries[i] = entry{Story: s, Href: href}
		}
		// ?s=<Name> selects the previewed story so width switching (a full
		// index reload) keeps it; an unknown or missing name falls back to
		// the first non-HTTP entry.
		sel := r.URL.Query().Get("s")
		known := false
		for _, s := range stories {
			if s.Name == sel && !s.IsHTTP() {
				known = true
				break
			}
		}
		if !known {
			sel = ""
			for _, s := range stories {
				if !s.IsHTTP() {
					sel = s.Name
					break
				}
			}
		}
		groups := groupEntries(entries)
		for i := range groups {
			if groups[i].Name == "" {
				continue
			}
			groups[i].Href = "/grid/" + groups[i].Name
			if wpx != "" {
				groups[i].Href += "?w=" + wpx
			}
		}
		if err := indexTmpl.Execute(w, map[string]any{
			"Dir":       b.Dir,
			"Groups":    groups,
			"S":         sel,
			"W":         wpx,
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
		}); err != nil {
			slog.ErrorContext(r.Context(), "index", "err", err)
		}
	})
	mux.HandleFunc("GET /grid/{group}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("group")
		stories, err := b.Stories()
		if err != nil {
			b.errorPage(w, http.StatusInternalServerError, err)
			return
		}
		var entries []entry
		for _, s := range stories {
			if s.Group == name && !s.IsHTTP() {
				entries = append(entries, entry{Story: s, Href: "/story/" + s.Name})
			}
		}
		if len(entries) == 0 {
			b.errorPage(w, http.StatusNotFound, errors.New("no stories in this group"))
			return
		}
		if err := gridTmpl.Execute(w, map[string]any{
			"Index":     "/",
			"Name":      name,
			"Stories":   entries,
			"W":         r.URL.Query().Get("w"),
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
		}); err != nil {
			slog.ErrorContext(r.Context(), "grid", "err", err)
		}
	})
	mux.HandleFunc("GET /story/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, err := b.Render(r.Context(), name)
		if err != nil {
			b.errorPage(w, http.StatusInternalServerError, err)
			return
		}
		// Handler-shaped stories recorded real response headers and a
		// status — replay them so the preview exercises what the app's
		// handler emits (security headers, CSP, non-200 states).
		h := w.Header()
		for k, vs := range res.Header {
			h[k] = vs
		}
		if len(res.Body) > 0 && h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/html; charset=utf-8")
		}
		// A story opened in its own tab reloads too — only when the
		// response is HTML, so non-HTML bodies stay byte-identical.
		body := b.AppendLiveReload(h, res.Body)
		w.WriteHeader(res.Status)
		w.Write(body)
	})
	mux.Handle("GET /static/", b.staticHandler())
	b.MountLiveReload(mux)
	return mux
}

// errorPage answers an error response. While watching it is a small
// HTML page carrying the reload script — a text/plain error cannot run
// it, and a page that loses the script never rejoins the reload loop,
// so a transient mid-edit failure would stick until a manual refresh.
func (b *Book) errorPage(w http.ResponseWriter, status int, err error) {
	if tag := b.ScriptTag(); tag != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<pre>%s</pre>%s", template.HTMLEscapeString(err.Error()), tag)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, err)
}

// resolvePath resolves symlinks in the existing prefix of p and rejoins
// the not-yet-existing tail, so containment checks work on planned paths.
func resolvePath(p string) string {
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
