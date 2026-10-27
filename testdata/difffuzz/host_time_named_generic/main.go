package main

// A time.Month from time.Time.Month and a time.Duration from
// time.Time.Sub are the same types as time.March and time.Second: they
// bind a type parameter together, and as interface values they equal
// the constants (an untagged host Duration included), as in the go
// toolchain.

import (
	"fmt"
	"time"
)

func eq[T comparable](a, b T) bool { return a == b }

func id[T any](x T) T { return x }

func main() {
	t := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	mo := t.Month()
	fmt.Println(eq(mo, time.March), eq(time.March, mo), eq(mo, time.April))
	fmt.Printf("%T %v\n", id(mo), id(mo))
	var a any = mo
	fmt.Println(a == any(time.March), a == any(3))

	var di any = time.Duration(2e9)
	fmt.Println(di == any(2*time.Second), di == any(int64(2e9)))
	x := time.Duration(2e9)
	fmt.Println(any(x) == any(2*time.Second), x == 2*time.Second)
	d := t.Add(time.Hour).Sub(t)
	fmt.Println(eq(d, time.Hour), eq(time.Hour, d), any(d) == any(time.Hour), any(d) == any(int64(time.Hour)))
	fmt.Printf("%T %v\n", id(d), id(d))
}
