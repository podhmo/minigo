package main

// Regression for #1399: a FuncMap callback's post-call projection sync
// re-projects every registered container; when the re-projected struct
// carries a template-callable method (here, promoted through the
// embedded Moderation), each eager method call's own host calls
// re-entered the merge machinery and the re-projections kept
// registering fresh entries — the merge set and the eager calls
// compounded without bound and the render never returned. The fix
// binds callable members lazily inside a merge and stops merge-time
// registrations from growing the set, so the render completes and
// each member still reads its computed value.

import (
	"fmt"
	"os"
	"text/template"
	"time"
)

type Moderation struct {
	EditedAt time.Time
	Hidden   bool
	Reason   string
}

func (m Moderation) Edited() bool { return !m.EditedAt.IsZero() }

type Reply struct {
	ID   int64
	Body string
	Moderation
}

type Thread struct {
	ID       int64
	IsAnswer bool
	Body     string
	Replies  []*Reply
	Moderation
}

var shared map[string]any

func dict(pairs ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i].(string)] = pairs[i+1]
	}
	return m
}

func bump() int {
	shared["n"] = shared["n"].(int) + 1
	return shared["n"].(int)
}

func main() {
	var ts []*Thread
	for i := int64(1); i <= 40; i++ {
		var rs []*Reply
		for j := int64(1); j <= 3; j++ {
			rs = append(rs, &Reply{ID: j, Body: "r"})
		}
		ts = append(ts, &Thread{ID: i, Body: "t", Replies: rs,
			Moderation: Moderation{EditedAt: time.Unix(i, 0)}})
	}
	ts[1].IsAnswer = true
	ts[2].Hidden = true
	shared = dict("Threads", ts, "Replies", ts[0].Replies, "n", 0)
	t := template.Must(template.New("t").Funcs(template.FuncMap{"bump": bump, "dict": dict}).Parse(
		`{{range $t := .Threads}}{{bump}}{{template "row" (dict "C" $t)}}{{end}}{{define "row"}}{{if .C.Hidden}}H{{else}}-{{end}}{{if .C.Edited}}E{{else}}-{{end}}{{if .C.IsAnswer}}A{{else}}-{{end}};{{end}}`))
	if err := t.Execute(os.Stdout, shared); err != nil {
		fmt.Println("ERR:", err)
	}
}
