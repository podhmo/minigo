package storybook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/x/storybook"
)

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, src)
	}
	return dir
}

// storyPackage writes the fixture most tests need — go.mod, an
// always-built app.go, and storySrc wrapped in the //go:build story
// header — plus any extra or overriding files.
func storyPackage(t *testing.T, storySrc string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module x\n\ngo 1.26\n",
		"app.go": "package app\n",
	}
	if storySrc != "" {
		files["story.go"] = "//go:build story\n\npackage app\n\n" + storySrc + "\n"
	}
	for name, src := range extra {
		files[name] = src
	}
	return writePackage(t, files)
}

func TestDiscover(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"go.mod": "module x\n\ngo 1.26\n",
		"app.go": "package app\n",
		"story.go": `//go:build story

package app

import "net/http"

// StoryB renders b.
func StoryB() { RenderB() }

// StoryA renders a.
func StoryA() { RenderA() }

// StoryHttpC drives the handler.
func StoryHttpC(w http.ResponseWriter, r *http.Request) { serve() }

func RenderA()  {}
func RenderB()  {}
func serve()    {}
func notStory() {}
`,
		// an untagged file is not scanned — its Story funcs do not
		// exist for discovery.
		"other.go": "package app\n\nfunc StoryOther() {}\n",
		// test files are never scanned.
		"app_test.go": "package app\n\nfunc StoryInTest() {}\n",
	})
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	// sorted by name; only the tagged file's exported Story funcs.
	names := make([]string, 0, len(stories))
	for _, s := range stories {
		names = append(names, s.Name)
	}
	if diff := cmp.Diff([]string{"StoryA", "StoryB", "StoryHttpC"}, names); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
	for _, s := range stories {
		if s.Doc == "" || !strings.HasPrefix(s.Pos, filepath.Join(dir, "story.go")) {
			t.Errorf("missing doc or position: %+v", s)
		}
	}
	byName := map[string]storybook.Story{}
	for _, s := range stories {
		byName[s.Name] = s
	}
	if byName["StoryA"].IsHTTP() || byName["StoryB"].IsHTTP() || !byName["StoryHttpC"].IsHTTP() {
		t.Errorf("kinds: %+v", stories)
	}
	if diff := cmp.Diff(map[string]string{
		"StoryA":     "RenderA",
		"StoryB":     "RenderB",
		"StoryHttpC": "",
	}, map[string]string{
		"StoryA":     byName["StoryA"].Group,
		"StoryB":     byName["StoryB"].Group,
		"StoryHttpC": byName["StoryHttpC"].Group,
	}); diff != "" {
		t.Errorf("groups (-want +got):\n%s", diff)
	}
}

// A story-prefixed func whose signature fits neither shape fails
// discovery instead of being silently skipped — a typo'd signature
// must not drop the story.

func TestRenderGeneratedFixture(t *testing.T) {
	dir := storyPackage(t, `import (
	"fmt"
	"html/template"
	"github.com/podhmo/minigo/x/storybook/fixture"
)

type Inner struct{ Enabled bool }
type Label string
type Pair [2]string
type Pair2 Pair
type Base struct{ Text string }
type Defined Base
type PtrMap[K comparable, V any] map[K]V
type DerivedMap PtrMap[*[]int, int]
type MidMap[K comparable, V any] PtrMap[K, V]
type FinalMap MidMap[string, int]
type View struct {
	Name Label
	Count int
	Tags []string
	Inner *Inner
	Scores map[string]int
	Badge template.HTML
	Pair Pair
	Defined Defined
	PtrScores map[*Inner]int
	SlicePtrScores map[*[]int]int
	Pair2 Pair2
	DM DerivedMap
	FM FinalMap
	Next *View
}

func StoryGenerated() {
	v := fixture.Minimal[View]()
	fmt.Printf("%s/%d/%d/%t/%t/%d/%d/%s/%s/%t/%s/%d/%d/%s/%d/%s/%d", v.Name, v.Count, len(v.Tags), v.Tags[0] != "", v.Inner.Enabled, len(v.Scores), v.Scores["x"], v.Badge, v.Pair[1], v.Next == nil, v.Defined.Text, len(v.PtrScores), len(v.SlicePtrScores), v.Pair2[1], len(v.DM), copyProbe(v.Pair2), v.FM["x"])
}

func copyProbe(p Pair2) string { q := p; q[0] = "changed"; return p[0] }

func StoryGeneratedLong() {
	v := fixture.LongText[View]()
	fmt.Print(len(v.Name) > 40)
}
`, nil)
	b := &storybook.Book{Dir: dir, Tag: "story"}
	page, err := b.Render(t.Context(), "StoryGenerated")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("x/1/1/true/true/1/1/x/x/true/x/1/1/x/1/x/1", string(page.Body)); diff != "" {
		t.Fatalf("generated fixture output (-want +got):\n%s", diff)
	}
	page, err = b.Render(t.Context(), "StoryGeneratedLong")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("true", string(page.Body)); diff != "" {
		t.Fatalf("generated long-text output (-want +got):\n%s", diff)
	}
}

func TestRenderGeneratedFixtureDerivedShapes(t *testing.T) {
	dir := storyPackage(t, `import (
	"fmt"
	"github.com/podhmo/minigo/x/storybook/fixture"
)

type Strings []string
type Strings2 Strings
type Strings3 Strings2
type IntPtr *int
type IntPtr2 IntPtr
type IntPtr3 IntPtr2
type Pair [2]string
type Pair2 Pair
type N1 struct{ S string }
type N2 struct{ F N1 }
type N3 struct{ F N2 }
type N4 struct{ F N3 }
type N5 struct{ F N4 }
type N6 struct{ F N5 }
type N7 struct{ F N6 }
type Tree struct {
	Children []Tree
	ByName   map[string]Tree
}

func StoryDerivedShapes() {
	ss := fixture.Minimal[Strings3]()
	ip := fixture.Minimal[IntPtr3]()
	var p Pair2 = fixture.Minimal[Pair2]()
	q := p
	q[0] = "changed"
	var a [2]string = p
	var asany any = a
	_, asserts := asany.([2]string)
	a[0] = "changed"
	lit := Pair2{"x", "x"}
	ai := any(lit)
	lit[0] = "changed"
	eq := any(p) == ai
	_, backok := ai.(Pair2)
	plain := Pair2{"x", "x"}
	var unnamed [2]string = plain
	var uany any = unnamed
	_, uok := uany.([2]string)
	c := fixture.Minimal[complex128]()
	deep := fixture.Minimal[N7]()
	tree := fixture.Minimal[Tree]()
	fmt.Printf("%s/%d/%s/%t/%t/%t/%t/%t/%t/%d/%d", ss[0], *ip, p[0], real(c) != 0, deep.F.F.F.F.F.F.S != "", asserts, eq, backok, uok, len(tree.Children), len(tree.ByName))
}
`, nil)
	b := &storybook.Book{Dir: dir, Tag: "story"}
	page, err := b.Render(t.Context(), "StoryDerivedShapes")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("x/1/x/true/true/true/true/true/true/1/1", string(page.Body)); diff != "" {
		t.Fatalf("derived fixture output (-want +got):\n%s", diff)
	}
}

func TestRenderGeneratedFixtureCrossPackageArrayLen(t *testing.T) {
	dir := storyPackage(t, `import (
	"fmt"
	"x/model"
	"github.com/podhmo/minigo/x/storybook/fixture"
)

const N = 1

type Pair model.Pair

func StoryCrossPackageArrayLen() {
	p := fixture.Minimal[Pair]()
	fmt.Printf("%d/%s", len(p), p[2])
}
`, map[string]string{
		"model/model.go": `package model

import "x/sizes"

type Pair [sizes.N]string
`,
		"sizes/sizes.go": `package sizes

import "fmt"

const F float32 = 2.999999999999999999

const N = int(F)

func init() { fmt.Println("sizes init") }
`,
	})
	b := &storybook.Book{Dir: dir, Tag: "story"}
	page, err := b.Render(t.Context(), "StoryCrossPackageArrayLen")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("3/x", string(page.Body)); diff != "" {
		t.Fatalf("cross-package array fixture output (-want +got):\n%s", diff)
	}
}

func TestDiscoverRejectsBadShape(t *testing.T) {
	for _, tc := range []struct {
		decl, want string
	}{
		{"func StoryX(w http.ResponseWriter, r *http.Request) {}", "StoryX must be func()"},
		{"func StoryX() string { return \"\" }", "StoryX must be func()"},
		{"func StoryX[T any]() {}", "StoryX must be func()"},
		{"func StoryHttpX() {}", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
		{"func StoryHttpX(w, extra http.ResponseWriter, r *http.Request) {}", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
		{"func StoryHttpX(w http.ResponseWriter, r *http.Request) error { return nil }", "StoryHttpX must be func(http.ResponseWriter, *http.Request)"},
	} {
		t.Run(tc.decl, func(t *testing.T) {
			dir := storyPackage(t, "import \"net/http\"\n\n"+tc.decl, nil)
			_, err := storybook.Discover(dir, "story")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want invalid signature error %q, got %v", tc.want, err)
			}
		})
	}
}

// Only print stories group. A handler story calling an unqualified
// helper must not end up pointing an index at a grid that cannot
// frame it.
func TestHTTPStoryStaysUngrouped(t *testing.T) {
	dir := storyPackage(t, `import "net/http"

func StoryHttpX(w http.ResponseWriter, r *http.Request) { helper() }
func helper() {}
`, nil)
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("", stories[0].Group); diff != "" {
		t.Errorf("http story group (-want +got):\n%s", diff)
	}
}

// A //storybook:group doc marker overrides the last-call heuristic, so
// a story that must end on a helper can still name its render function.
func TestDiscoverGroupMarker(t *testing.T) {
	dir := storyPackage(t, `// StoryMarked renders a but must end on a helper.
//
//storybook:group RenderA
func StoryMarked() { RenderA(); helper() }

// StoryPlain relies on the heuristic.
func StoryPlain() { RenderB() }

func RenderA() {}
func RenderB() {}
func helper()  {}
`, nil)
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]storybook.Story{}
	for _, s := range stories {
		byName[s.Name] = s
	}
	if diff := cmp.Diff("RenderA", byName["StoryMarked"].Group); diff != "" {
		t.Errorf("marked group (-want +got):\n%s", diff)
	}
	if byName["StoryMarked"].GroupInferred {
		t.Error("marked group must not report GroupInferred")
	}
	if diff := cmp.Diff("RenderB", byName["StoryPlain"].Group); diff != "" {
		t.Errorf("heuristic keeps the last call (-want +got):\n%s", diff)
	}
	// The heuristic result is flagged so consumers can distinguish a
	// declared group from a stolen one.
	if !byName["StoryPlain"].GroupInferred {
		t.Error("heuristic group should report GroupInferred")
	}
}

// A marker on a handler story has no grid to group into — an explicit
// marker must not sit there silently doing nothing.
func TestDiscoverGroupMarkerOnHTTPStory(t *testing.T) {
	dir := storyPackage(t, `import "net/http"

//storybook:group RenderA
func StoryHttpX(w http.ResponseWriter, r *http.Request) {}
`, nil)
	_, err := storybook.Discover(dir, "story")
	if err == nil || !strings.Contains(err.Error(), "storybook:group") {
		t.Fatalf("want marker-on-handler error, got %v", err)
	}
}

// Files the plain build already selects are not story files, even when
// they name StoryXxx — discovery only counts what the tag brings in,
// and the skipped funcs are reported so a missing //go:build line
// surfaces instead of failing silently.
func TestExcluded(t *testing.T) {
	dir := storyPackage(t, "func StoryTagged() {}", map[string]string{
		"app.go":  "package app\n\nfunc StoryAlways() {}\n",
		"unix.go": "//go:build unix\n\npackage app\n\nfunc StoryUnix() {}\n",
	})
	excluded, err := storybook.Excluded(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range excluded {
		names = append(names, s.Name)
	}
	if diff := cmp.Diff([]string{"StoryAlways", "StoryUnix"}, names); diff != "" {
		t.Errorf("excluded (-want +got):\n%s", diff)
	}
	stories, err := storybook.Discover(dir, "story")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"StoryTagged"}, []string{stories[0].Name}); diff != "" {
		t.Errorf("stories (-want +got):\n%s", diff)
	}
}

// A package with no story funcs is an error, not an empty list —
// preview servers tolerate it explicitly, silent emptiness does not.
func TestDiscoverNoStories(t *testing.T) {
	dir := storyPackage(t, "", nil)
	_, err := storybook.Discover(dir, "story")
	if err == nil || !strings.Contains(err.Error(), "no Story funcs") {
		t.Fatalf("want no-stories error, got %v", err)
	}
}
