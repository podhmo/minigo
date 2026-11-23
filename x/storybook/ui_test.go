package storybook_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/podhmo/minigo/x/storybook"
)

// Handler's chrome claims only GET routes: the index, /story/, /grid/
// and /static/. Every other request — unmatched paths and non-GET
// methods on chrome paths alike — goes to the book's Fallback, so an
// app mounted there keeps its full method surface (POST forms, partial
// submits) while the chrome keeps browsing GETs.
func TestHandlerFallbackDispatch(t *testing.T) {
	dir := storyPackage(t, "func StoryA() {}", nil)
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "fallback")
		w.WriteHeader(http.StatusTeapot)
	})
	b := &storybook.Book{Dir: dir, Tag: "story", Fallback: fallback}
	h := b.Handler()

	for _, tc := range []struct {
		method, target, want string // want: "index" or "fallback"
	}{
		{http.MethodGet, "/", "index"},
		{http.MethodPost, "/", "fallback"}, // POST / is the app's, not the index's
		{http.MethodGet, "/app/issues", "fallback"},
		{http.MethodPost, "/story/StoryA", "fallback"}, // chrome stories are GET-only
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		switch tc.want {
		case "index":
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "stories:") {
				t.Errorf("%s %s: want the index, got %d %q", tc.method, tc.target, rec.Code, rec.Body.String()[:80])
			}
		case "fallback":
			if rec.Code != http.StatusTeapot || rec.Header().Get("X-From") != "fallback" {
				t.Errorf("%s %s: want the fallback, got %d", tc.method, tc.target, rec.Code)
			}
		}
	}

	// Without a fallback the unmatched paths answer 404.
	h = (&storybook.Book{Dir: dir, Tag: "story"}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/issues", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /app/issues without Fallback: got %d, want 404", rec.Code)
	}
}
