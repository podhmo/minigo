// Package main exercises boxed host named slices (net.IP) and strings
// (template.HTML) that the embedder hands in as host values, and the
// script-side forms of the same types: append, copy, spread, conversions,
// the slices/sort helpers, literals and make on a slice; operators and
// conversions on a string. A net.IP stays a net.IP through every one.
package main

import (
	"fmt"
	"html/template"
	"net"
	"net/url"
	"slices"
	"sort"
)

// Append grows a net.IP: the result is a net.IP, and an append within
// capacity writes the shared backing.
func Append(ip net.IP) string {
	a := append(ip, 9)
	c := append(ip, 1, 2, 3)
	b := append(ip[:2], 7)
	return fmt.Sprintf("%v %T %d|%v %d|%v|%v", a, a, len(a), c, len(c), b, ip)
}

// AppendNil appends to a nil net.IP and to a typed nil.
func AppendNil(nilIP net.IP) string {
	var ip net.IP
	ip = append(ip, 1, 2)
	n := append(nilIP, 3)
	bad := net.ParseIP("bad")
	bad = append(bad, 4)
	return fmt.Sprintf("%v %T|%v %T|%v %T", ip, ip, n, n, bad, bad)
}

func sum(xs ...byte) int {
	n := 0
	for _, x := range xs {
		n += int(x)
	}
	return n
}

// Spread spreads a net.IP into append and into a variadic call.
func Spread(ip net.IP) string {
	b := append([]byte{0}, ip...)
	c := append(ip[:1], ip[2:]...)
	d := append(net.IP{}, ip...)
	return fmt.Sprintf("%v|%v %T|%d|%d", b, d, d, sum(ip...), len(c))
}

// Copy copies from and into a net.IP, overlapping copies included.
func Copy(ip net.IP) string {
	dst := make([]byte, 6)
	n := copy(dst, ip)
	m := copy(ip, []byte{9, 8})
	k := copy(ip[1:], ip)
	s := copy(ip, "xy")
	return fmt.Sprintf("%d %v|%d %d %d|%v", n, dst, m, k, s, ip)
}

// CopyIP copies between two boxed net.IPs.
func CopyIP(ip, other net.IP) string {
	n := copy(ip, other)
	return fmt.Sprintf("%d %v %v", n, ip, other)
}

// Convert converts a net.IP to []byte, string and an array.
func Convert(ip net.IP) string {
	b := []byte(ip)
	s := string(ip)
	arr := [4]byte(ip)
	return fmt.Sprintf("%v %T %q %v %T", b, b, s, arr, arr)
}

// ConvertShort converts a net.IP to a longer array.
func ConvertShort(ip net.IP) (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	_ = [8]byte(ip)
	return ""
}

// ConvertTo converts script values to net.IP and between host types.
func ConvertTo(ip net.IP) string {
	b := []byte{10, 20, 30, 40}
	x := net.IP(b)
	h := net.HardwareAddr(ip)
	y := net.IP(h)
	s := net.IP("abcd")
	return fmt.Sprintf("%v %T|%v %T|%v|%v %T", x, x, h, h, y.Equal(ip), s, s)
}

// Helpers runs the slices helpers on a net.IP: in place ones write the
// host backing, the cloning ones return a net.IP.
func Helpers(ip net.IP) string {
	c := slices.Clone(ip)
	slices.Sort(ip)
	sorted := fmt.Sprint(ip)
	slices.Reverse(ip)
	rev := fmt.Sprint(ip)
	slices.SortFunc(ip, func(a, b byte) int { return int(b) - int(a) })
	i, found := slices.BinarySearchFunc(ip, byte(3), func(e, t byte) int { return int(t) - int(e) })
	return fmt.Sprintf("%v %T|%s|%s|%v|%d %v|%v", c, c, sorted, rev, ip, i, found, slices.Equal(c, ip))
}

// Reshape compacts and deletes in place on a net.IP.
func Reshape(ip net.IP) string {
	d := slices.Clone(ip)
	d = slices.Compact(d)
	e := slices.Delete(ip, 1, 2)
	f := slices.Insert(net.IP{1}, 1, 2, 3)
	return fmt.Sprintf("%v %T|%v %T|%v %T|%v", d, d, e, e, f, f, ip)
}

// Alias writes through a slices.Delete result, which views the same
// backing as its net.IP argument.
func Alias(ip net.IP) string {
	e := slices.Delete(ip, 1, 2)
	e[0] = 99
	return fmt.Sprintf("%v %v %T", ip, e, e)
}

// Captured appends to and copies into a net.IP held by a closure.
func Captured(ip net.IP) string {
	f := func() {
		ip = append(ip, 5)
		copy(ip, []byte{9})
	}
	f()
	return fmt.Sprint(ip, " ", len(ip))
}

// Parsed runs the same operations on a net.IP a host call returned.
func Parsed() string {
	ip := net.ParseIP("1.2.3.4").To4()
	a := append(ip, 9)
	n := copy(ip, []byte{9})
	s := slices.Clone(ip)
	slices.Reverse(ip)
	sort.Slice(s, func(i, j int) bool { return s[i] > s[j] })
	return fmt.Sprintf("%v %T|%d|%v %T|%v|%v", a, a, n, s, s, ip, []byte(ip))
}

// SortSlice orders a net.IP with sort.Slice, whose less reads the slice.
func SortSlice(ip net.IP) string {
	sort.Slice(ip, func(i, j int) bool { return ip[i] > ip[j] })
	desc := fmt.Sprint(ip)
	sort.SliceStable(ip, func(i, j int) bool { return ip[i] < ip[j] })
	return fmt.Sprint(desc, " ", ip, " ", sort.SliceIsSorted(ip, func(i, j int) bool { return ip[i] < ip[j] }))
}

// SortStrings orders a host sort.StringSlice, passed as a []string, with
// sort.Strings.
func SortStrings(ss []string) string {
	sort.Strings(ss)
	return fmt.Sprint(ss, " ", slices.Contains(ss, "b"))
}

// Literal builds net.IP values in the script.
func Literal() string {
	ip := net.IP{1, 2, 3, 4}
	sparse := net.IP{0: 9, 3: 7}
	m := make(net.IP, 2, 8)
	m[0] = 10
	m = append(m, 11)
	var nilIP net.IP
	return fmt.Sprintf("%v %T %d|%v|%v %d %d|%s|%v %d", ip, ip, len(ip), sparse, m, len(m), cap(m), ip.String(), nilIP == nil, len(net.IP{}))
}

// LiteralOps appends, copies and converts a script net.IP.
func LiteralOps() string {
	ip := net.IP{1, 2, 3, 4}
	a := append(ip, 5)
	n := copy(ip, []byte{9})
	b := []byte(ip)
	s := slices.Clone(ip)
	slices.Reverse(s)
	return fmt.Sprintf("%v %T|%d %v|%v|%v %T", a, a, n, ip, b, s, s)
}

// MakeOOB makes a net.IP with a negative length.
func MakeOOB() (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	n := -1
	_ = make(net.IP, n)
	return ""
}

// MakeMap makes a host named map and writes it.
func MakeMap() string {
	q := make(url.Values)
	q["a"] = []string{"1"}
	q.Add("a", "2")
	return fmt.Sprint(q, len(q), q.Encode())
}

// Add adds to and compares a template.HTML; the sum is a template.HTML.
func Add(h template.HTML) string {
	g := h + "x"
	g += template.HTML("!")
	return fmt.Sprintf("%v %T %d", g, g, len(g))
}

// Order compares a template.HTML with a string constant.
func Order(h template.HTML) string {
	return fmt.Sprint(h < "z", h > "z", h <= "<b>hi</b>", h >= "<b>hi</b>", h == "<b>hi</b>", h != "q")
}

// Bytes converts a template.HTML to bytes, runes and a string.
func Bytes(h template.HTML) string {
	b := []byte(h)
	r := []rune(h)
	s := string(h)
	return fmt.Sprintf("%v %T|%d|%q %T", b[:3], b, len(r), s, s)
}

// Script does the same on a template.HTML built in the script.
func Script() string {
	h := template.HTML("<i>")
	g := h + "x"
	return fmt.Sprintf("%v %T %v %v %v", g, g, h < "z", []byte(h)[1], string(g))
}

func main() {}
