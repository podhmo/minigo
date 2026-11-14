# Preview server-rendered pages with ordinary Story functions

The goal is what Storybook does for SPA components, applied to a classic
server-rendered MPA: render a page in a real browser for a chosen data
state and adjust its CSS and layout, without real data or a running
server. minigo runs a Story by name, without a test or another `main`.

In Storybook terms the component is the page's render function (view data
to HTML), not the handler that also looks data up. Stories here therefore
come in two kinds:

- **Page stories** (`app/story_page.go`, the primary kind) call a render
  function such as `RenderUser` or `RenderUserList` with one data state:
  a typical user, a hostile name, a very long name with many tags, an
  empty list. No store, request, or recorder is needed. They are plain
  `func StoryXxx()` printing HTML to stdout.
- **Handler stories** (`app/story_handler.go`, the fallback) have the
  handler shape `func StoryHttpXxx(w http.ResponseWriter, r *http.Request)`
  and drive the real mux through `httptest`. The helper replays the
  recorded status and headers — a 404 stays a 404 and `X-Total-Count`
  survives — where a print story would flatten everything to a 200 body.

The value is in looking at these pages in a browser while editing CSS and
templates, not in asserting their HTML; the goldens below are regression
checks of the preview path, not checks of appearance.

This example's `app` is a small synthetic collection of handlers and
templates, not evidence that an existing large application needs no changes.
It has no fake-only server command. Application code imports neither minigo
nor the preview helper; fixtures live in tagged stories and tests. The
pages link a real external stylesheet (`app/static/app.css`) so the preview
serves it the way an application's static directory would.

Run the following commands from `examples/storybook`.

## Choose a workflow

| Purpose | Targets | When to use them |
|---|---|---|
| Direct minigo use | `render` | Run one Story and redirect its HTML to a file. This is sufficient to inspect a self-contained page in a browser. |
| Optional minigo helper | `list`, `export`, `preview` | Discover Stories, save them together, or repeatedly browse them while editing. These targets use `cmd/storybook`. |
| Verification | `test` | Check handler HTTP behavior and the direct CLI and helper outputs. Not a prerequisite for previewing. |

For the smallest workflow, use `make render STORY=StoryUser > /tmp/story-user.html`
and open the file in a browser. You can stop there.

## The primitive: run a Story directly

A Story is a zero-argument function that owns its display conditions and
prints only the rendered HTML to stdout. A page story builds view data and
calls the render function:

```go
//go:build story

package app

import (
    "fmt"
    "strings"
)

func StoryUserListEmpty() {
    var b strings.Builder
    if err := RenderUserList(&b, UserListView{Filters: []string{"sort=name", "tag=nobody"}}); err != nil {
        panic(err)
    }
    fmt.Print(b.String())
}
```

Handler stories instead get the handler signature. `StoryHttpListUsersByTag`
hands a `?tag=go&sort=id` request to the application's mux, so routing and
path values behave as in production and the recorder keeps the real status
and headers. The `StoryHttp` prefix selects this shape; the direct CLI
below calls stories with no arguments, so it can only run the print kind —
handler stories need the `cmd/storybook` helper.

With the minigo executable installed:

```sh
minigo run --tags story ./app StoryUser > /tmp/story-user.html
```

Using this repository's command sources, without installing anything:

```sh
make render STORY=StoryUser > /tmp/story-user.html
```

Open `/tmp/story-user.html` in a real browser to inspect it. This path
needs neither the storybook helper nor a Go test, registration map, or
application server. `make render` writes no command banner to stdout.
The `story` tag keeps fixtures out of ordinary Go builds; it is a choice
for this example, not a requirement of minigo's arbitrary-entry API.

## Why not a Go test

A Go test could call a render function and save its HTML too, but each
Story would need an explicit reference in a test, the whole package would
have to build, and a test is not where one edits CSS while watching a
browser. minigo calls the entry by name, including entries in
`package main`, without running the application's `main()`. An uncalled
function with an undefined name need not block the preview. Source
syntax, initialization, and the executed path must still be supported by
minigo.

## Optional helper: discover, select, and browse

Specifying function names and opening individual files gets repetitive.
`cmd/storybook` adds discovery and a browser index over the same Stories:

```sh
make list
make preview                       # http://127.0.0.1:6006/
make export OUT=/tmp/storybook-out  # open index.html
```

`export` saves a snapshot: run it again after source changes. `preview`
keeps an HTTP server running and forks a fresh interpreter session on each
request, so a browser reload reflects edits to Go code, templates, and
stories. It also serves `app/static/` under `/static/`, so stylesheet
edits need no re-execution at all, and the index offers fixed iframe
widths (320px, 360px, 768px) for checking narrow layouts. Use it when
repeated exports become inconvenient; it is not needed for a one-time
HTML inspection.

Equivalent commands from this directory:

```sh
go run ./cmd/storybook -list ./app
go run ./cmd/storybook ./app StoryUser
go run ./cmd/storybook -http 127.0.0.1:6006 ./app
go run ./cmd/storybook -static ./app/static -http 127.0.0.1:6006 ./app
go run ./cmd/storybook -o /tmp/storybook-out ./app
```

`-static` defaults to `<pkgdir>/static`; pass it to serve a different
directory.

The helper is an ordinary Go executable embedding minigo as a library.
`Book` scans the target source directory for exported top-level `Story*`
functions in files selected only by the story tag, checks each against its
shape — `func()` for `StoryXxx`, `func(http.ResponseWriter, *http.Request)`
for `StoryHttpXxx` — then calls the chosen function with `Engine.Call` on a
session forked from a shared base engine. `Engine.NewSession` keeps build
options and host bindings warm while re-reading the source package per
request, so `Book.base` is built once and edits still show up on reload.
`minigo.WithOutput` captures stdout on each session without replacing the
host's `os.Stdout`. Handler stories instead run against an injected
`httptest` recorder. There is no application import or integration with an
application's server main.

Print stories serve captured stdout as `text/html` with HTTP 200; handler
stories replay the recorder's real status and headers, so security headers
and error states render as the application emits them — and a response
forbidding framing opens in a separate tab instead of the index's iframe.
Interpreter failures produce preview HTTP 500.

The sample pages link `/static/app.css`, served from `app/static` — a real
page's images, JavaScript, or routes likewise need an appropriate serving
setup; file URLs and preview URLs are not the application's original
origin. An iframe view is not an end-to-end browser test of the application.

## Extension: supply fake standard-library bindings

A custom preview command embedding minigo can replace host bindings,
including standard-library intrinsics. This can fix the clock or supply
predictable external I/O results for interpreted application code without
adding an application interface solely for previews.

`Engine.Bind` registers a host package at an import path, including a
standard-library path; `Engine.BoundOf` exposes an existing bound package —
and renaming it there is how a package whose declared name differs from
its last path element (e.g. `goldmark-alert` declaring `package alert`)
becomes reachable by that name. Bind names a bound package after the last
import-path element. Configure replacements before application
initialization and execution; a session forked with `Engine.NewSession`
inherits them, so `Book.baseEngine` is the single place they belong. If
re-registering a whole package, preserve its other required symbols.
`ModeSource` bypasses host bindings, so the target package's execution
mode matters too.

Replacements affect interpreted calls through those bindings, not calls
inside compiled host implementations. This is an extension point of the
existing minigo API; the current `cmd/storybook` has no CLI configuration
for fake bindings. A custom runner can therefore provide a controlled
preview environment in addition to discovery and browser serving.

## Layout and checks

- `app/handler.go`: ordinary handlers, routes, templates, and the render functions they call.
- `app/static/app.css`: the stylesheet the templates link at `/static/app.css`.
- `app/story_page.go`: page stories calling render functions with data states.
- `app/story_handler.go`: fallback handler stories (`StoryHttp*`) and their fake store.
- `app/testdata/golden`: expected response bodies only.
- `app/handler_test.go`: native HTTP routing, status/header assertions, and bodies.
- `cmd/storybook/`: optional discovery, capture, export, and browser helper.
- `cmd/storybook/book_test.go`: direct minigo CLI and helper output checks.

```sh
make help
make test
```

Override `STORY`, `PKG`, `OUT`, or `PREVIEW_ADDR` as needed. `PKG` selects
the direct minigo and helper target. From the repository root use
`make -C examples/storybook <target>`. Stop `make preview` with Ctrl-C.

The tests compare direct minigo CLI stdout and helper stdout with the same
body goldens; the handler test checks the routes against them natively.
They also verify source reload, package-main entry execution without
calling main, discovery, export, and preview HTTP behavior. These are behavioral and regression
checks for the selected fixtures, not proof of visual equivalence in a
browser or usefulness on an existing application.

Design and evidence limits:
[docs/sketch/ja/experiment-storybook.md](../../docs/sketch/ja/experiment-storybook.md).
