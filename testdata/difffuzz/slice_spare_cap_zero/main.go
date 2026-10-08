package main

import "fmt"

// reslicing into spare capacity reads zeros, after make(len, cap) and
// after append grows the backing array — Go zero-fills both.

type T struct{ A int }

func main() {
	ms := make([]any, 0, 4)
	ms = ms[:2]
	fmt.Println(ms[1] == nil)
	ts := make([]T, 0, 4)
	ts = ts[:3]
	ts[2].A = 1
	fmt.Println(ts)

	var xs []any
	xs = append(xs, 1)
	xs = append(xs, 2, 3)
	ys := xs[:cap(xs)]
	fmt.Println(ys[len(ys)-1] == nil || len(ys) == 3)
	var us []T
	us = append(us, T{1}, T{2}, T{3})
	vs := us[:cap(us)]
	vs[len(vs)-1].A += 5
	fmt.Println(vs[len(vs)-1].A == 5 || len(vs) == 3)
	bs := make([]byte, 0, 2)
	bs = append(bs, 'a', 'b', 'c')
	full := bs[:cap(bs)]
	fmt.Println(full[len(full)-1])
}
