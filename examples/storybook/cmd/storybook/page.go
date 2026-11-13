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
	Status int // 0 when not rendered (serve mode renders lazily)
	Err    string
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>stories: {{.Dir}}</title>
<style>
body { margin: 0; display: flex; height: 100vh; font-family: sans-serif; }
nav { width: 18rem; overflow: auto; border-right: 1px solid #ccc; padding: .5rem; }
nav a { display: block; padding: .25rem; color: inherit; text-decoration: none; }
nav a:hover { background: #eef; }
nav small { display: block; color: #666; }
.err { color: #b00; }
iframe { flex: 1; border: 0; }
</style></head>
<body>
<nav>
<strong>{{.Dir}}</strong>
{{range .Entries}}<a href="{{.Href}}" target="story">{{.Name}}{{if .Status}} <small>{{.Status}}</small>{{end}}
<small>{{.Doc}}</small>{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}</nav>
<iframe name="story"{{with .Entries}} src="{{(index . 0).Href}}"{{end}}></iframe>
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
		entries[i] = entry{Story: s, Href: s.Name + ".html"}
		res, err := b.Render(ctx, s.Name, "/")
		if err != nil {
			entries[i].Err = firstLine(err.Error())
			errs = append(errs, err)
			continue
		}
		entries[i].Status = res.Status
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

// Handler serves the index at / and each story at /story/<Name>; the
// story's query string reaches its *http.Request. Stories are listed and
// re-interpreted on every request, so editing the package needs only a
// reload. A failing story answers 500 with minigo's error text.
func (b *Book) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		stories, err := b.Stories()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries := make([]entry, len(stories))
		for i, s := range stories {
			entries[i] = entry{Story: s, Href: "/story/" + s.Name}
		}
		if err := indexTmpl.Execute(w, map[string]any{"Dir": b.Dir, "Entries": entries}); err != nil {
			slog.ErrorContext(r.Context(), "index", "err", err)
		}
	})
	mux.HandleFunc("GET /story/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		target := "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		res, err := b.Render(r.Context(), name, target)
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, err)
			return
		}
		for k, vs := range res.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(res.Status)
		w.Write(res.Body)
	})
	return mux
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
