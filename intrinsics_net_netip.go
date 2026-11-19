package minigo

import (
	"net/netip"
)

// netip's Addr/AddrPort/Prefix typedefs keep value constructors —
// literals mint the value, not a pointer — so they stay handwritten;
// the rest of the table is generated.
func (e *Engine) installNetNetip(h *hostHelpers) {
	tbl := netNetipGenEntries()
	tbl["Addr"] = hostType("net/netip.Addr", func() any { return netip.Addr{} })
	tbl["AddrPort"] = hostType("net/netip.AddrPort", func() any { return netip.AddrPort{} })
	tbl["Prefix"] = hostType("net/netip.Prefix", func() any { return netip.Prefix{} })
	e.bindBound("net/netip", tbl)
}
