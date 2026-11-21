package main

import (
	"fmt"
	"os"
	"sync"
	"text/template"
	"time"
)

type Moderation struct {
	Hidden       bool
	HiddenReason string
}

func (m Moderation) Label() string   { return "LBL" }
func (m *Moderation) PLabel() string { return "PLBL" }

type Inner struct{ Deep string }

type Comment struct {
	ID   int64
	Body string
	Moderation
}

type Base struct {
	Inner
	Moderation
	Name string
}

type PtrEmbed struct {
	*Moderation
	Tag string
}

type Dup struct {
	Moderation
	M2
}

type M2 struct {
	Hidden       bool
	HiddenReason string
}

type HostEmbed struct {
	sync.Mutex
	N int
}

type HostTime struct {
	time.Time
	N int
}

type HostTpl struct {
	template.Template
	N int
}

type Alias = Moderation
type Defined Moderation

type ViaDefined struct {
	Defined
	X int
}

func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i].(string)] = pairs[i+1]
	}
	return m
}

func run(src string, data any) {
	t := template.Must(template.New("t").Parse(src))
	if err := t.Execute(os.Stdout, data); err != nil {
		fmt.Printf("%s => ERROR: %v\n", src, err)
		return
	}
	fmt.Println()
}

func main() {
	c := &Comment{ID: 1, Body: "hi", Moderation: Moderation{Hidden: true, HiddenReason: "spam"}}
	run("direct: {{.C.ID}} {{.C.Body}}", dict("C", c))
	run("promoted: {{.C.Hidden}} {{.C.HiddenReason}}", dict("C", c))
	run("explicit: {{.C.Moderation.Hidden}} {{.C.Moderation.HiddenReason}}", dict("C", c))

	b := &Base{Inner: Inner{Deep: "deep"}, Moderation: Moderation{Hidden: true, HiddenReason: "spam"}, Name: "b"}
	run("nested: {{.B.Deep}} {{.B.Hidden}} {{.B.Name}}", dict("B", b))
	run("pmethod: {{.B.Label}}", dict("B", b))
	run("ppmethod: {{.B.PLabel}}", dict("B", b))

	v := Base{Inner: Inner{Deep: "deep"}, Moderation: Moderation{Hidden: true, HiddenReason: "spam"}, Name: "v"}
	run("valp: {{.B.PLabel}} {{.B.Label}}", dict("B", v))

	p := &PtrEmbed{Moderation: &Moderation{Hidden: true, HiddenReason: "ps"}, Tag: "t"}
	run("ptremb: {{.P.Hidden}} {{.P.PLabel}} {{.P.Tag}}", dict("P", p))

	d := &Dup{Moderation: Moderation{Hidden: true, HiddenReason: "a"}, M2: M2{Hidden: false, HiddenReason: "b"}}
	run("ambig: {{.D.Hidden}} {{.D.Moderation.Hidden}}", dict("D", d))

	he := &HostEmbed{N: 5}
	run("hostemb: {{.H.N}}", dict("H", he))

	vd := &ViaDefined{Defined: Defined{Hidden: true, HiddenReason: "vd"}, X: 1}
	run("defined: {{.V.Hidden}} {{.V.X}}", dict("V", vd))

	ht := &HostTime{Time: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), N: 7}
	run("hostmeth: {{.S.Year}} {{.S.N}}", dict("S", ht))

	// a zero template.Template's *Tree is nil — a promoted field read
	// through it must not abort the marshal (gc skips the member).
	var zt template.Template
	tz := &HostTpl{Template: zt, N: 3}
	run("nilhost: {{.S.N}}", dict("S", tz))

	a := &struct {
		Alias
		Z int
	}{Alias: Alias{Hidden: true, HiddenReason: "al"}, Z: 9}
	run("alias: {{.S.Hidden}} {{.S.Z}}", dict("S", a))
}
