package main

import (
	"fmt"
	"time"
)

// time.Weekday and its Sunday..Saturday constants bind like time.Month:
// printing, conversion, zero value, %T, comparison with Time.Weekday,
// switch, map keys, min/max, the String method and fmt.Stringer —
// including dynamic interface checks and method constraints on a bound
// constant or conversion, not just on a value a host call returned.

type sched map[time.Weekday]string

func kind(w time.Weekday) string {
	switch w {
	case time.Saturday, time.Sunday:
		return "weekend"
	case time.Friday:
		return "friday"
	}
	return "weekday"
}

type SI interface {
	~int
	String() string
}

type SD interface {
	~int64
	String() string
}

func name[T SI](x T) string  { return x.String() }
func dname[T SD](x T) string { return x.String() }

// D and M are fresh named types: they do not inherit the host methods.
type D time.Duration
type M time.Month

func kindOf(v any) string {
	switch x := v.(type) {
	case fmt.Stringer:
		return "stringer " + x.String()
	}
	return "plain"
}

func main() {
	fmt.Println(time.Monday, time.Sunday, time.Saturday) // Monday Sunday Saturday
	fmt.Println(time.Weekday(5), int(time.Wednesday))    // Friday 3
	var w time.Weekday
	fmt.Println(w, w == time.Sunday)                                          // Sunday true
	fmt.Printf("%T %v %d %s\n", w, time.Tuesday, time.Tuesday, time.Thursday) // time.Weekday Tuesday 2 Thursday
	t := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	fmt.Println(t.Weekday() == time.Friday, t.Weekday() == 5, t.Weekday()) // true true Friday
	fmt.Printf("%T\n", t.Weekday())                                        // time.Weekday
	for d := time.Sunday; d <= time.Saturday; d++ {
		fmt.Print(kind(d), " ")
	}
	fmt.Println()
	fmt.Println(kind(t.Weekday()), kind(t.AddDate(0, 0, 1).Weekday()))
	m := sched{time.Monday: "gym", t.Weekday(): "pizza"}
	fmt.Println(m[time.Friday], m[time.Monday], m[time.Weekday(1)], len(m))
	fmt.Println(max(time.Monday, t.Weekday()), min(t.Weekday(), time.Monday), max(t.Weekday(), 6))
	fmt.Printf("%T\n", min(t.Weekday(), time.Monday))
	fmt.Println(w.String(), time.Saturday.String(), t.Weekday().String(), time.Weekday(9).String())
	var s fmt.Stringer = time.Wednesday
	fmt.Println(s, s.String())
	s = time.March // a bound Month satisfies fmt.Stringer the same way
	fmt.Println(s)
	ws := []time.Weekday{time.Friday, time.Monday}
	fmt.Println(ws, time.Weekday(1) == time.Monday, time.Weekday(1) < time.Tuesday)
	var any1 any = time.Monday
	_, ok := any1.(time.Weekday)
	_, ok2 := any1.(time.Month)
	fmt.Println(ok, ok2)

	var a any = time.Wednesday
	_, sok := a.(fmt.Stringer)
	_, sok1 := any(time.March).(fmt.Stringer)
	_, sok2 := any(time.Duration(5)).(fmt.Stringer)
	_, sok3 := any(time.Month(0)).(fmt.Stringer)
	_, sok4 := any(time.Weekday(3)).(fmt.Stringer)
	fmt.Println(sok, sok1, sok2, sok3, sok4)
	fmt.Println(kindOf(time.Friday), kindOf(time.March), kindOf(time.Second), kindOf(3))
	var e any = s
	s2, sok5 := e.(fmt.Stringer)
	fmt.Println(sok5, s2)
	_, sok6 := any(D(5)).(fmt.Stringer)
	_, sok7 := any(M(1)).(fmt.Stringer)
	fmt.Println(sok6, sok7, kindOf(D(5)), kindOf(M(1)))
	fmt.Println(name(time.Monday), name(time.May), dname(time.Second), dname(3*time.Millisecond))
	fmt.Println(name(t.Weekday()), name(t.Month()), kindOf(t.Weekday()))
}
