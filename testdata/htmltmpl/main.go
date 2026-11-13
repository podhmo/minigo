package main

import (
	"fmt"
	"html/template"
	"math"
	"net"
	"net/url"
	"strings"
)

type user struct {
	Name  string
	Badge template.HTML
}

func (u user) Initial() string { return strings.ToUpper(u.Name[:1]) }

var tmpl = template.Must(template.New("u").Parse(`<p title="{{.Name}}">{{.Initial}} {{.Name}} {{.Badge}}</p>`))

// Render executes the bound html/template on a script struct: fields
// escape by context, a script method is callable, template.HTML is not
// escaped.
func Render() string {
	var sb strings.Builder
	if err := tmpl.Execute(&sb, user{Name: "<bob>", Badge: template.HTML("<b>x</b>")}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String() + "|" + template.HTMLEscapeString("a&b")
}

// ParseError recovers Must's panic on a bad template.
func ParseError() (msg string) {
	defer func() { msg = recover().(error).Error() }()
	template.Must(template.New("bad").Parse("{{"))
	return ""
}

// HostQuery indexes a host url.Values handed in by the embedder.
func HostQuery(q url.Values) string {
	tags, ok := q["tag"]
	_, missing := q["nope"]
	return strings.Join(tags, ",") + "|" + q["name"][0] + "|" + boolStr(ok) + boolStr(missing)
}

// HostSliceOOB indexes a boxed host named slice past its end with a
// uint64 index: a recoverable bounds panic, as for script slices.
func HostSliceOOB(ip net.IP) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	var i uint64 = math.MaxUint64
	_ = ip[i]
	return ""
}

func boolStr(b bool) string {
	if b {
		return "T"
	}
	return "F"
}

func main() {}
