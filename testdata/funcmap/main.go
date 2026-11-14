// Package main registers script funcs with text/template's and
// html/template's FuncMap.
package main

import (
	"fmt"
	htmpl "html/template"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
)

type user struct{ Name string }

func shout(s string) string { return strings.ToUpper(s) + "!" }

// Text registers a bound host func, a declared script func, a variadic
// closure and an int-typed closure with text/template.
func Text() string {
	var sb strings.Builder
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"up":    strings.ToUpper,
		"shout": shout,
		"join":  func(sep string, xs ...string) string { return strings.Join(xs, sep) },
		"add":   func(a, b int) int { return a + b },
	}).Parse(`{{up .Name}} {{shout .Name}} {{join "-" "a" "b"}} {{add 1 2}}`))
	if err := t.Execute(&sb, user{Name: "bob"}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// Fail returns a script func's error through the template.
func Fail() string {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"fail": func() (string, error) { return "", fmt.Errorf("boom") },
	}).Parse(`{{fail}}`))
	var sb strings.Builder
	err := t.Execute(&sb, nil)
	return fmt.Sprint(err)
}

// HTML returns template.HTML from a script func under html/template.
func HTML() string {
	var sb strings.Builder
	t := htmpl.Must(htmpl.New("h").Funcs(htmpl.FuncMap{
		"badge": func(s string) htmpl.HTML { return htmpl.HTML("<b>" + htmpl.HTMLEscapeString(s) + "</b>") },
		"lower": strings.ToLower,
	}).Parse(`<p>{{badge .Name}} {{lower .Name}}</p>`))
	if err := t.Execute(&sb, user{Name: "<Bob>"}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

type item struct {
	Tags []string
	Meta map[string]string
}

// NilFields passes nil slice and map fields of a script struct to funcs
// typed for them.
func NilFields() string {
	var sb strings.Builder
	t := template.Must(template.New("n").Funcs(template.FuncMap{
		"join": strings.Join,
		"size": func(m map[string]string) int { return len(m) },
	}).Parse(`[{{join .Tags ","}}] {{size .Meta}}`))
	if err := t.Execute(&sb, item{}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// Builtins registers intrinsics that have no host func of their own,
// passes an html FuncMap where a text one is expected, and hands the
// template a nil slice through `any`.
func Builtins() string {
	var fm template.FuncMap = htmpl.FuncMap{
		"split":  strings.Split,
		"fields": strings.Fields,
		"atoi":   strconv.Atoi,
	}
	var sb strings.Builder
	t := template.Must(template.New("b").Funcs(fm).Parse(`{{range split "a,b" ","}}[{{.}}]{{end}}{{len (fields "x y z")}} {{atoi "42"}} {{len .a}}`))
	var none []string
	if err := t.Execute(&sb, map[string]any{"a": none}); err != nil {
		return "error: " + err.Error()
	}
	err := template.Must(template.New("e").Funcs(fm).Parse(`{{atoi "x"}}`)).Execute(&sb, nil)
	return sb.String() + fmt.Sprint(" | ", err)
}

type pt struct{ X, Y int }
type pts []pt

type page struct {
	Users []pt
	Ptrs  []*pt
	Named pts
	Arr   [2]pt
	Grid  [][]pt
	ByKey map[string][]pt
	Me    *user
	Num   int
}

func sumPts(l []pt) int {
	n := 0
	for _, p := range l {
		n += p.X + p.Y
	}
	return n
}

func newPage() page {
	return page{
		Users: []pt{{1, 2}, {3, 4}},
		Ptrs:  []*pt{{1, 2}, {5, 6}},
		Named: pts{{7, 8}},
		Arr:   [2]pt{{1, 1}, {2, 2}},
		Grid:  [][]pt{{{1, 2}}, {{3, 4}, {5, 6}}},
		ByKey: map[string][]pt{"a": {{1, 2}}, "b": {{3, 4}, {5, 6}}},
		Me:    &user{Name: "me"},
		Num:   3,
	}
}

// TypedParams passes script slices, arrays, maps and pointers of script
// structs to funcs that declare them, and checks a func writing through
// a slice parameter reaches the template's data.
func TypedParams() string {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"sum":   sumPts,
		"psum":  func(l []*pt) int { return len(l) + l[1].X },
		"nsum":  func(l pts) int { return sumPts(l) },
		"asum":  func(a [2]pt) int { return a[0].X + a[1].X },
		"grid":  func(g [][]pt) int { return len(g[1]) + sumPts(g[0]) },
		"keys":  func(m map[string][]pt) int { return len(m) + sumPts(m["b"]) },
		"many":  func(sep string, ls ...[]pt) int { return len(ls) + sumPts(ls[1]) },
		"bump":  func(l []pt) int { l[0].X = 100; return 0 },
		"first": func(l []pt) int { return l[0].X },
		"name":  func(u *user) string { return u.Name },
		"val":   func(u user) string { return u.Name },
	}).Parse(`{{sum .Users}} {{psum .Ptrs}} {{nsum .Named}} {{asum .Arr}} {{grid .Grid}} {{keys .ByKey}} {{many "-" .Users .Users}} {{name .Me}} {{val .Me}} {{bump .Users}}{{first .Users}}`))
	var sb strings.Builder
	if err := t.Execute(&sb, newPage()); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// WrongTypes feeds funcs arguments of the wrong type; the engines word
// the mismatch alike, down to the arguments' type names.
func WrongTypes() string {
	byPtr := template.FuncMap{"f": func(u *user) string { return u.Name }}
	byVal := template.FuncMap{"f": func(p pt) int { return p.X }}
	var out []string
	for _, c := range []struct {
		tpl string
		fm  template.FuncMap
	}{
		{`{{f .N}}`, byPtr},
		{`{{f .U}}`, byPtr},
		{`{{f .U}}`, byVal},
		{`{{f .N}}`, byVal},
	} {
		var sb strings.Builder
		err := template.Must(template.New("t").Funcs(c.fm).Parse(c.tpl)).Execute(&sb, map[string]any{"N": 3, "U": user{Name: "u"}})
		msg := fmt.Sprint(err)
		if i := strings.Index(msg, "wrong type for value"); i >= 0 {
			msg = msg[i:]
		}
		out = append(out, msg)
	}
	return strings.Join(out, "\n")
}

// NilElems hands a page whose slices and maps of script structs are nil
// to the template and to funcs declaring them.
func NilElems() string {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"sum":     sumPts,
		"isnil":   func(l []pt) bool { return l == nil },
		"isnilm":  func(m map[string][]pt) bool { return m == nil },
		"isnilp":  func(l []*pt) bool { return l == nil },
		"isnilpp": func(u *user) bool { return u == nil },
	}).Parse(`{{len .Users}} {{.Users}} {{len .ByKey}} {{.ByKey}} {{sum .Users}} {{isnil .Users}} {{isnilm .ByKey}} {{isnilp .Ptrs}} {{isnilpp .Me}} {{range .Users}}x{{else}}empty{{end}} {{if .Users}}y{{else}}n{{end}}`))
	var sb strings.Builder
	if err := t.Execute(&sb, page{}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

type codeErr struct{ Code int }

func (e codeErr) Error() string { return fmt.Sprintf("code %d", e.Code) }

type tag string

func (t tag) String() string { return "tag:" + string(t) }

// Panics runs funcs that panic with each kind of payload; the template
// reports the payload, as an error or printed with %v.
func Panics() string {
	var out []string
	for _, c := range []struct {
		name string
		fn   func() string
	}{
		{"str", func() string { panic("boom") }},
		{"err", func() string { panic(fmt.Errorf("eboom")) }},
		{"int", func() string { panic(42) }},
		{"custom", func() string { panic(codeErr{7}) }},
		{"stringer", func() string { panic(tag("x")) }},
		{"nilmap", func() string { var m map[string]int; m["a"] = 1; return "" }},
		{"index", func() string { var s []int; return fmt.Sprint(s[3]) }},
	} {
		var sb strings.Builder
		err := template.Must(template.New(c.name).Funcs(template.FuncMap{"p": c.fn}).Parse(`{{p}}`)).Execute(&sb, nil)
		out = append(out, fmt.Sprint(err))
	}
	return strings.Join(out, "\n")
}

func funcsPanic(fm template.FuncMap) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	template.New("t").Funcs(fm)
	return "no panic"
}

// NotFunc registers values Template.Funcs rejects with a panic; the
// script recovers it and goes on.
func NotFunc() string {
	var out []string
	for _, fm := range []template.FuncMap{
		{"bad": 42},
		{"bad": nil},
		{"bad": "s"},
		{"1bad": strings.ToUpper},
		{"f": func() (int, int, int) { return 1, 2, 3 }},
	} {
		out = append(out, funcsPanic(fm))
	}
	var sb strings.Builder
	template.Must(template.New("ok").Funcs(template.FuncMap{"up": strings.ToUpper}).Parse(`{{up "x"}}`)).Execute(&sb, nil)
	return strings.Join(out, "; ") + "; " + sb.String()
}

type boom struct{ N int }

func recovered(f func()) (r any) {
	defer func() { r = recover() }()
	f()
	return nil
}

// CallbackPanics checks a panic in the other host callbacks still
// reaches the script's recover with its payload intact, before and after
// a template swallowed a func's panic.
func CallbackPanics() string {
	var out []string
	check := func() {
		r := recovered(func() { sort.Slice([]int{3, 1, 2}, func(i, j int) bool { panic(boom{1}) }) })
		b, ok := r.(boom)
		out = append(out, fmt.Sprint("sort ", b.N, ok))
		var once sync.Once
		out = append(out, fmt.Sprint("once ", recovered(func() { once.Do(func() { panic("once") }) })))
		r = recovered(func() { strings.Map(func(r rune) rune { panic(fmt.Errorf("map %c", r)) }, "a") })
		err, _ := r.(error)
		out = append(out, fmt.Sprint("map ", err))
		out = append(out, fmt.Sprint("handler ", recovered(func() {
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("handler") }).
				ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		})))
	}
	check()
	var sb strings.Builder
	err := template.Must(template.New("t").Funcs(template.FuncMap{"p": func() string { panic("tmpl") }}).Parse(`{{p}}`)).Execute(&sb, nil)
	out = append(out, fmt.Sprint(err))
	check()
	return strings.Join(out, "\n")
}

// Goroutine executes templates on goroutines other than the one that
// registered their funcs: arguments convert there too, and a func's
// panic is the template's error, not the process's.
func Goroutine() string {
	fm := template.FuncMap{
		"sum": sumPts,
		"p":   func() string { panic("gp") },
		"f":   func(u *user) string { return u.Name },
	}
	var wg sync.WaitGroup
	res := make([]string, 3)
	for i, tpl := range []string{`{{sum .Users}}`, `{{p}}`, `{{f .Me}}{{f .Num}}`} {
		t := template.Must(template.New(fmt.Sprint("g", i)).Funcs(fm).Parse(tpl))
		wg.Add(1)
		go func() {
			defer wg.Done()
			var sb strings.Builder
			err := t.Execute(&sb, map[string]any{"Users": []pt{{1, 2}, {3, 4}}, "Me": &user{Name: "me"}, "Num": 3})
			msg := fmt.Sprint(err)
			if j := strings.Index(msg, "wrong type for value"); j >= 0 {
				msg = msg[j:]
			}
			res[i] = sb.String() + "/" + msg
		}()
	}
	wg.Wait()
	return strings.Join(res, "\n")
}

type hidden struct{ n int }

// HiddenFields calls a func with two distinct values of a struct type
// that declares no exported members: both project to the same shape,
// so a projection registry keyed on the host pointer must keep their
// originals apart — a zero-size *struct{} shares one address and
// would fold every arg to the newest value.
func HiddenFields() string {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"get": func(h *hidden) int { return h.n },
	}).Parse(`{{get .A}} {{get .B}}`))
	var sb strings.Builder
	if err := t.Execute(&sb, map[string]any{"A": &hidden{n: 1}, "B": &hidden{n: 2}}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// HTMLTyped registers a func with a script-typed parameter under
// html/template.
func HTMLTyped() string {
	t := htmpl.Must(htmpl.New("h").Funcs(htmpl.FuncMap{
		"list": func(l []pt) htmpl.HTML {
			var sb strings.Builder
			for _, p := range l {
				sb.WriteString(fmt.Sprintf("<li>%d,%d</li>", p.X, p.Y))
			}
			return htmpl.HTML(sb.String())
		},
	}).Parse(`<ul>{{list .Users}}</ul>`))
	var sb strings.Builder
	if err := t.Execute(&sb, newPage()); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

func main() {
	fmt.Println(Builtins())
	fmt.Println(Text())
	fmt.Println(Fail())
	fmt.Println(HTML())
	fmt.Println(NilFields())
	fmt.Println(TypedParams())
	fmt.Println(WrongTypes())
	fmt.Println(NilElems())
	fmt.Println(Panics())
	fmt.Println(NotFunc())
	fmt.Println(CallbackPanics())
	fmt.Println(Goroutine())
	fmt.Println(HTMLTyped())
}
