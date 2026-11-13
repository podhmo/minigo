# Preview handler output with an ordinary Story function

The goal is to inspect the HTML and appearance produced by an existing HTTP
handler or rendering function in a real browser. A Go test can do this too:
call the handler with a request and fake dependencies, save its body as an
HTML file, and open that file. minigo is useful when calling a function by
name is easier than adding a native test or another `main`.

This example's `app` is a small synthetic collection of handlers and
templates, not evidence that an existing large application needs no changes.
It has no fake-only server command. Application code imports neither minigo
nor the preview helper; fixtures live in tagged stories and tests.

Run the following commands from `examples/storybook`.

## Choose a workflow

| Purpose | Targets | When to use them |
|---|---|---|
| Direct minigo use | `render` | Run one Story and redirect its HTML to a file. This is sufficient to inspect a self-contained page in a browser. |
| Optional minigo helper | `list`, `export`, `preview` | Discover Stories, save them together, or repeatedly browse them while editing. These targets use `cmd/storybook`. |
| Comparison and verification | `native-html`, `test` | Compare the Go-only alternative and check native/minigo behavior. Neither is a prerequisite for minigo previewing. |

For the smallest workflow, use `make render STORY=StoryUser > /tmp/story-user.html`
and open the file in a browser. You can stop there. `native-html` demonstrates
the Go-only alternative; it is not an additional setup step.

## The primitive: run a Story directly

A Story is a zero-argument function that owns its display conditions and
prints only the rendered response body to stdout:

```go
//go:build story

package app

import (
    "fmt"
    "net/http/httptest"
)

func StoryUser() {
    rec := httptest.NewRecorder()
    RenderUser(rec, users["1"])
    fmt.Print(rec.Body.String())
}
```

Handler stories also construct their own request and fake store. For
example, `StoryListUsersByTag` fixes `?tag=go&sort=id` inside the function;
it is a different display condition from `StoryListUsers`.

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

## Comparison: Go alone is a valid alternative

```sh
make native-html OUT=/tmp/storybook-native
```

This calls the same Stories through `app/story_test.go` with the
native Go toolchain and saves `<Story>.html` files. Open, for example,
`/tmp/storybook-native/StoryUser.html` in a browser. The underlying command
is:

```sh
go test -tags story ./app -run '^TestStories$' -count=1 \
  -args -html-dir /tmp/storybook-native
```

For an application already easy to exercise through a test, saving its
rendered HTML this way may be sufficient. Native Go needs an explicit
reference to each Story in that test and a buildable package. minigo can
call the entry by name, including entries in `package main`, without
running the application's `main()`. An uncalled function with an undefined
name need not block the preview. Source syntax, initialization, and the
executed path must still be supported by minigo.

## Optional helper: discover, select, and browse

Specifying function names and opening individual files gets repetitive.
`cmd/storybook` adds discovery and a browser index over the same Stories:

```sh
make list
make preview                       # http://127.0.0.1:6006/
make export OUT=/tmp/storybook-out  # open index.html
```

`export` saves a snapshot: run it again after source changes. `preview`
keeps an HTTP server running and re-executes the selected Story on each
request, so a browser reload reflects edits. Use it when repeated exports
become inconvenient; it is not needed for a one-time HTML inspection.

Equivalent commands from this directory:

```sh
go run ./cmd/storybook -list ./app
go run ./cmd/storybook ./app StoryUser
go run ./cmd/storybook -http 127.0.0.1:6006 ./app
go run ./cmd/storybook -o /tmp/storybook-out ./app
```

The helper is an ordinary Go executable embedding minigo as a library.
`Book` scans the target source directory for exported top-level `Story*`
functions in files selected only by the story tag, rejects signatures other
than `func()`, then calls the chosen function with `Engine.Call`.
`minigo.WithOutput` captures stdout without replacing the host's
`os.Stdout`. A fresh engine per rendering reflects source changes on the
next browser reload. There is no application import, HTTP argument
injection, query control, or integration with an application's server main.

Successful previews serve captured stdout as `text/html` with HTTP 200.
A Story printing a handler's 404 body still produces a successful preview;
original HTTP status and headers are not encoded in stdout. Interpreter
failures produce preview HTTP 500. Use handler tests to check actual HTTP
status and headers.

The sample pages are self-contained HTML. If a real page references CSS,
images, JavaScript, or routes, those resources need an appropriate serving
setup; file URLs and preview URLs are not the application's original
origin. An iframe view is not an end-to-end browser test of the application.

## Extension: supply fake standard-library bindings

A custom preview command embedding minigo can replace host bindings,
including standard-library intrinsics. This can fix the clock or supply
predictable external I/O results for interpreted application code without
adding an application interface solely for previews.

`Engine.Bind` registers a host package at an import path, including a
standard-library path; `Engine.BoundOf` exposes an existing bound package.
Configure replacements before application initialization and execution,
and apply them to each fresh engine. If re-registering a whole package,
preserve its other required symbols. `ModeSource` bypasses host bindings,
so the target package's execution mode matters too.

Replacements affect interpreted calls through those bindings, not calls
inside compiled host implementations. This is an extension point of the
existing minigo API; the current `cmd/storybook` has no CLI configuration
for fake bindings. A custom runner can therefore provide a controlled
preview environment in addition to discovery and browser serving.

## Layout and checks

- `app/handler.go`: ordinary handlers, routes, and templates.
- `app/story_user.go`: zero-argument Stories and their fake store.
- `app/testdata/golden`: expected response bodies only.
- `app/handler_test.go`: native HTTP routing, status/header assertions, and bodies.
- `app/story_test.go`: native Story stdout checks and optional HTML export.
- `cmd/storybook/`: optional discovery, capture, export, and browser helper.
- `cmd/storybook/book_test.go`: direct minigo CLI and helper output checks.

```sh
make help
make test
```

Override `STORY`, `PKG`, `OUT`, or `PREVIEW_ADDR` as needed. `native-html`
is specifically the Go-only test of this example's `app`; `PKG` selects the
direct minigo and helper target. From the repository root use
`make -C examples/storybook <target>`. Stop `make preview` with Ctrl-C.

The tests independently compare native stdout, direct minigo CLI stdout,
and helper stdout with the same body goldens. They also verify source
reload, package-main entry execution without calling main, discovery,
export, and preview HTTP behavior. These are behavioral and regression
checks for the selected fixtures, not proof of visual equivalence in a
browser or usefulness on an existing application.

Design and evidence limits:
[docs/sketch/ja/experiment-storybook.md](../../docs/sketch/ja/experiment-storybook.md).
