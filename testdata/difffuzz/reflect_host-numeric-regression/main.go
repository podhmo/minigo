package main

import (
	"fmt"
	"time"
)

func main() { var a any = time.Unix(0, 0).UTC().Weekday(); var b any = int(4); fmt.Println(a == b) }
