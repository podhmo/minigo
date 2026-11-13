//go:build story

package app

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
)

// Handler stories: the fallback kind, for code whose rendering is not split
// out of the handler. They need a fake Store and a request, and print only
// the response body.

// fakeStore is the dummy Store the handler stories hand to Handler.
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

var users = fakeStore{"1": alice, "2": hostile, "3": bob}

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

// StoryListUsersByTag fixes a tag filter and sorts by ID.
func StoryListUsersByTag() {
	req := httptest.NewRequest(http.MethodGet, "/users?tag=go&sort=id", nil)
	rec := httptest.NewRecorder()
	(&Handler{Store: users}).ListUsers(rec, req)
	fmt.Print(rec.Body.String())
}
