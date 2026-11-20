package main

import (
	"fmt"
	"net"
)

func try(f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panicked:", r)
		}
	}()
	f()
}

func main() {
	ip := net.ParseIP("1.2.3.4")
	try(func() { p := &ip[99]; fmt.Println(p) })
	try(func() { p := &ip[0]; fmt.Println(*p) })
	h := map[string][]int{"a": {1}}
	try(func() { p := &h["nope"][0]; fmt.Println(p) })
}
