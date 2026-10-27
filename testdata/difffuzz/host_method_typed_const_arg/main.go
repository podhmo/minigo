package main

import (
	"fmt"
	"time"
)

// A typed constant of a bound host scalar — time.Duration(5), a
// `const d time.Duration = 5` — passed to a host method must convert at
// the parameter type like any other Duration, through direct calls,
// method values, defer and stored copies alike.

const cd time.Duration = 5

type cfg struct{ wait time.Duration }

func main() {
	t := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	fmt.Println(t.Add(time.Duration(5)).Nanosecond(), t.Add(cd).Nanosecond())
	fmt.Println(t.Add(time.Duration(5) * time.Second).Second())
	fmt.Println(t.Truncate(time.Duration(3600e9)).Equal(t), t.Round(time.Duration(0)).Equal(t))
	fmt.Println(time.Duration(5).Round(time.Duration(2)), time.Duration(90e9).Truncate(time.Duration(60e9)))
	add := t.Add
	fmt.Println(add(time.Duration(7)).Nanosecond(), add(cd).Nanosecond())
	m := map[string]time.Duration{"a": time.Duration(9)}
	c := cfg{wait: cd}
	fmt.Println(t.Add(m["a"]).Nanosecond(), t.Add(c.wait).Nanosecond())
	ds := []time.Duration{time.Duration(1), cd}
	for _, d := range ds {
		fmt.Print(t.Add(d).Nanosecond(), " ")
	}
	fmt.Println()
	func() {
		defer func() { fmt.Println("deferred") }()
		defer t.Add(time.Duration(5))
	}()
	tk := time.NewTicker(time.Hour)
	tk.Reset(time.Duration(1))
	<-tk.C
	tk.Stop()
	tm := time.NewTimer(time.Hour)
	fmt.Println(tm.Reset(time.Duration(1)))
	<-tm.C
	fmt.Println("timer")
	fmt.Println(t.AddDate(0, int(time.Month(2)), 0).Month(), t.Add(-cd).Year())
}
