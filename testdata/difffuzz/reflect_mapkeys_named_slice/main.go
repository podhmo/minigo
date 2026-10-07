package main

import (
	"fmt"
	"reflect"
)

type keyList []reflect.Value

func main() {
	ks := reflect.ValueOf(map[int]int{1: 1}).MapKeys()
	fmt.Printf("%T %d\n", ks, len(ks))
	var kl keyList = ks
	fmt.Println(len(kl))
	kl2 := keyList(ks)
	fmt.Println(len(kl2))
}
