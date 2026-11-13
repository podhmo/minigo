package app

import (
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRoutesGolden exercises the ordinary application without the story build tag.
func TestRoutesGolden(t *testing.T) {
	h := &Handler{Store: MemoryStore{
		"1": {ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)},
		"2": {ID: "2", Name: "<script>alert(1)</script>"},
		"3": {ID: "3", Name: "Bob", Tags: []string{"go"}},
	}}
	mux := h.Routes()
	for _, tc := range []struct{ name, target string }{
		{"StoryListUsers", "/users"},
		{"StoryListUsersByTag", "/users?tag=go&sort=id"},
		{"StoryShowUser", "/users/1"},
		{"StoryShowUserNotFound", "/users/404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			want, err := os.ReadFile(filepath.Join("testdata", "golden", tc.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), goldenText(rec.Code, rec.Header(), rec.Body.Bytes())); diff != "" {
				t.Errorf("application response (-golden +got):\n%s", diff)
			}
		})
	}
}

// goldenText serializes native responses in the same format as the preview tests.
func goldenText(status int, h http.Header, body []byte) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d\n", status)
	for _, k := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[k] {
			fmt.Fprintf(&sb, "%s: %s\n", k, v)
		}
	}
	fmt.Fprintf(&sb, "\n%s", body)
	return sb.String()
}
