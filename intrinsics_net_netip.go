package minigo

import (
	"net/netip"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installNetNetip(h *hostHelpers) {
	// net/netip: the value types back net/http's addr plumbing
	// (TCPAddrFromAddrPort and friends); bound as host values like net.
	e.Bind("net/netip", map[string]runtime.Value{
		"Addr":       hostType("net/netip.Addr", func() any { return netip.Addr{} }),
		"AddrPort":   hostType("net/netip.AddrPort", func() any { return netip.AddrPort{} }),
		"Prefix":     hostType("net/netip.Prefix", func() any { return netip.Prefix{} }),
		"AddrFrom4":  h.fn("netip.AddrFrom4", func(a []any) (any, error) { return netip.AddrFrom4(byteSlice4(a[0])), nil }),
		"AddrFrom16": h.fn("netip.AddrFrom16", func(a []any) (any, error) { return netip.AddrFrom16(byteSlice16(a[0])), nil }),
		"AddrFromSlice": h.fn("netip.AddrFromSlice", func(a []any) (any, error) {
			ip, ok := netip.AddrFromSlice(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(ip), ok}}, nil
		}),
		"AddrPortFrom": h.fn("netip.AddrPortFrom", func(a []any) (any, error) {
			return netip.AddrPortFrom(netipAddrOf(a[0]), uint16(intOf(a[1]))), nil
		}, netip.AddrPortFrom),
		"PrefixFrom": h.fn("netip.PrefixFrom", func(a []any) (any, error) {
			return netip.PrefixFrom(netipAddrOf(a[0]), intOf(a[1])), nil
		}),
		"MustParseAddr":         h.fn("netip.MustParseAddr", func(a []any) (any, error) { return netip.MustParseAddr(str(a[0])), nil }),
		"MustParseAddrPort":     h.fn("netip.MustParseAddrPort", func(a []any) (any, error) { return netip.MustParseAddrPort(str(a[0])), nil }),
		"MustParsePrefix":       h.fn("netip.MustParsePrefix", func(a []any) (any, error) { return netip.MustParsePrefix(str(a[0])), nil }),
		"ParseAddr":             h.fn("netip.ParseAddr", func(a []any) (any, error) { return retErr2(netip.ParseAddr(str(a[0]))) }),
		"ParsePrefix":           h.fn("netip.ParsePrefix", func(a []any) (any, error) { return retErr2(netip.ParsePrefix(str(a[0]))) }),
		"IPv4Unspecified":       h.fn("netip.IPv4Unspecified", func(a []any) (any, error) { return netip.IPv4Unspecified(), nil }),
		"IPv6LinkLocalAllNodes": h.fn("netip.IPv6LinkLocalAllNodes", func(a []any) (any, error) { return netip.IPv6LinkLocalAllNodes(), nil }),
		"IPv6Loopback":          h.fn("netip.IPv6Loopback", func(a []any) (any, error) { return netip.IPv6Loopback(), nil }),
		"IPv6Unspecified":       h.fn("netip.IPv6Unspecified", func(a []any) (any, error) { return netip.IPv6Unspecified(), nil }),
	})
}
