//go:build story

package app

import (
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
)

// fakeStore is the dummy Store the stories hand to Handler.
type fakeStore map[string]*User

func (s fakeStore) Find(id string) (*User, error) {
	if u, ok := s[id]; ok {
		return u, nil
	}
	return nil, errors.New("no such user: " + id)
}

func (s fakeStore) List() []*User {
	return slices.Collect(maps.Values(s))
}

var users = fakeStore{
	"1": {ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)},
	"2": {ID: "2", Name: "<script>alert(1)</script>", Tags: nil},
	"3": {ID: "3", Name: "Bob", Tags: []string{"go"}},
}

// StoryUser renders the user page with a fixed user.
func StoryUser() {
	rec := httptest.NewRecorder()
	RenderUser(rec, users["1"])
	fmt.Print(rec.Body.String())
}

// StoryUserEscaped shows that a hostile name is escaped.
func StoryUserEscaped() {
	rec := httptest.NewRecorder()
	RenderUser(rec, users["2"])
	fmt.Print(rec.Body.String())
}

// StoryUserFromQuery renders data read from a fixed request query.
func StoryUserFromQuery() {
	req := httptest.NewRequest(http.MethodGet, "/?name=Bob&tag=x&tag=%3Cy%3E", nil)
	rec := httptest.NewRecorder()
	RenderUser(rec, &User{ID: "q", Name: req.URL.Query().Get("name"), Tags: req.URL.Query()["tag"]})
	fmt.Print(rec.Body.String())
}

// StoryShowUser calls the handler with a successful lookup.
func StoryShowUser() {
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	(&Handler{Store: users}).ShowUser(rec, req)
	fmt.Print(rec.Body.String())
}

// StoryShowUserNotFound prints the response body of the handler's error path.
func StoryShowUserNotFound() {
	req := httptest.NewRequest(http.MethodGet, "/users/404", nil)
	req.SetPathValue("id", "404")
	rec := httptest.NewRecorder()
	(&Handler{Store: users}).ShowUser(rec, req)
	fmt.Print(rec.Body.String())
}

// StoryListUsers lists every user in the default order.
func StoryListUsers() {
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	rec := httptest.NewRecorder()
	(&Handler{Store: users}).ListUsers(rec, req)
	fmt.Print(rec.Body.String())
}

// StoryListUsersByTag fixes a tag filter and sorts by ID.
func StoryListUsersByTag() {
	req := httptest.NewRequest(http.MethodGet, "/users?tag=go&sort=id", nil)
	rec := httptest.NewRecorder()
	(&Handler{Store: users}).ListUsers(rec, req)
	fmt.Print(rec.Body.String())
}
