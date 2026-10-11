package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// A bound named basic's host methods resolve on the host scalar: the
// script payload — a stored int64 or a const-domain UConst — converts
// to the host type at member dispatch, so time.Month.String and
// fmt's Stringer path print "January", and json.Number answers
// String/Int64/Float64 while staying a script string for len/indexing.
func main() {
	fmt.Println(time.January, time.January.String())
	var m time.Month = 3
	fmt.Println(m.String(), time.Month(3).String(), m+1)
	fmt.Println(time.Duration(6), time.Duration(6).String(), time.Duration(6).Nanoseconds())
	var n json.Number = "42"
	i, _ := n.Int64()
	f, _ := n.Float64()
	fmt.Println(n.String(), i, f, n+"x", string(n), len(n), n == "42", n[:2])
}
