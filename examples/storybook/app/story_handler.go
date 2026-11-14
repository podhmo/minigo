//go:build story

package app

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
)

// Handler stories: the fallback kind, for code whose rendering is not split
// out of the handler. The StoryHttp prefix marks the handler shape,
// func(http.ResponseWriter, *http.Request): storybook drives it through a
// recorder, so the preview replays the real status and headers, not just
// the body. These stories hand their own request to the real mux, so
// routing and path values behave as in production.
//
// The direct minigo CLI can only run the print kind — it calls a story by
// name with no arguments — so handler stories need the storybook helper.

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

// StoryHttpShowUser drives the real mux for a successful lookup.
func StoryHttpShowUser(w http.ResponseWriter, r *http.Request) {
	(&Handler{Store: users}).Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/1", nil))
}

// StoryHttpShowUserNotFound drives the handler's error path; the preview
// replays the 404 status and text/plain body a print story would hide.
func StoryHttpShowUserNotFound(w http.ResponseWriter, r *http.Request) {
	(&Handler{Store: users}).Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/404", nil))
}

// StoryHttpListUsersByTag fixes a tag filter and sorts by ID; the
// preview replays the X-Total-Count response header too.
func StoryHttpListUsersByTag(w http.ResponseWriter, r *http.Request) {
	(&Handler{Store: users}).Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users?tag=go&sort=id", nil))
}
