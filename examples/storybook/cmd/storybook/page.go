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

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>stories: {{.Dir}}</title>
<style>
body { margin: 0; display: flex; height: 100vh; font-family: sans-serif; }
nav { width: 18rem; overflow: auto; border-right: 1px solid #ccc; padding: .5rem; }
nav a { display: block; padding: .25rem; color: inherit; text-decoration: none; }
nav a:hover { background: #eef; }
nav a.active { background: #eef; font-weight: 600; }
nav small { display: block; color: #666; }
nav .group a { padding: .4rem .25rem 0; font-size: .9em; font-weight: 600; color: #444; }
.err { color: #b00; }
.viewport { display: flex; gap: .5em; margin: .5rem 0; font-size: .9em; }
.viewport a { padding: .1em .4em; border: 1px solid #ccc; border-radius: .3em; }
.viewport a.active { background: #def; border-color: #79b; }
iframe { flex: 1; border: 0; }
iframe.sized { flex: none; margin-inline: auto; border-inline: 1px dashed #ccc; }
</style></head>
<body>
<nav>
<strong>{{.Dir}}</strong>
{{if .Err}}<small class="err">{{.Err}}</small>{{end}}
<div class="viewport">{{range .Widths}}<a href="?w={{.}}&s={{$.S}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}full{{end}}</a>{{end}}</div>
{{range .Groups}}{{if .Name}}<div class="group"><a href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}} title="variants side by side">{{.Name}} &#10514;</a></div>{{end}}
{{range .Entries}}<a href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}}{{if eq .Name $.S}} class="active"{{end}}>{{.Name}}{{if .IsHTTP}} <small>&#8599;</small>{{end}}
<small>{{.Doc}}</small>{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}{{end}}</nav>
<iframe name="story"{{if .S}} src="/story/{{.S}}"{{else}}{{if .Default}} src="{{.Default}}"{{end}}{{end}}{{if .W}} class="sized" style="width: {{.W}}px"{{end}}></iframe>
</body></html>
`))

// gridTmpl lays one render function's stories side by side, so a CSS or
// template edit can be checked against every data state at once. Each
// variant is an iframe at the chosen width (?w=, flexible when unset).
var gridTmpl = template.Must(template.New("grid").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Name}} variants</title>
<style>
body { margin: 0; display: flex; flex-direction: column; height: 100vh; font-family: sans-serif; }
header { display: flex; gap: 1em; align-items: baseline; padding: .4rem .6rem; border-bottom: 1px solid #ccc; }
header a { color: inherit; }
.viewport { display: flex; gap: .5em; font-size: .9em; }
.viewport a { padding: .1em .4em; border: 1px solid #ccc; border-radius: .3em; text-decoration: none; color: inherit; }
.viewport a.active { background: #def; border-color: #79b; }
.row { flex: 1; display: flex; overflow-x: auto; }
figure { flex: 1 1 0; min-width: 0; margin: 0; display: flex; flex-direction: column; }
figure.sized { flex: none; }
figure + figure { border-left: 1px solid #ccc; }
figure iframe { flex: 1; border: 0; width: 100%; }
figcaption { padding: .3rem .6rem; font-size: .85em; border-top: 1px solid #ccc; }
</style></head>
<body>
<header><a href="{{.Index}}">&#8592; index</a><strong>{{.Name}}</strong>
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
	// Fail fast on pathological layouts before writing anything: the
	// snapshot must neither write inside the static source (recursive
	// self-copy) nor contain it (the re-export cleanup would erase it).
	src := b.staticRoot()
	srcOK := false
	if src != "" {
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			srcOK = true
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
			"Index":   "index.html",
			"Name":    groups[i].Name,
			"Stories": gridRows(groups[i]),
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
	if srcOK {
		dst := filepath.Join(dir, "static")
		// A fresh copy drops assets deleted upstream since the last
		// export; the containment checks above keep this safe.
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
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
				"Dir": b.Dir,
				"Err": err.Error(),
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
			"Dir":    b.Dir,
			"Groups": groups,
			"S":      sel,
			"W":      wpx,
			"Widths": []string{"", "320", "360", "768"},
		}); err != nil {
			slog.ErrorContext(r.Context(), "index", "err", err)
		}
	})
	mux.HandleFunc("GET /grid/{group}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("group")
		stories, err := b.Stories()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var entries []entry
		for _, s := range stories {
			if s.Group == name && !s.IsHTTP() {
				entries = append(entries, entry{Story: s, Href: "/story/" + s.Name})
			}
		}
		if len(entries) == 0 {
			http.NotFound(w, r)
			return
		}
		if err := gridTmpl.Execute(w, map[string]any{
			"Index":   "/",
			"Name":    name,
			"Stories": entries,
			"W":       r.URL.Query().Get("w"),
			"Widths":  []string{"", "320", "360", "768"},
		}); err != nil {
			slog.ErrorContext(r.Context(), "grid", "err", err)
		}
	})
	mux.HandleFunc("GET /story/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, err := b.Render(r.Context(), name)
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, err)
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
		w.WriteHeader(res.Status)
		w.Write(res.Body)
	})
	mux.Handle("GET /static/", b.staticHandler())
	return mux
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
