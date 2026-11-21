package net

import (
	"net"
	"net/netip"
	"os"

	"github.com/podhmo/minigo/internal/stdlib"

	"github.com/podhmo/minigo/runtime"
)

// Install binds net wholesale: sockets, DNS and syscall plumbing
// cannot be interpreted. Interfaces stay typedefs; concrete values are
// GoValue boxes dispatching through reflection.
func Install(bind stdlib.Bind, h stdlib.Helpers, unwrapFile func(any) *os.File) {
	// net: sockets, DNS and syscall plumbing cannot be interpreted, so
	// the package is bound wholesale. The interfaces stay typedefs so
	// script code declares net.Conn/net.Listener/net.Addr/net.Error;
	// concrete values arrive as GoValue boxes whose methods dispatch
	// through reflection, and script pointers (e.g. &net.TCPAddr{...})
	// unbox via hostbridge.GoNative at call boundaries.
	bind("net", map[string]runtime.Value{
		"Addr":             &runtime.TypeDef{Name: "net.Addr", Kind: runtime.KindInterface, MReqs: []string{"Network", "String"}},
		"Conn":             &runtime.TypeDef{Name: "net.Conn", Kind: runtime.KindInterface, MReqs: []string{"Read", "Write", "Close", "LocalAddr", "RemoteAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Listener":         &runtime.TypeDef{Name: "net.Listener", Kind: runtime.KindInterface, MReqs: []string{"Accept", "Close", "Addr"}},
		"PacketConn":       &runtime.TypeDef{Name: "net.PacketConn", Kind: runtime.KindInterface, MReqs: []string{"ReadFrom", "WriteTo", "Close", "LocalAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Error":            &runtime.TypeDef{Name: "net.Error", Kind: runtime.KindInterface, MReqs: []string{"Error", "Timeout", "Temporary"}},
		"Buffers":          stdlib.HostType("net.Buffers", func() any { return &net.Buffers{} }),
		"Dialer":           stdlib.HostType("net.Dialer", func() any { return &net.Dialer{} }),
		"ListenConfig":     stdlib.HostType("net.ListenConfig", func() any { return &net.ListenConfig{} }),
		"Resolver":         stdlib.HostType("net.Resolver", func() any { return &net.Resolver{} }),
		"Interface":        stdlib.HostType("net.Interface", func() any { return net.Interface{} }),
		"HardwareAddr":     stdlib.HostType("net.HardwareAddr", func() any { return net.HardwareAddr{} }),
		"IP":               stdlib.HostType("net.IP", func() any { return net.IP{} }),
		"IPMask":           stdlib.HostType("net.IPMask", func() any { return net.IPMask{} }),
		"IPAddr":           stdlib.HostType("net.IPAddr", func() any { return &net.IPAddr{} }),
		"IPNet":            stdlib.HostType("net.IPNet", func() any { return &net.IPNet{} }),
		"TCPAddr":          stdlib.HostType("net.TCPAddr", func() any { return &net.TCPAddr{} }),
		"TCPConn":          stdlib.HostType("net.TCPConn", func() any { return &net.TCPConn{} }),
		"TCPListener":      stdlib.HostType("net.TCPListener", func() any { return &net.TCPListener{} }),
		"UDPAddr":          stdlib.HostType("net.UDPAddr", func() any { return &net.UDPAddr{} }),
		"UDPConn":          stdlib.HostType("net.UDPConn", func() any { return &net.UDPConn{} }),
		"UnixAddr":         stdlib.HostType("net.UnixAddr", func() any { return &net.UnixAddr{} }),
		"UnixConn":         stdlib.HostType("net.UnixConn", func() any { return &net.UnixConn{} }),
		"UnixListener":     stdlib.HostType("net.UnixListener", func() any { return &net.UnixListener{} }),
		"OpError":          stdlib.HostType("net.OpError", func() any { return &net.OpError{} }),
		"DNSError":         stdlib.HostType("net.DNSError", func() any { return &net.DNSError{} }),
		"AddrError":        stdlib.HostType("net.AddrError", func() any { return &net.AddrError{} }),
		"ParseError":       stdlib.HostType("net.ParseError", func() any { return &net.ParseError{} }),
		"InvalidAddrError": stdlib.HostType("net.InvalidAddrError", func() any { return net.InvalidAddrError("") }),
		"Dial":             h.Fn("net.Dial", func(a []any) (any, error) { return stdlib.RetErr2(net.Dial(stdlib.Str(a[0]), stdlib.Str(a[1]))) }),
		"DialIP": h.Fn("net.DialIP", func(a []any) (any, error) {
			return stdlib.RetErr2(net.DialIP(stdlib.Str(a[0]), ipAddrOf(a[1]), ipAddrOf(a[2])))
		}),
		"DialTCP": h.Fn("net.DialTCP", func(a []any) (any, error) {
			return stdlib.RetErr2(net.DialTCP(stdlib.Str(a[0]), tcpAddrOf(a[1]), tcpAddrOf(a[2])))
		}),
		"DialTimeout": h.Fn("net.DialTimeout", func(a []any) (any, error) {
			return stdlib.RetErr2(net.DialTimeout(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.DurOf(a[2])))
		}),
		"DialUDP": h.Fn("net.DialUDP", func(a []any) (any, error) {
			return stdlib.RetErr2(net.DialUDP(stdlib.Str(a[0]), udpAddrOf(a[1]), udpAddrOf(a[2])))
		}),
		"DialUnix": h.Fn("net.DialUnix", func(a []any) (any, error) {
			return stdlib.RetErr2(net.DialUnix(stdlib.Str(a[0]), unixAddrOf(a[1]), unixAddrOf(a[2])))
		}),
		"Listen":   h.Fn("net.Listen", func(a []any) (any, error) { return stdlib.RetErr2(net.Listen(stdlib.Str(a[0]), stdlib.Str(a[1]))) }),
		"ListenIP": h.Fn("net.ListenIP", func(a []any) (any, error) { return stdlib.RetErr2(net.ListenIP(stdlib.Str(a[0]), ipAddrOf(a[1]))) }),
		"ListenPacket": h.Fn("net.ListenPacket", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ListenPacket(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}),
		"ListenTCP":  h.Fn("net.ListenTCP", func(a []any) (any, error) { return stdlib.RetErr2(net.ListenTCP(stdlib.Str(a[0]), tcpAddrOf(a[1]))) }),
		"ListenUDP":  h.Fn("net.ListenUDP", func(a []any) (any, error) { return stdlib.RetErr2(net.ListenUDP(stdlib.Str(a[0]), udpAddrOf(a[1]))) }),
		"ListenUnix": h.Fn("net.ListenUnix", func(a []any) (any, error) { return stdlib.RetErr2(net.ListenUnix(stdlib.Str(a[0]), unixAddrOf(a[1]))) }),
		"ListenMulticastUDP": h.Fn("net.ListenMulticastUDP", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ListenMulticastUDP(stdlib.Str(a[0]), netIfaceOf(a[1]), udpAddrOf(a[2])))
		}),
		"FileConn":       h.Fn("net.FileConn", func(a []any) (any, error) { return stdlib.RetErr2(net.FileConn(netFileOf(unwrapFile, a[0]))) }),
		"FileListener":   h.Fn("net.FileListener", func(a []any) (any, error) { return stdlib.RetErr2(net.FileListener(netFileOf(unwrapFile, a[0]))) }),
		"FilePacketConn": h.Fn("net.FilePacketConn", func(a []any) (any, error) { return stdlib.RetErr2(net.FilePacketConn(netFileOf(unwrapFile, a[0]))) }),
		"Pipe": h.Fn("net.Pipe", func(a []any) (any, error) {
			c1, c2 := net.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(c1), stdlib.ScriptVal(c2)}}, nil
		}),
		"ResolveTCPAddr": h.Fn("net.ResolveTCPAddr", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ResolveTCPAddr(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}),
		"ResolveUDPAddr": h.Fn("net.ResolveUDPAddr", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ResolveUDPAddr(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}),
		"ResolveIPAddr": h.Fn("net.ResolveIPAddr", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ResolveIPAddr(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}),
		"ResolveUnixAddr": h.Fn("net.ResolveUnixAddr", func(a []any) (any, error) {
			return stdlib.RetErr2(net.ResolveUnixAddr(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}),
		"TCPAddrFromAddrPort": h.Fn("net.TCPAddrFromAddrPort", func(a []any) (any, error) { return net.TCPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"UDPAddrFromAddrPort": h.Fn("net.UDPAddrFromAddrPort", func(a []any) (any, error) { return net.UDPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"SplitHostPort": h.Fn("net.SplitHostPort", func(a []any) (any, error) {
			h, p, err := net.SplitHostPort(stdlib.Str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{h, p, stdlib.ErrVal(err)}}, nil
		}),
		"JoinHostPort": h.Fn("net.JoinHostPort", func(a []any) (any, error) { return net.JoinHostPort(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"ParseIP":      h.Fn("net.ParseIP", func(a []any) (any, error) { return net.ParseIP(stdlib.Str(a[0])), nil }),
		"ParseCIDR": h.Fn("net.ParseCIDR", func(a []any) (any, error) {
			ip, ipn, err := net.ParseCIDR(stdlib.Str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(ip), stdlib.ScriptVal(ipn), stdlib.ErrVal(err)}}, nil
		}),
		"ParseMAC": h.Fn("net.ParseMAC", func(a []any) (any, error) { return stdlib.RetErr2(net.ParseMAC(stdlib.Str(a[0]))) }),
		"IPv4": h.Fn("net.IPv4", func(a []any) (any, error) {
			return net.IPv4(byte(stdlib.IntOf(a[0])), byte(stdlib.IntOf(a[1])), byte(stdlib.IntOf(a[2])), byte(stdlib.IntOf(a[3]))), nil
		}),
		"IPv4Mask": h.Fn("net.IPv4Mask", func(a []any) (any, error) {
			return net.IPv4Mask(byte(stdlib.IntOf(a[0])), byte(stdlib.IntOf(a[1])), byte(stdlib.IntOf(a[2])), byte(stdlib.IntOf(a[3]))), nil
		}),
		"CIDRMask":    h.Fn("net.CIDRMask", func(a []any) (any, error) { return net.CIDRMask(stdlib.IntOf(a[0]), stdlib.IntOf(a[1])), nil }),
		"LookupAddr":  h.Fn("net.LookupAddr", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupAddr(stdlib.Str(a[0]))) }),
		"LookupCNAME": h.Fn("net.LookupCNAME", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupCNAME(stdlib.Str(a[0]))) }),
		"LookupHost":  h.Fn("net.LookupHost", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupHost(stdlib.Str(a[0]))) }),
		"LookupIP":    h.Fn("net.LookupIP", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupIP(stdlib.Str(a[0]))) }),
		"LookupMX":    h.Fn("net.LookupMX", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupMX(stdlib.Str(a[0]))) }),
		"LookupNS":    h.Fn("net.LookupNS", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupNS(stdlib.Str(a[0]))) }),
		"LookupPort":  h.Fn("net.LookupPort", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupPort(stdlib.Str(a[0]), stdlib.Str(a[1]))) }),
		"LookupSRV": h.Fn("net.LookupSRV", func(a []any) (any, error) {
			cname, srvs, err := net.LookupSRV(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.Str(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{cname, stdlib.ScriptVal(srvs), stdlib.ErrVal(err)}}, nil
		}),
		"LookupTXT":                  h.Fn("net.LookupTXT", func(a []any) (any, error) { return stdlib.RetErr2(net.LookupTXT(stdlib.Str(a[0]))) }),
		"Interfaces":                 h.Fn("net.Interfaces", func(a []any) (any, error) { return stdlib.RetErr2(net.Interfaces()) }),
		"InterfaceAddrs":             h.Fn("net.InterfaceAddrs", func(a []any) (any, error) { return stdlib.RetErr2(net.InterfaceAddrs()) }),
		"InterfaceByIndex":           h.Fn("net.InterfaceByIndex", func(a []any) (any, error) { return stdlib.RetErr2(net.InterfaceByIndex(stdlib.IntOf(a[0]))) }),
		"InterfaceByName":            h.Fn("net.InterfaceByName", func(a []any) (any, error) { return stdlib.RetErr2(net.InterfaceByName(stdlib.Str(a[0]))) }),
		"DefaultResolver":            &runtime.GoValue{V: net.DefaultResolver},
		"ErrClosed":                  &runtime.GoValue{V: net.ErrClosed},
		"ErrWriteToConnected":        &runtime.GoValue{V: net.ErrWriteToConnected},
		"IPv4zero":                   &runtime.GoValue{V: net.IPv4zero},
		"IPv4bcast":                  &runtime.GoValue{V: net.IPv4bcast},
		"IPv4allsys":                 &runtime.GoValue{V: net.IPv4allsys},
		"IPv4allrouter":              &runtime.GoValue{V: net.IPv4allrouter},
		"IPv6zero":                   &runtime.GoValue{V: net.IPv6zero},
		"IPv6unspecified":            &runtime.GoValue{V: net.IPv6unspecified},
		"IPv6interfacelocalallnodes": &runtime.GoValue{V: net.IPv6interfacelocalallnodes},
		"IPv6linklocalallnodes":      &runtime.GoValue{V: net.IPv6linklocalallnodes},
		"IPv6linklocalallrouters":    &runtime.GoValue{V: net.IPv6linklocalallrouters},
		"IPv6loopback":               &runtime.GoValue{V: net.IPv6loopback},
	})
}

// net pointer/addr unwrappers: a script &net.TCPAddr{...} arrives at a
// host call boundary as a GoValue box, unboxed by hostbridge.GoNative upstream;
// nil stays a nil pointer like Go's optional-addr arguments.
func tcpAddrOf(v any) *net.TCPAddr { t, _ := v.(*net.TCPAddr); return t }

func udpAddrOf(v any) *net.UDPAddr { t, _ := v.(*net.UDPAddr); return t }

func unixAddrOf(v any) *net.UnixAddr { t, _ := v.(*net.UnixAddr); return t }

func ipAddrOf(v any) *net.IPAddr { t, _ := v.(*net.IPAddr); return t }

func netIfaceOf(v any) *net.Interface { t, _ := v.(*net.Interface); return t }

func netFileOf(unwrapFile func(any) *os.File, v any) *os.File {
	return unwrapFile(v)
}

func addrPortOf(v any) netip.AddrPort { t, _ := v.(netip.AddrPort); return t }
