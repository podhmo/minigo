package storybook_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

	type result struct {
		Code int
		From string
	}
	for _, tc := range []struct {
		method, target string
		want           result
	}{
		{http.MethodGet, "/", result{http.StatusOK, ""}},
		{http.MethodPost, "/", result{http.StatusTeapot, "fallback"}}, // POST / is the app's, not the index's
		{http.MethodGet, "/app/issues", result{http.StatusTeapot, "fallback"}},
		{http.MethodPost, "/story/StoryA", result{http.StatusTeapot, "fallback"}}, // chrome stories are GET-only
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		got := result{Code: rec.Code, From: rec.Header().Get("X-From")}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s %s (-want +got):\n%s", tc.method, tc.target, diff)
		}
	}

	// The index names the discovered story: an empty body check would
	// also pass when discovery fails, since the chrome skeleton renders
	// regardless.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "StoryA") {
		t.Errorf("GET / does not list StoryA; body:\n%s", rec.Body.String())
	}

	// Without a fallback the unmatched paths answer 404.
	h = (&storybook.Book{Dir: dir, Tag: "story"}).Handler()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/issues", nil))
	if diff := cmp.Diff(http.StatusNotFound, rec.Code); diff != "" {
		t.Errorf("GET /app/issues without Fallback (-want +got):\n%s", diff)
	}
}
