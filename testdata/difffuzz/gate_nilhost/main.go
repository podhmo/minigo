package main

import (
	"fmt"
	"net"
	"net/url"
)

// Named host types whose zero shape is a slice or map answer `== nil` —
// the deferred gate must read their host kind, not their KindStruct.
func main() {
	var ip net.IP
	var hw net.HardwareAddr
	var v url.Values
	fmt.Println(ip == nil, hw == nil, v == nil)
}
