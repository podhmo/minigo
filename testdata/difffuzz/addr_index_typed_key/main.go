package main

import "fmt"

type levelInfo struct {
	level, lastFreq int32
}

const maxBitsLimit = 16

func main() {
	var levels [maxBitsLimit]levelInfo
	levels[3] = levelInfo{level: 3, lastFreq: 7}
	level := uint32(3)
	l := &levels[level]
	l.lastFreq = 9
	fmt.Println(levels[3], l.lastFreq)
	k := int32(4)
	m := &levels[k]
	m.level = 1
	fmt.Println(levels[4])
}

type K string

func init() {
	s := []levelInfo{{}, {}}
	idx := int8(1)
	p := &s[idx]
	p.level = 5
	m := map[K]int{"a": 1}
	const key K = "a"
	m[key]++
	fmt.Println(s, m)
}
