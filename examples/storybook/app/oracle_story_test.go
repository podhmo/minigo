//go:build story

package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestNativeGolden renders the stories with the go toolchain (`go test
// -tags story`): the goldens minigo is checked against must be what the
// compiled application writes.
func TestNativeGolden(t *testing.T) {
	stories := map[string]struct {
		handler http.HandlerFunc
		target  string
	}{
		"StoryListUsers":        {StoryListUsers, "/"},
		"StoryListUsersByTag":   {StoryListUsersByTag, "/?tag=go&sort=id"},
		"StoryShowUser":         {StoryShowUser, "/"},
		"StoryShowUserNotFound": {StoryShowUserNotFound, "/"},
		"StoryUser":             {StoryUser, "/"},
		"StoryUserEscaped":      {StoryUserEscaped, "/"},
		"StoryUserFromQuery":    {StoryUserFromQuery, "/?name=Bob&tag=x&tag=%3Cy%3E"},
	}
	for name, story := range stories {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			story.handler(rec, httptest.NewRequest(http.MethodGet, story.target, nil))
			want, err := os.ReadFile(filepath.Join("testdata", "golden", name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			got := goldenText(rec.Code, rec.Header(), rec.Body.Bytes())
			if diff := cmp.Diff(string(want), got); diff != "" {
				t.Errorf("native rendering (-golden +got):\n%s", diff)
			}
		})
	}
}
