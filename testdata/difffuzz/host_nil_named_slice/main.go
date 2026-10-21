package main

// A nil named host slice (net.ParseIP's IP on bad input) is a typed nil:
// it equals nil (also as a net.IP param, field or element), has length
// 0, dispatches IP's methods on the nil receiver (String prints
// "<nil>"), asserts back to net.IP from an interface, and an interface
// holding it is not nil. len/cap of a non-nil boxed IP measure the host
// slice.

import (
	"fmt"
	"net"
)

type rec struct{ IP net.IP }

func isNil(ip net.IP) bool { return ip == nil }

func main() {
	ip := net.ParseIP("bad")
	fmt.Println(ip == nil)
	fmt.Println(len(ip), cap(ip))
	fmt.Println(isNil(ip))
	var x any = ip
	fmt.Println(x == nil)
	fmt.Printf("%T %v\n", x, ip)
	ok := net.ParseIP("1.2.3.4")
	fmt.Println(ok == nil, len(ok), cap(ok), ok.String(), isNil(ok))
	fmt.Println(ip.String(), ip.To4() == nil)

	var ip2 net.IP = net.ParseIP("x")
	fmt.Println(ip2 == nil, len(ip2))
	r := rec{IP: ip}
	fmt.Println(r.IP == nil, r.IP.String())
	fmt.Println(ok.Equal(ip), ip.Equal(ok), ip.IsLoopback(), ip.To16() == nil)
	v, isIP := x.(net.IP)
	fmt.Println(v == nil, isIP)
	fmt.Printf("%v|%s|%T\n", x, ip, ip)
	n := 0
	for range ip {
		n++
	}
	fmt.Println(n)
	s := []net.IP{ip, ok}
	fmt.Println(s[0] == nil, s[1] == nil)
	fmt.Println(len(ok.To4()))
}
