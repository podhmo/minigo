# plan-storybook.md — previewing `net/http` views by calling build-tagged stories in the interpreter

Companion to `docs/sketch/plan-task-runner.md`,
`docs/sketch/plan-gen-sync.md` and `docs/sketch/plan-minigo-generate.md`.
This note records what `examples/storybook` is for and where it sits
among the examples. The experiment log — what broke, what was added,
and the comparison with plain Go — is
`docs/sketch/ja/experiment-storybook.md` (Japanese). The example is an
experiment and may not be merged.

## Motivation

A usage of minigo that the other examples do not cover: **run the
application's own code, not a tool written for minigo**. The scripts of
`task-run`, `gen-sync`, `extract-schema` and `minigo-generate` are
written to be interpreted; here the interpreted package is an ordinary
`net/http` application, and minigo is only asked to call one of its
functions with values the host supplies.

The question is whether minigo's two properties are enough to build a
Storybook-like preview:

- **any function is an entrypoint** — `Engine.Call(ctx, pkg, name,
  args...)` calls a top-level func by name, so there is no `main`, no
  registry and no generated dispatch code to keep in sync with the
  stories
- **minigo is a VM interpreter, not a build** — it never compiles a
  package to machine code and never links a binary. Packages are parsed
  and indexed on demand, and a function is compiled to bytecode for the
  stack VM only when it is first called. A preview pays for the code the
  story reaches, not for the package or its dependency graph

A story is a plain Go func in a file behind `//go:build story`:

```go
//go:build story

package app

func StoryUser(w http.ResponseWriter, r *http.Request) {
	RenderUser(w, &User{ID: "1", Name: "Alice"})
}
```

The tag keeps stories out of the real binary (`go build` drops the file)
while `go vet`/gopls still check them with `-tags story`. Dummy values —
fake stores, hostile names, query-driven controls — live next to the
code they exercise.

## Where it sits among the examples

| | `task-run` | `minigo-generate` | `extract-schema` | `storybook` |
|---|---|---|---|---|
| What the script *is* | a trusted build file | tool packages run by directive | the package walker itself | the user's application package plus its build-tagged stories |
| Written for minigo? | yes (imports a stub `task` package) | yes (`func Main([]string) int`) | yes (calls `inspect`) | no — ordinary Go; stories are ordinary Go |
| Entrypoint selection | exported task funcs, by name | `Main` of the named package | `Main` | exported `StoryXxx` funcs, by name, from tagged files only |
| Engine feature exercised | intrinsic-bound stub package, `os`/`exec`, virtual cwd | `Engine.Call` reuse + `syntax` comment access | `inspect` API | `Engine.Call` with host Go values as arguments, `WithBuildConfig` tags, host-bound `html/template`, boxed host map/slice indexing |
| Value boundary | strings in, error out | `[]string` in, `int` out | flag values in, JSON string out | host `*httptest.ResponseRecorder` / `*http.Request` in; the script writes through host interfaces |
| Data direction | reads parts of a file → executes them | reads comments → dispatches | reads the index → emits a schema | calls view code → captures status, headers, body → HTML preview |
| Trust model | unrestricted | unrestricted | unrestricted | unrestricted (application `init` and dependencies run) |

The closest relative is `task-run`: both pick named functions out of a
package and run only those. The difference is that task-run's Taskfile
exists *for* minigo, while storybook's target exists for `go build` and
is merely borrowed. That makes it the first example whose ceiling is
interpreter fidelity on unmodified code — the same axis the `realworld`
harness measures for scanning tools, applied to code that is *executed*
rather than inspected.

Compared with `convert-define`, which also uses a build tag
(`//go:build codegen`) to keep a minigo-only file out of the real build:
convert-define never evaluates the tagged file's body as Go (its calls
are quoted special forms), whereas storybook evaluates the tagged file
as ordinary Go together with the untagged files of the same package.

## Shape

- `book.go` — `Book.Stories` finds `Story*` funcs with
  `go/build.Context.MatchFile` + `go/parser` (no load, no typecheck) and
  rejects a `Story`-prefixed func of the wrong shape. `Book.Render`
  checks the name against that list, builds a fresh engine with the tag,
  and calls the story with a host recorder and request
  (`&runtime.GoValue{V: ...}`).
- `page.go` — static export (`-o`) and the `-http` server. Every request
  builds a new engine, so an edited source shows up on reload without a
  restart; the query string is forwarded as a story control.
- `testdata/app` — the fixture application and five stories. Each story
  is rendered by minigo and, under `-tags story`, natively; both are
  compared against the same goldens with `cmp.Diff`.

## Limitations hit along the way

Fixed in the spike: `html/template` trapped at init
(`undefined: template.FuncMap`) until a host binding was added; reads of
boxed host maps/slices (`r.URL.Query()["tag"]`) trapped until
`VM.hostIndex`; `minigo run` could not select tagged files until
`--tags`.

Still open (TODO.md): `template.FuncMap` in either template package;
assignment to and `range` over boxed host maps (`w.Header()[k] = v`,
`range r.URL.Query()`); element writes through copied host slices. Real
view code reaches the first two almost immediately, so they bound how
far the preview carries past the fixture.

## Verdict

The mechanism works on the fixture, with byte-identical output to native
Go. Against doing the same in plain Go (`go test -tags story`, or a
generated registry `main` under `go run -tags story`), the measured edit
loop on a small package is 0.3–0.6 s for Go versus ~0.07 s for minigo —
too small a gap to matter. The structural differences matter more: no
registry to generate, handlers in `package main` are callable, a package
with unrelated broken code still previews, and the server never
restarts. Until the open fidelity gaps are closed and the loop is
re-measured on a dependency-heavy application, the example is a
behavior check of minigo rather than evidence that it beats Go for this
use. Details and numbers: `docs/sketch/ja/experiment-storybook.md`
section 7.
