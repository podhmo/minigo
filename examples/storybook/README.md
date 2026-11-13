# storybook — render net/http views with minigo, Storybook style

An experiment: preview the HTML a `net/http` application renders without
building or starting the application server. Stories are ordinary Go functions in
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

Run these commands from `examples/storybook`.

The ordinary application is independent of the preview tool:

```console
$ go run ./cmd/server -addr 127.0.0.1:8080
$ curl 'http://127.0.0.1:8080/users?tag=go&sort=id'
$ curl http://127.0.0.1:8080/users/1
```

`cmd/server/main.go` creates an `app.MemoryStore`, supplies it to
`app.Handler`, and calls `http.ListenAndServe` with `h.Routes()`. It does
not import minigo, register stories, or enable a build tag. The application
uses ordinary `net/http` and `html/template` code.

Start the preview tool separately:

```console
$ go run . -list ./app              # name, position, doc comment
$ go run . ./app StoryUser          # print one story's body
$ go run . -o out ./app             # out/index.html + one page per story
$ go run . -http :6006 ./app        # browse; every request re-interprets
```

- A story is an exported top-level `func StoryXxx(w http.ResponseWriter, r
  *http.Request)` in a file the tag selects and plain `go build` drops.
  A `Story`-prefixed func with any other shape is an error, not skipped.
- `-http` forwards the query string, so `/story/StoryUserFromQuery?name=Bob`
  works as a story control. Source edits show up on reload.
- `-tag` changes the build tag; `-src pkg,...` interprets those packages
  from source (`minigo run --src`).

The layout separates the application from the preview and its fixtures:

- `app/handler.go`: handlers, routes, and templates used by the real server.
- `app/store.go`: an in-memory `Store` implementation for the real server.
- `app/story_user.go`: tagged preview functions and their fake store.
- `cmd/server/main.go`: the ordinary application entry point.
- `main.go`, `book.go`, `page.go`: the preview tool.
- `testdata/golden`: expected responses, not application source.

The `Store` interface permits fake data in previews and tests; it is an
application design choice, not a requirement of minigo or Go. The story
tag, exported `Story*` names, and handler signature are conventions of this
preview tool, not limitations of minigo's entry-point API.

Go does not let a separate program import `package main` or look up a
package-level function by name. Neither restriction applies to minigo's
preview: handlers and stories may live in a server's `package main`, and
its `main()` is not called when rendering a story. The separate `app`
package here is a conventional server layout, not required for previewing.

`book_test.go` renders stories with minigo and checks the goldens, including
a copy of the handlers and stories in `package main` with an uncalled,
unfinished function that would prevent a native Go build. `app/handler_test.go`
checks ordinary application routes without the story tag.
`app/oracle_story_test.go` (`go test -tags story ./...`) calls the stories
natively and checks the same goldens. Its explicit list of functions is
only for native verification: preview users need no Go registry, generated
main, native test, or successful whole-application Go build. The native
test is in the target package so it does not require importing that package.

The preview executable runs as native Go; `Book.Render` uses minigo to
interpret the application and supplies the recorder and request. Running
`minigo run --tags story ./app StoryUser` directly cannot supply those two
arguments; use the preview command above or `Engine.Call` with host values.

Notes and results: [docs/sketch/ja/experiment-storybook.md](../../docs/sketch/ja/experiment-storybook.md).
