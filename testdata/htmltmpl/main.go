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

// MissingField executes a template naming a member the script struct
// does not declare: the projection is a real struct shape, so the miss
// reports gc's can't-evaluate rather than a map's <no value>.
func MissingField() string {
	var sb strings.Builder
	err := template.Must(template.New("m").Parse(`{{.Nope}}`)).Execute(&sb, user{Name: "x"})
	if err == nil {
		return "no error: " + sb.String()
	}
	return "error: " + err.Error()
}

// MissingMethod is the same miss a level deeper: evalField walks a
// method's result — a real string here — so the error names a type the
// projection did not fabricate.
func MissingMethod() string {
	var sb strings.Builder
	err := template.Must(template.New("m").Parse(`{{.Initial.Nope}}`)).Execute(&sb, user{Name: "x"})
	if err == nil {
		return "no error: " + sb.String()
	}
	return "error: " + err.Error()
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
