package minigo

import (
	"net"
	"net/netip"
	"os"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installNet(h *hostHelpers) {
	// net: sockets, DNS and syscall plumbing cannot be interpreted, so
	// the package is bound wholesale. The interfaces stay typedefs so
	// script code declares net.Conn/net.Listener/net.Addr/net.Error;
	// concrete values arrive as GoValue boxes whose methods dispatch
	// through reflection, and script pointers (e.g. &net.TCPAddr{...})
	// unbox via hostbridge.GoNative at call boundaries.
	e.Bind("net", map[string]runtime.Value{
		"Addr":             &runtime.TypeDef{Name: "net.Addr", Kind: runtime.KindInterface, MReqs: []string{"Network", "String"}},
		"Conn":             &runtime.TypeDef{Name: "net.Conn", Kind: runtime.KindInterface, MReqs: []string{"Read", "Write", "Close", "LocalAddr", "RemoteAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Listener":         &runtime.TypeDef{Name: "net.Listener", Kind: runtime.KindInterface, MReqs: []string{"Accept", "Close", "Addr"}},
		"PacketConn":       &runtime.TypeDef{Name: "net.PacketConn", Kind: runtime.KindInterface, MReqs: []string{"ReadFrom", "WriteTo", "Close", "LocalAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Error":            &runtime.TypeDef{Name: "net.Error", Kind: runtime.KindInterface, MReqs: []string{"Error", "Timeout", "Temporary"}},
		"Buffers":          hostType("net.Buffers", func() any { return &net.Buffers{} }),
		"Dialer":           hostType("net.Dialer", func() any { return &net.Dialer{} }),
		"ListenConfig":     hostType("net.ListenConfig", func() any { return &net.ListenConfig{} }),
		"Resolver":         hostType("net.Resolver", func() any { return &net.Resolver{} }),
		"Interface":        hostType("net.Interface", func() any { return net.Interface{} }),
		"HardwareAddr":     hostType("net.HardwareAddr", func() any { return net.HardwareAddr{} }),
		"IP":               hostType("net.IP", func() any { return net.IP{} }),
		"IPAddr":           hostType("net.IPAddr", func() any { return &net.IPAddr{} }),
		"IPNet":            hostType("net.IPNet", func() any { return &net.IPNet{} }),
		"TCPAddr":          hostType("net.TCPAddr", func() any { return &net.TCPAddr{} }),
		"TCPConn":          hostType("net.TCPConn", func() any { return &net.TCPConn{} }),
		"TCPListener":      hostType("net.TCPListener", func() any { return &net.TCPListener{} }),
		"UDPAddr":          hostType("net.UDPAddr", func() any { return &net.UDPAddr{} }),
		"UDPConn":          hostType("net.UDPConn", func() any { return &net.UDPConn{} }),
		"UnixAddr":         hostType("net.UnixAddr", func() any { return &net.UnixAddr{} }),
		"UnixConn":         hostType("net.UnixConn", func() any { return &net.UnixConn{} }),
		"UnixListener":     hostType("net.UnixListener", func() any { return &net.UnixListener{} }),
		"OpError":          hostType("net.OpError", func() any { return &net.OpError{} }),
		"DNSError":         hostType("net.DNSError", func() any { return &net.DNSError{} }),
		"AddrError":        hostType("net.AddrError", func() any { return &net.AddrError{} }),
		"ParseError":       hostType("net.ParseError", func() any { return &net.ParseError{} }),
		"InvalidAddrError": hostType("net.InvalidAddrError", func() any { return net.InvalidAddrError("") }),
		"Dial":             h.fn("net.Dial", func(a []any) (any, error) { return retErr2(net.Dial(str(a[0]), str(a[1]))) }),
		"DialIP":           h.fn("net.DialIP", func(a []any) (any, error) { return retErr2(net.DialIP(str(a[0]), ipAddrOf(a[1]), ipAddrOf(a[2]))) }),
		"DialTCP":          h.fn("net.DialTCP", func(a []any) (any, error) { return retErr2(net.DialTCP(str(a[0]), tcpAddrOf(a[1]), tcpAddrOf(a[2]))) }),
		"DialTimeout":      h.fn("net.DialTimeout", func(a []any) (any, error) { return retErr2(net.DialTimeout(str(a[0]), str(a[1]), durOf(a[2]))) }),
		"DialUDP":          h.fn("net.DialUDP", func(a []any) (any, error) { return retErr2(net.DialUDP(str(a[0]), udpAddrOf(a[1]), udpAddrOf(a[2]))) }),
		"DialUnix": h.fn("net.DialUnix", func(a []any) (any, error) {
			return retErr2(net.DialUnix(str(a[0]), unixAddrOf(a[1]), unixAddrOf(a[2])))
		}),
		"Listen":       h.fn("net.Listen", func(a []any) (any, error) { return retErr2(net.Listen(str(a[0]), str(a[1]))) }),
		"ListenIP":     h.fn("net.ListenIP", func(a []any) (any, error) { return retErr2(net.ListenIP(str(a[0]), ipAddrOf(a[1]))) }),
		"ListenPacket": h.fn("net.ListenPacket", func(a []any) (any, error) { return retErr2(net.ListenPacket(str(a[0]), str(a[1]))) }),
		"ListenTCP":    h.fn("net.ListenTCP", func(a []any) (any, error) { return retErr2(net.ListenTCP(str(a[0]), tcpAddrOf(a[1]))) }),
		"ListenUDP":    h.fn("net.ListenUDP", func(a []any) (any, error) { return retErr2(net.ListenUDP(str(a[0]), udpAddrOf(a[1]))) }),
		"ListenUnix":   h.fn("net.ListenUnix", func(a []any) (any, error) { return retErr2(net.ListenUnix(str(a[0]), unixAddrOf(a[1]))) }),
		"ListenMulticastUDP": h.fn("net.ListenMulticastUDP", func(a []any) (any, error) {
			return retErr2(net.ListenMulticastUDP(str(a[0]), netIfaceOf(a[1]), udpAddrOf(a[2])))
		}),
		"FileConn":       h.fn("net.FileConn", func(a []any) (any, error) { return retErr2(net.FileConn(netFileOf(a[0]))) }),
		"FileListener":   h.fn("net.FileListener", func(a []any) (any, error) { return retErr2(net.FileListener(netFileOf(a[0]))) }),
		"FilePacketConn": h.fn("net.FilePacketConn", func(a []any) (any, error) { return retErr2(net.FilePacketConn(netFileOf(a[0]))) }),
		"Pipe": h.fn("net.Pipe", func(a []any) (any, error) {
			c1, c2 := net.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(c1), scriptVal(c2)}}, nil
		}),
		"ResolveTCPAddr":      h.fn("net.ResolveTCPAddr", func(a []any) (any, error) { return retErr2(net.ResolveTCPAddr(str(a[0]), str(a[1]))) }),
		"ResolveUDPAddr":      h.fn("net.ResolveUDPAddr", func(a []any) (any, error) { return retErr2(net.ResolveUDPAddr(str(a[0]), str(a[1]))) }),
		"ResolveIPAddr":       h.fn("net.ResolveIPAddr", func(a []any) (any, error) { return retErr2(net.ResolveIPAddr(str(a[0]), str(a[1]))) }),
		"ResolveUnixAddr":     h.fn("net.ResolveUnixAddr", func(a []any) (any, error) { return retErr2(net.ResolveUnixAddr(str(a[0]), str(a[1]))) }),
		"TCPAddrFromAddrPort": h.fn("net.TCPAddrFromAddrPort", func(a []any) (any, error) { return net.TCPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"UDPAddrFromAddrPort": h.fn("net.UDPAddrFromAddrPort", func(a []any) (any, error) { return net.UDPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"SplitHostPort": h.fn("net.SplitHostPort", func(a []any) (any, error) {
			h, p, err := net.SplitHostPort(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{h, p, errVal(err)}}, nil
		}),
		"JoinHostPort": h.fn("net.JoinHostPort", func(a []any) (any, error) { return net.JoinHostPort(str(a[0]), str(a[1])), nil }),
		"ParseIP":      h.fn("net.ParseIP", func(a []any) (any, error) { return net.ParseIP(str(a[0])), nil }),
		"ParseCIDR": h.fn("net.ParseCIDR", func(a []any) (any, error) {
			ip, ipn, err := net.ParseCIDR(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(ip), scriptVal(ipn), errVal(err)}}, nil
		}),
		"IPv4": h.fn("net.IPv4", func(a []any) (any, error) {
			return net.IPv4(byte(intOf(a[0])), byte(intOf(a[1])), byte(intOf(a[2])), byte(intOf(a[3]))), nil
		}),
		"IPv4Mask": h.fn("net.IPv4Mask", func(a []any) (any, error) {
			return net.IPv4Mask(byte(intOf(a[0])), byte(intOf(a[1])), byte(intOf(a[2])), byte(intOf(a[3]))), nil
		}),
		"CIDRMask":    h.fn("net.CIDRMask", func(a []any) (any, error) { return net.CIDRMask(intOf(a[0]), intOf(a[1])), nil }),
		"LookupAddr":  h.fn("net.LookupAddr", func(a []any) (any, error) { return retErr2(net.LookupAddr(str(a[0]))) }),
		"LookupCNAME": h.fn("net.LookupCNAME", func(a []any) (any, error) { return retErr2(net.LookupCNAME(str(a[0]))) }),
		"LookupHost":  h.fn("net.LookupHost", func(a []any) (any, error) { return retErr2(net.LookupHost(str(a[0]))) }),
		"LookupIP":    h.fn("net.LookupIP", func(a []any) (any, error) { return retErr2(net.LookupIP(str(a[0]))) }),
		"LookupMX":    h.fn("net.LookupMX", func(a []any) (any, error) { return retErr2(net.LookupMX(str(a[0]))) }),
		"LookupNS":    h.fn("net.LookupNS", func(a []any) (any, error) { return retErr2(net.LookupNS(str(a[0]))) }),
		"LookupPort":  h.fn("net.LookupPort", func(a []any) (any, error) { return retErr2(net.LookupPort(str(a[0]), str(a[1]))) }),
		"LookupSRV": h.fn("net.LookupSRV", func(a []any) (any, error) {
			cname, srvs, err := net.LookupSRV(str(a[0]), str(a[1]), str(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{cname, scriptVal(srvs), errVal(err)}}, nil
		}),
		"LookupTXT":                  h.fn("net.LookupTXT", func(a []any) (any, error) { return retErr2(net.LookupTXT(str(a[0]))) }),
		"Interfaces":                 h.fn("net.Interfaces", func(a []any) (any, error) { return retErr2(net.Interfaces()) }),
		"InterfaceAddrs":             h.fn("net.InterfaceAddrs", func(a []any) (any, error) { return retErr2(net.InterfaceAddrs()) }),
		"InterfaceByIndex":           h.fn("net.InterfaceByIndex", func(a []any) (any, error) { return retErr2(net.InterfaceByIndex(intOf(a[0]))) }),
		"InterfaceByName":            h.fn("net.InterfaceByName", func(a []any) (any, error) { return retErr2(net.InterfaceByName(str(a[0]))) }),
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

func netFileOf(v any) *os.File {
	// the script-visible os.Stdout is a facade: unwrap to the real file.
	if s, ok := v.(*engineStdout); ok {
		return s.File
	}
	t, _ := v.(*os.File)
	return t
}

func addrPortOf(v any) netip.AddrPort { t, _ := v.(netip.AddrPort); return t }
