# storybook — render net/http views with minigo, Storybook style

An experiment: preview the HTML a `net/http` application renders without
building or running the application. Stories are ordinary Go functions in
files behind a build tag, so the real binary never contains them:

```go
//go:build story

package app

// StoryUser renders the user page with a fixed user.
func StoryUser(w http.ResponseWriter, r *http.Request) {
	RenderUser(w, &User{ID: "1", Name: "Alice"})
}
```

`storybook` interprets the package with minigo and the tag enabled, calls
the story with a host `httptest.ResponseRecorder` and request, and shows
the status, headers and body it wrote.

```console
$ go run . -list ./testdata/app              # name, position, doc comment
$ go run . ./testdata/app StoryUser          # print one story's body
$ go run . -o out ./testdata/app             # out/index.html + one page per story
$ go run . -http :6006 ./testdata/app        # browse; every request re-interprets
```

- A story is an exported top-level `func StoryXxx(w http.ResponseWriter, r
  *http.Request)` in a file the tag selects and plain `go build` drops.
  A `Story`-prefixed func with any other shape is an error, not skipped.
- `-http` forwards the query string, so `/story/StoryUserFromQuery?name=Bob`
  works as a story control. Source edits show up on reload.
- `-tag` changes the build tag; `-src pkg,...` interprets those packages
  from source (`minigo run --src`).

`testdata/app` is a small application (`handler.go`) and its stories
(`story_user.go`). `book_test.go` renders every story with minigo and
compares against `testdata/golden`; `oracle_story_test.go` (`-tags story`)
runs the same stories natively and compares against the same goldens.

Design notes: [docs/sketch/plan-storybook.md](../../docs/sketch/plan-storybook.md). Experiment log (Japanese): [docs/sketch/ja/experiment-storybook.md](../../docs/sketch/ja/experiment-storybook.md).
