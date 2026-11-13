// Package app is a small net/http application whose rendering half the
// storybook stories (story_*.go, behind //go:build story) call with
// dummy values.
package app

import (
	"html/template"
	"net/http"
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
	RenderUser(w, u)
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", h.ShowUser)
	return mux
}

var userTmpl = template.Must(template.New("user").Parse(`<!doctype html>
<html><head><title>{{.Name}}</title></head>
<body>
<h1><span class="avatar">{{.Initial}}</span> {{.Name}} {{.Badge}}</h1>
{{if .Tags}}<ul>{{range .Tags}}<li>{{.}}</li>{{end}}</ul>{{else}}<p>no tags</p>{{end}}
<a href="/users/{{.ID}}?tab={{.Name}}">permalink</a>
</body></html>
`))

// RenderUser is the rendering half of ShowUser.
func RenderUser(w http.ResponseWriter, u *User) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := userTmpl.Execute(w, u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
