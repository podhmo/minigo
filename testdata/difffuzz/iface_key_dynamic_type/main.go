package main

// An interface-typed map key is a (dynamic type, value) pair: int8(1),
// M(2) and uint32(2) key apart from the int with the same payload, in a
// map[any], a map[I] for a named empty interface, a generic map[K] with
// K = any and a struct key with an `any` field; ranging yields the key
// with its type, and the declared aliases byte/rune key as uint8/int32.

import (
	"fmt"
	"maps"
	"time"
)

type M uint32

type K struct {
	A any
	B int
}

type I interface{}

func get[Key comparable, V any](m map[Key]V, k Key) V { return m[k] }

func mk[Key comparable]() map[Key]int { return make(map[Key]int) }

func main() {
	m := map[any]int{int8(1): 1}
	fmt.Println(m[1], m[int8(1)], len(m))
	m2 := map[any]int{M(2): 5}
	fmt.Println(m2[uint32(2)], m2[M(2)])
	for k, v := range m2 {
		fmt.Printf("%T %v %v\n", k, k, v)
	}
	b := map[any]int{byte(3): 1, rune(4): 2}
	fmt.Println(b[uint8(3)], b[int32(4)], b[3], b[4])
	f := map[any]int{float32(1.5): 1, 1.5: 2}
	fmt.Println(len(f), f[float32(1.5)], f[1.5])
	d := map[any]int{time.Second: 1, int64(time.Second): 2}
	fmt.Println(len(d), d[time.Second], d[1000*time.Millisecond], d[int64(1e9)])
	ks := map[K]int{{M(2), 1}: 1, {uint32(2), 1}: 2, {2, 1}: 3}
	fmt.Println(len(ks), ks[K{M(2), 1}], ks[K{2, 1}])

	mm := make(map[any]string)
	mm[M(1)] = "m"
	mm[1] = "i"
	delete(mm, uint32(1))
	fmt.Println(len(mm))
	delete(mm, M(1))
	fmt.Println(len(mm), mm[1])

	cp := map[any]int{}
	maps.Copy(cp, map[any]int{M(7): 1, 7: 2})
	fmt.Println(len(cp), cp[M(7)], cp[7])
	cl := maps.Clone(cp)
	fmt.Println(cl[M(7)], cl[7], cl[uint(7)])

	g := map[any]int{M(9): 9}
	fmt.Println(get(g, any(M(9))), get(g, any(9)))
	gm := mk[any]()
	gm[int16(5)] = 1
	gm[5] = 2
	fmt.Println(len(gm), gm[int16(5)])
	ii := map[I]int{M(1): 1, 1: 2}
	fmt.Println(len(ii))

	inc := map[any]int{}
	inc[M(3)]++
	inc[M(3)] += 2
	inc[3]++
	fmt.Println(inc[M(3)], inc[3], len(inc))
}
