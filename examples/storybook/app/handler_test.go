package app

import (
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRoutesGolden exercises the ordinary application without the story build tag.
func TestRoutesGolden(t *testing.T) {
	h := &Handler{Store: routeStore{
		"1": {ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)},
		"2": {ID: "2", Name: "<script>alert(1)</script>"},
		"3": {ID: "3", Name: "Bob", Tags: []string{"go"}},
	}}
	mux := h.Routes()
	for _, tc := range []struct {
		name, target       string
		status             int
		contentType, total string
	}{
		{"StoryUserList", "/users", 200, "text/html; charset=utf-8", "3"},
		{"StoryListUsersByTag", "/users?tag=go&sort=id", 200, "text/html; charset=utf-8", "2"},
		{"StoryShowUser", "/users/1", 200, "text/html; charset=utf-8", ""},
		{"StoryShowUserNotFound", "/users/404", 404, "text/plain; charset=utf-8", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			want, err := os.ReadFile(filepath.Join("testdata", "golden", tc.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			wantHeaders := http.Header{"Content-Type": {tc.contentType}}
			if tc.total != "" {
				wantHeaders.Set("X-Total-Count", tc.total)
			}
			if tc.status == http.StatusNotFound {
				wantHeaders.Set("X-Content-Type-Options", "nosniff")
			}
			if diff := cmp.Diff(tc.status, rec.Code); diff != "" {
				t.Errorf("HTTP status (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(wantHeaders, rec.Header()); diff != "" {
				t.Errorf("HTTP headers (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(string(want), rec.Body.String()); diff != "" {
				t.Errorf("application response (-golden +got):\n%s", diff)
			}
		})
	}
}

// routeStore supplies fixtures only to native HTTP tests.
type routeStore map[string]*User

func (s routeStore) Find(id string) (*User, error) {
	if u, ok := s[id]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("no such user: %s", id)
}

func (s routeStore) List() []*User {
	var users []*User
	for _, u := range s {
		users = append(users, u)
	}
	return users
}
