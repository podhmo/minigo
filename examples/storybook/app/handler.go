// Package app implements HTTP handlers and templates for user pages.
package app

import (
	"html/template"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type User struct {
	ID    string
	Name  string
	Tags  []string
	Badge template.HTML // trusted markup, inserted unescaped
}

// Initial is called from the template as {{.Initial}}.
func (u *User) Initial() string {
	if u.Name == "" {
		return "?"
	}
	return strings.ToUpper(u.Name[:1])
}

type Store interface {
	Find(id string) (*User, error)
	List() []*User
}

type Handler struct {
	Store Store
}

// ShowUser is the real handler: lookup, then render.
func (h *Handler) ShowUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.Store.Find(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderUser(w, u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ListUsers lists the users carrying any ?tag=, ordered by ?sort= (name
// or id; name when absent), and reports the match count in a header.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, ok := q["sort"]; !ok {
		q["sort"] = []string{"name"}
	}
	var hits []*User
	for _, u := range h.Store.List() {
		if len(q["tag"]) == 0 || slices.ContainsFunc(u.Tags, func(t string) bool { return slices.Contains(q["tag"], t) }) {
			hits = append(hits, u)
		}
	}
	slices.SortFunc(hits, func(a, b *User) int {
		if q.Get("sort") == "id" {
			return strings.Compare(a.ID, b.ID)
		}
		return strings.Compare(a.Name, b.Name)
	})
	// the effective query, echoed so the page shows what it filtered by
	var filters []string
	for k, vs := range q {
		filters = append(filters, k+"="+strings.Join(vs, ","))
	}
	slices.Sort(filters)
	w.Header()["X-Total-Count"] = []string{strconv.Itoa(len(hits))}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderUserList(w, UserListView{Users: hits, Filters: filters}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users", h.ListUsers)
	mux.HandleFunc("GET /users/{id}", h.ShowUser)
	return mux
}

// listFuncs are the view helpers the list template calls.
var listFuncs = template.FuncMap{
	"join": strings.Join,
	"plural": func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return strconv.Itoa(n) + " " + word + "s"
	},
}

var listTmpl = template.Must(template.New("list").Funcs(listFuncs).Parse(`<!doctype html>
<html><head><title>users</title><link rel="stylesheet" href="/static/app.css"></head>
<body>
<p class="filters">{{range .Filters}}<code>{{.}}</code> {{end}}</p>
<p>{{plural (len .Users) "user"}}</p>
<ul>{{range .Users}}<li><a href="/users/{{.ID}}">{{.Name}}</a> <small>{{join .Tags ", "}}</small></li>{{else}}<li>no users</li>{{end}}</ul>
</body></html>
`))

var userTmpl = template.Must(template.New("user").Parse(`<!doctype html>
<html><head><title>{{.Name}}</title><link rel="stylesheet" href="/static/app.css"></head>
<body>
<h1><span class="avatar">{{.Initial}}</span> {{.Name}} {{.Badge}}</h1>
{{if .Tags}}<ul>{{range .Tags}}<li>{{.}}</li>{{end}}</ul>{{else}}<p>no tags</p>{{end}}
<a href="/users/{{.ID}}?tab={{.Name}}">permalink</a>
</body></html>
`))

// RenderUser is the rendering half of ShowUser: a page-level component
// that turns view data into HTML, with no lookup and no HTTP.
func RenderUser(w io.Writer, u *User) error {
	return userTmpl.Execute(w, u)
}

// UserListView is the view data of the user list page.
type UserListView struct {
	Users   []*User
	Filters []string // effective query, shown as-is
}

// RenderUserList is the rendering half of ListUsers.
func RenderUserList(w io.Writer, v UserListView) error {
	return listTmpl.Execute(w, v)
}
