package main

import (
	"os"
	"text/template"
	"time"
)

// A bound host scalar (time.Duration, time.Month, time.Weekday) passed
// to a host `any` parameter crosses as the host type, whether it is a
// constant, a conversion or a var holding a constant, and also when
// nested in a slice, map or struct field the template walks.

const cd time.Duration = 7

type wrap struct {
	Wait  time.Duration
	Month time.Month
}

func main() {
	tpl := template.Must(template.New("t").Parse("{{.}} {{printf \"%T\" .}}\n"))
	var m time.Month = 3
	var d time.Duration = 9
	tpl.Execute(os.Stdout, time.Duration(5))
	tpl.Execute(os.Stdout, cd)
	tpl.Execute(os.Stdout, time.March)
	tpl.Execute(os.Stdout, m)
	tpl.Execute(os.Stdout, d)
	tpl.Execute(os.Stdout, time.Monday)
	tpl.Execute(os.Stdout, time.Second)
	for _, v := range []any{time.Duration(5), cd, time.March, m, time.Weekday(2)} {
		tpl.Execute(os.Stdout, v)
	}
	t2 := template.Must(template.New("t2").Parse("{{.Wait}} {{.Month}} {{printf \"%T %T\" .Wait .Month}}\n"))
	t2.Execute(os.Stdout, wrap{Wait: cd, Month: time.April})
	t2.Execute(os.Stdout, &wrap{Wait: 2})
	t3 := template.Must(template.New("t3").Parse("{{range .}}{{.}}/{{printf \"%T\" .}} {{end}}\n"))
	t3.Execute(os.Stdout, []time.Duration{time.Duration(1), cd, time.Second})
	t3.Execute(os.Stdout, []time.Weekday{time.Friday, 2})
	t3.Execute(os.Stdout, map[string]time.Month{"a": time.May, "b": 6})
}
