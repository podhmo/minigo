package main

// Nil pointers returned by bound host functions on their error paths
// bind into script slots typed with the host pointer type — params,
// vars, slice and map elements, struct fields — and stay nil there,
// including for bound packages that register no entry for the type
// itself (regexp.Regexp) and under a renamed import.

import (
	"fmt"
	"net"
	"os"
	"regexp"
	rx "regexp"
)

type holder struct {
	Re   *regexp.Regexp
	F    *os.File
	Addr *net.TCPAddr
}

func isNilRe(r *regexp.Regexp) bool        { return r == nil }
func isNilRx(r *rx.Regexp) bool            { return r == nil }
func isNilFile(f *os.File) bool            { return f == nil }
func isNilTCP(a *net.TCPAddr) bool         { return a == nil }
func isNilUDP(a *net.UDPAddr) bool         { return a == nil }
func isNilIf(i *net.Interface) bool        { return i == nil }
func isNilAny(x any) bool                  { return x == nil }
func reOr(r *regexp.Regexp) *regexp.Regexp { return r }

func main() {
	re, err := regexp.Compile("(")
	fmt.Println(re == nil, err != nil)
	fmt.Println(isNilRe(re), isNilRx(re), isNilAny(re), reOr(re) == nil)
	var x *regexp.Regexp = re
	var y *rx.Regexp = re
	fmt.Println(x == nil, y == nil, x == re)
	res := []*regexp.Regexp{re}
	m := map[string]*regexp.Regexp{"a": re}
	fmt.Println(res[0] == nil, m["a"] == nil)
	var a any = re
	_, ok := a.(*regexp.Regexp)
	fmt.Println(a == nil, ok)
	fmt.Printf("%T %v\n", re, re)

	f, err := os.Create("/nonexistent/minigo-host-nilptr/x")
	fmt.Println(f == nil, err != nil, isNilFile(f))

	ta, err := net.ResolveTCPAddr("tcp", "bad:addr:x")
	fmt.Println(ta == nil, err != nil, isNilTCP(ta))
	ua, err := net.ResolveUDPAddr("udp", "bad:addr:x")
	fmt.Println(ua == nil, err != nil, isNilUDP(ua))
	ifc, err := net.InterfaceByName("minigo-no-such-iface")
	fmt.Println(ifc == nil, err != nil, isNilIf(ifc))

	h := holder{Re: re, F: f, Addr: ta}
	fmt.Println(h.Re == nil, h.F == nil, h.Addr == nil)
	var h2 holder
	h2.Re, h2.F, h2.Addr = re, f, ta
	fmt.Println(h2.Re == nil, h2.F == nil, h2.Addr == nil)
	fmt.Printf("%+v\n", h2)
}
