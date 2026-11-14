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
	"strings"
)

// entry is one row of the index page.
type entry struct {
	Story
	Href   string
	Target string // "story" in the static export, "_blank" for served http stories
	Err    string
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
{{range .Entries}}<a href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}}{{if eq .Name $.S}} class="active"{{end}}>{{.Name}}{{if .IsHTTP}} <small>&#8599;</small>{{end}}
<small>{{.Doc}}</small>{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}</nav>
<iframe name="story"{{if .S}} src="/story/{{.S}}"{{else}}{{with .Entries}} src="{{(index . 0).Href}}"{{end}}{{end}}{{if .W}} class="sized" style="width: {{.W}}px"{{end}}></iframe>
</body></html>
`))

// Export renders every story into dir: index.html plus <Story>.html.
// A failing story still gets its row (with the error) so the rest of
// the book is written, but Export then reports the failures.
func (b *Book) Export(ctx context.Context, dir string, stories []Story) error {
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
	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := indexTmpl.Execute(f, map[string]any{"Dir": b.Dir, "Entries": entries}); err != nil {
		return err
	}
	return errors.Join(errs...)
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
		if err := indexTmpl.Execute(w, map[string]any{
			"Dir":     b.Dir,
			"Entries": entries,
			"S":       sel,
			"W":       wpx,
			"Widths":  []string{"", "320", "360", "768"},
		}); err != nil {
			slog.ErrorContext(r.Context(), "index", "err", err)
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

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
