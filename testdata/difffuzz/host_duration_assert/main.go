package main

// A time.Duration held untagged in an interface — a folded constant
// (`var x any = 2 * time.Second`) or a host result (time.Time.Sub) —
// asserts and type-switches to time.Duration, never to int64, as in
// the go toolchain; a host time.Month does the same for time.Month.

import (
	"fmt"
	"time"
)

func kind(x any) string {
	switch v := x.(type) {
	case time.Duration:
		return "dur " + v.String()
	case time.Month:
		return "month " + v.String()
	case int64:
		return "int64"
	case int:
		return "int"
	}
	return "default"
}

func intFirst(x any) string {
	switch x.(type) {
	case int64:
		return "int64"
	case int:
		return "int"
	case time.Duration:
		return "dur"
	}
	return "default"
}

func main() {
	var x any = 2 * time.Second
	_, ok := x.(time.Duration)
	fmt.Println(ok, kind(x), intFirst(x))
	var y any = time.Second
	_, ok = y.(time.Duration)
	fmt.Println(ok, kind(y))
	t := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	d := t.Add(time.Hour).Sub(t)
	var z any = d
	dd, ok := z.(time.Duration)
	fmt.Println(dd, ok, kind(z), kind(d), intFirst(d))
	var w any = time.Duration(5)
	_, ok = w.(time.Duration)
	fmt.Println(ok, kind(w))
	var mo any = t.Month()
	m2, ok := mo.(time.Month)
	fmt.Println(m2, ok, kind(mo), kind(time.March))
	_, ok = x.(int64)
	fmt.Println(ok, kind(int64(3)), kind(3))
}
