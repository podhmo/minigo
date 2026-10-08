package main

import (
	"fmt"
	"time"
)

func main() {
	t, err := time.ParseInLocation(time.DateTime, "2024-03-05 10:20:30", time.UTC)
	fmt.Println(t, err)
	loc := time.FixedZone("JST", 9*3600)
	t, err = time.ParseInLocation(time.DateTime, "2024-03-05 10:20:30", loc)
	fmt.Println(t, t.Unix(), err)
	_, err = time.ParseInLocation(time.DateTime, "bogus", time.UTC)
	fmt.Println(err)
}
