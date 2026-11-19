package netip

import (
	"net/netip"

	"github.com/podhmo/minigo/internal/stdlib"
)

// Install binds net/netip: Addr/AddrPort/Prefix typedefs keep value
// constructors — literals mint the value, not a pointer — so they stay
// handwritten; the rest of the table is generated.
func Install(bind stdlib.Bind) {
	tbl := netNetipGenEntries()
	tbl["Addr"] = stdlib.HostType("net/netip.Addr", func() any { return netip.Addr{} })
	tbl["AddrPort"] = stdlib.HostType("net/netip.AddrPort", func() any { return netip.AddrPort{} })
	tbl["Prefix"] = stdlib.HostType("net/netip.Prefix", func() any { return netip.Prefix{} })
	bind("net/netip", tbl)
}
