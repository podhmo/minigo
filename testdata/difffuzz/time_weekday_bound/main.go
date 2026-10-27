package main

import (
	"fmt"
	"time"
)

// time.Weekday and its Sunday..Saturday constants bind like time.Month:
// printing, conversion, zero value, %T, comparison with Time.Weekday,
// switch, map keys, min/max, the String method and fmt.Stringer.

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
}
