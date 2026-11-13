//go:build story

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/storybook/testdata/app"
)

// TestNativeGolden renders the stories with the go toolchain (`go test
// -tags story`): the goldens minigo is checked against must be what the
// compiled application writes.
func TestNativeGolden(t *testing.T) {
	stories := map[string]func(http.ResponseWriter, *http.Request){
		"StoryShowUser":         app.StoryShowUser,
		"StoryShowUserNotFound": app.StoryShowUserNotFound,
		"StoryUser":             app.StoryUser,
		"StoryUserEscaped":      app.StoryUserEscaped,
		"StoryUserFromQuery":    app.StoryUserFromQuery,
	}
	if len(stories) != len(storyTargets) {
		t.Fatalf("stories and storyTargets disagree: %d vs %d", len(stories), len(storyTargets))
	}
	for name, fn := range stories {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fn(rec, httptest.NewRequest(http.MethodGet, storyTargets[name], nil))
			want, err := os.ReadFile(goldenPath(name))
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
