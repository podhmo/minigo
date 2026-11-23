package storybook

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// entry is one row of the index page.
type entry struct {
	Story
	Href   string // where the row's anchor points (serve: ?s=, export: Name.html)
	Src    string // what the preview iframe loads (serve: /story/, export: Name.html); "" when it cannot be framed
	Grid   string // this story's variants grid, for the toolbar's grid action
	Hay    string // lowercased haystack the filter matches: name + doc + group
	Target string // "story" in the static export, "_blank" for served http stories
	Blurb  string // Doc with the redundant story-name prefix dropped
	Hidden bool   // a non-match under ?q=: rendered but hidden so JS can restore it
	Err    string
}

// matchStory answers whether a story survives the ?q= filter: every
// whitespace-separated token must appear in the lowercased haystack of
// name + doc + group.
func matchStory(s Story, q string) bool {
	hay := strings.ToLower(s.Name + " " + s.Doc + " " + s.Group)
	for _, t := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// haystack is the searchable text carried on each row's data-haystack
// attribute; the live filter matches it the same way matchStory does.
func haystack(s Story) string {
	return strings.ToLower(s.Name + " " + s.Doc + " " + s.Group)
}

// blurb is the doc line without its redundant "StoryXxx renders ..."
// prefix — the row already shows the name, so repeating it only adds
// noise to the list.
func blurb(s Story) string {
	rest, ok := strings.CutPrefix(s.Doc, s.Name)
	if !ok {
		return s.Doc
	}
	return strings.TrimLeft(rest, " \t:—-")
}

// group is one render function's set of stories in the index. Ungrouped
// entries (handler stories) sit in the trailing group with an empty
// Name and no grid view: they are not variants of one template.
type group struct {
	Name    string
	Href    string // where the grid pill points (serve: ?g=, export: grid_X.html)
	Src     string // the grid document itself (serve: /grid/, export: grid_X.html)
	Target  string
	Hidden  bool // every entry filtered out under ?q=
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
			// stories are written — and still name the tag-skipped
			// stories, since that error can be exactly "no Story funcs".
			excluded, _ := b.Excluded()
			if err := indexTmpl.Execute(w, map[string]any{
				"Dir":       b.Dir,
				"Err":       err.Error(),
				"Excluded":  excluded,
				"ScriptTag": b.ScriptTag(),
			}); err != nil {
				slog.ErrorContext(r.Context(), "index", "err", err)
			}
			return
		}
		wpx := r.URL.Query().Get("w")
		q := r.URL.Query().Get("q")
		state := ""
		if wpx != "" {
			state += "&w=" + wpx
		}
		if q != "" {
			state += "&q=" + url.QueryEscape(q)
		}
		// Non-matching stories are rendered with a hidden marker rather
		// than omitted: no-JS readers still get the filtered view, and
		// the live filter can un-hide rows when the query changes.
		entries := make([]entry, len(stories))
		matched := 0
		for i, s := range stories {
			e := entry{Story: s, Hay: haystack(s), Blurb: blurb(s), Hidden: q != "" && !matchStory(s, q)}
			if s.IsHTTP() {
				// Handler stories replay the app's real response headers,
				// which can include frame-ancestors 'none' / X-Frame-Options:
				// DENY — they may not render inside the iframe, so they
				// open in a new tab with the headers intact.
				e.Href = "/story/" + s.Name
				e.Target = "_blank"
			} else {
				e.Href = "?s=" + s.Name + state
				e.Src = "/story/" + s.Name
				if s.Group != "" {
					e.Grid = "/grid/" + s.Group
				}
			}
			if !e.Hidden {
				matched++
			}
			entries[i] = e
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
			visible := false
			for _, e := range groups[i].Entries {
				if !e.Hidden {
					visible = true
					break
				}
			}
			groups[i].Hidden = !visible
			if groups[i].Name == "" {
				continue
			}
			groups[i].Href = "?g=" + groups[i].Name + state
			groups[i].Src = "/grid/" + groups[i].Name
		}
		// ?g=<renderFunc> previews the variants grid inside the index
		// frame, the same pane stories render in; the grid page itself
		// stays reachable for a full-width compare.
		g := r.URL.Query().Get("g")
		src := ""
		if g != "" {
			for _, gr := range groups {
				if gr.Name == g {
					src = "/grid/" + g
					if wpx != "" {
						src += "?w=" + wpx
					}
				}
			}
			if src == "" {
				g = ""
			}
		}
		// The toolbar mirrors the selected story: its group, the group's
		// grid, and the story's own URL for opening it in a tab.
		selGroup, selPos, selGrid, open := "", "", "", ""
		for _, e := range entries {
			if e.Name == sel {
				selGroup, selPos, selGrid, open = e.Group, e.Pos, e.Grid, e.Src
				break
			}
		}
		// Story funcs outside the tag guard are reported rather than
		// dropped silently — best-effort: a scan failure must not take
		// the index down.
		excluded, _ := b.Excluded()
		td := map[string]any{
			"Dir":       b.Dir,
			"Groups":    groups,
			"S":         sel,
			"G":         g,
			"SelGroup":  selGroup,
			"SelPos":    selPos,
			"W":         wpx,
			"Q":         q,
			"Total":     len(stories),
			"Matched":   matched,
			"None":      q != "" && matched == 0,
			"Excluded":  excluded,
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
		}
		if g != "" {
			td["Src"] = src
			td["OpenHref"] = "/grid/" + g
		} else {
			td["Src"] = open
			td["OpenHref"] = open
			td["Sized"] = wpx != "" && sel != ""
			if selGrid != "" {
				td["GridHref"] = "?g=" + selGroup + state
				td["GridSrc"] = selGrid
			}
		}
		if err := indexTmpl.Execute(w, td); err != nil {
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
			"Index": "/",
			// The grid may render inside the index's preview frame; its
			// back/story links must replace the whole page, not nest a
			// second index inside the first.
			"IndexTarget": "_top",
			"Name":        name,
			"Stories":     entries,
			"W":           r.URL.Query().Get("w"),
			"Widths":      []string{"", "320", "360", "768"},
			"ScriptTag":   b.ScriptTag(),
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
	// Whatever the chrome did not claim goes to the book's own
	// application, when it mounts one — a handler story driving the
	// real mux gives the preview a fake-data instance of the app, so
	// in-page fetches (partial submits, fragment swaps) run end to end.
	if b.Fallback != nil {
		mux.Handle("/", b.Fallback)
	}
	return mux
}

// StaticDir is the absolute path of the directory served under
// /static/: Static when given, <Dir>/static otherwise.
func (b *Book) StaticDir() string {
	dir := b.Static
	if dir == "" {
		dir = filepath.Join(b.Dir, "static")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return root
}

// staticHandler serves the application's real assets under /static/, the
// same path layout.html-style references use, so previews pick up
// stylesheet edits on a plain browser reload.
func (b *Book) staticHandler() http.Handler {
	root := b.StaticDir()
	if root == "" {
		return http.NotFoundHandler()
	}
	return http.StripPrefix("/static/", http.FileServer(http.Dir(root)))
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
