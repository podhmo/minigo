package main

import (
	"fmt"
	"time"
)

// min/max over a bound host named basic: a time.Month from Time.Month
// compares by value with the bound time.January/March constants in
// either argument order, like any other operand of the same type.

func main() {
	mo := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC).Month()
	fmt.Println(max(time.January, mo), max(mo, time.January), min(time.December, mo), min(mo, time.December)) // March March March March
	jan := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).Month()
	fmt.Println(max(jan, time.March), min(time.March, jan))  // March January
	fmt.Printf("%T %T\n", max(jan, time.March), max(jan, 5)) // time.Month time.Month
	fmt.Println(max(jan, 5), min(5, jan))                    // May January
	wd := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC).Weekday()
	fmt.Println(max(time.Monday, wd), min(wd, time.Monday), max(wd, time.Saturday)) // Friday Monday Saturday
	fmt.Printf("%T\n", min(time.Sunday, wd))                                        // time.Weekday
	d := 3 * time.Second
	fmt.Println(max(d, 5*time.Second), min(d, time.Second)) // 5s 1s
}
