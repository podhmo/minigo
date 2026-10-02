package main

import "fmt"

var pf float64 = 3

type Stat struct{ Avg float64 }

func half(x float64) float64 { return x / 2 }
func ret() float64           { return 3 }

func main() {
	var lf float64 = 3
	var f float64
	f = 3
	s := Stat{Avg: 3}
	m := map[string]float64{"a": 3}
	sl := []float64{3}
	p := new(float64)
	*p = 3
	ch := make(chan float64, 1)
	ch <- 3
	fmt.Println(pf/2, lf/2, f/2, s.Avg/2, half(3), ret()/2, m["a"]/2, sl[0]/2, *p/2, <-ch/2)

	var lz float64
	lz += 3
	cf := float64(3)
	fmt.Println(lz/2, cf/2, 3/2.0)

	var i64 int64 = 7
	fmt.Printf("%T %T %T\n", lf, i64, int8(i64))
}
