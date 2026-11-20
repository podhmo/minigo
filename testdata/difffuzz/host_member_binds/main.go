package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	m, err := net.ParseMAC("00:00:5e:00:53:01")
	fmt.Println(m, err)
	var mask net.IPMask = net.CIDRMask(24, 32)
	fmt.Println(mask)
	loc, err := time.LoadLocation("UTC")
	fmt.Println(loc, err)
	c := time.Tick(5 * time.Millisecond)
	<-c
	fmt.Println("ticked")
	tickNil()
}

func tickNil() {
	c := time.Tick(0)
	fmt.Println(c == nil)
}
