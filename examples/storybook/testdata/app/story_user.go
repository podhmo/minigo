//go:build story

package app

import (
	"errors"
	"html/template"
	"net/http"
)

// fakeStore is the dummy Store the stories hand to Handler.
type fakeStore map[string]*User

func (s fakeStore) Find(id string) (*User, error) {
	if u, ok := s[id]; ok {
		return u, nil
	}
	return nil, errors.New("no such user: " + id)
}

var users = fakeStore{
	"1": {ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)},
	"2": {ID: "2", Name: "<script>alert(1)</script>", Tags: nil},
}

// StoryUser renders the user page with a fixed user.
func StoryUser(w http.ResponseWriter, r *http.Request) {
	RenderUser(w, users["1"])
}

// StoryUserEscaped shows that a hostile name is escaped.
func StoryUserEscaped(w http.ResponseWriter, r *http.Request) {
	RenderUser(w, users["2"])
}

// StoryUserFromQuery takes the name from ?name=, like a story control.
func StoryUserFromQuery(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "(set ?name=)"
	}
	RenderUser(w, &User{ID: "q", Name: name, Tags: r.URL.Query()["tag"]})
}

// StoryShowUser runs the whole handler against the dummy store.
func StoryShowUser(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("id", "1")
	(&Handler{Store: users}).ShowUser(w, r)
}

// StoryShowUserNotFound runs the handler's error path.
func StoryShowUserNotFound(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("id", "404")
	(&Handler{Store: users}).ShowUser(w, r)
}
