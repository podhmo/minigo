// Package storybook discovers and renders "stories" — functions a
// net/http application keeps in build-tagged files — without building
// the application.
//
// A story is an exported func in a file the default build excludes but
// a tagged build (the book's [Book.Tag], conventionally "story")
// selects, in one of two shapes:
//
//	func StoryXxx()                                  // [KindPrint]: writes HTML to stdout
//	func StoryHttpXxx(w http.ResponseWriter, r *http.Request) // [KindHTTP]: handler-shaped
//
// [Discover] scans a package directory for those functions; [Book]
// adds the render loop a preview server needs: it keeps one configured
// base [*minigo.Engine] and forks a session ([Engine.NewSession]) per
// render, so a source, template, or //go:embed edit shows up on the
// next call while bound packages stay loaded — every render reads the
// current file contents; no restart is needed to pick up edits. Print
// stories render by capturing stdout; handler stories drive an
// [httptest.ResponseRecorder] so status code and headers are captured
// too.
//
// [Book.Watch] is the opt-in browser side of that loop: while it runs,
// file changes under the book's directory publish a reload to every
// open page embedding [Book.ScriptTag].
package storybook

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
)

// Book is one package's set of stories.
type Book struct {
	Dir string   // package directory
	Tag string   // build tag guarding the story files
	Src []string // packages to interpret from source

	// Setup, when non-nil, runs once on the lazily built base engine
	// before the first render — an application that needs host binds
	// (faking a driver import, binding a third-party package so it
	// stays compiled) registers them there, once, and every session
	// inherits them (Engine.Bind; rename the bound package via
	// BoundOf when the declared package name differs from the last
	// import-path element).
	Setup func(*minigo.Engine) error

	once    sync.Once
	base    *minigo.Engine // configured once; each Render forks a session
	dir     string         // absolute Dir, computed with base
	baseErr error

	reload *broadcaster // non-nil once Watch is running; nil means off
}

// Page is one rendered story: body plus, for handler-shaped stories, the
// recorded status code and response headers.
type Page struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// Render runs one story in a session forked from the book's base engine:
// the session keeps the configuration and host bindings but starts with an
// empty package cache, so an edit to the package — template, Go code, or an
// //go:embed'ed file — shows up on the next call while bound packages stay
// loaded. This is the reload guarantee: nothing but the once-built base
// engine is cached across calls. Stories own their fixtures; the helper injects no HTTP values.
// Print stories answer Status 200 with an empty Header; handler stories
// report the recorder's result.
func (b *Book) Render(ctx context.Context, name string) (*Page, error) {
	stories, err := b.Stories()
	if err != nil {
		return nil, err
	}
	kind := KindHTTP + 1
	for _, st := range stories {
		if st.Name == name {
			kind = st.Kind
			break
		}
	}
	if kind > KindHTTP {
		return nil, fmt.Errorf("%s: not a story", name)
	}
	base, dir, err := b.baseEngine()
	if err != nil {
		return nil, err
	}
	s := base.NewSession()
	var output bytes.Buffer
	minigo.WithOutput(&output)(s)
	pkg, err := s.Package(ctx, dir)
	if err != nil {
		return nil, err
	}
	if kind == KindHTTP {
		// Handler-shaped story: drive it through a recorder so the
		// response status and headers are captured, not just the body.
		// The request is a plain GET /; stories wanting a specific path
		// build their own with httptest.NewRequest inside.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, err := s.Call(ctx, pkg, name, rec, req); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		res := rec.Result()
		// the handler writes to the recorder, not to stdout: the page
		// body is the recorder's buffer, not the captured print output.
		return &Page{Status: res.StatusCode, Header: res.Header, Body: rec.Body.Bytes()}, nil
	}
	if _, err := s.Call(ctx, pkg, name); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &Page{Status: http.StatusOK, Body: output.Bytes()}, nil
}

// baseEngine lazily builds the long-lived engine all renders fork from.
// Build-time options (tags, package modes) live here; the per-request
// output buffer is installed on each session instead. A Book.Setup
// hook, when set, runs on the fresh engine inside the same Once, so
// renders never race a half-configured base. The handler can serve
// concurrent first requests, so the build is serialized: otherwise
// b.base could be observed before b.dir.
func (b *Book) baseEngine() (*minigo.Engine, string, error) {
	b.once.Do(func() {
		dir, err := filepath.Abs(b.Dir)
		if err != nil {
			b.baseErr = err
			return
		}
		opts := []minigo.Option{minigo.WithBuildConfig(resolve.BuildConfig{Tags: []string{b.Tag}})}
		if len(b.Src) > 0 {
			modes := map[string]minigo.PackageMode{}
			for _, p := range b.Src {
				modes[p] = minigo.ModeSource
			}
			opts = append(opts, minigo.WithPackageModes(modes))
		}
		b.base = minigo.NewEngine(dir, opts...)
		b.dir = dir
		if b.Setup != nil {
			b.baseErr = b.Setup(b.base)
		}
	})
	return b.base, b.dir, b.baseErr
}
