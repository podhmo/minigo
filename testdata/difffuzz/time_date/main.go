package main

import (
	"fmt"
	"time"
)

func main() {
	t := time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC)
	fmt.Println(t.Format(time.RFC3339))
	u := time.Date(1980, 1, 2, 3, 4, 5, 6, time.Local)
	fmt.Println(u.Year(), int(u.Month()), u.Day())
}
