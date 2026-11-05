package webrtc

import (
	"fmt"
	"net"
	"net/netip"
)

// IPv6 prefixes that embed an IPv4 destination. Translators and relays
// deliver such packets to that IPv4 address, so a candidate in them is judged
// by the embedded address: otherwise 64:ff9b::7f00:1 or 2002:a00:1:: would
// reach loopback or private IPv4 hosts as "global unicast".
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour      = netip.MustParsePrefix("2002::/16")
	teredo         = netip.MustParsePrefix("2001::/32")
)

// embeddedIPv4 returns the IPv4 destination an IPv4-embedding IPv6 address
// reaches. A local-use NAT64 prefix has an operator-chosen layout, so it
// reports ok with an invalid address, which is refused.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	switch {
	case nat64WellKnown.Contains(addr):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case nat64Local.Contains(addr):
		return netip.Addr{}, true
	case sixToFour.Contains(addr):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	case teredo.Contains(addr):
		// The Teredo client address is stored inverted in the last 32 bits.
		return netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}), true
	}
	return netip.Addr{}, false
}

// remoteICEPolicy is applied inside Pion after parsing candidates, including
// inline SDP, trickle ICE and peer-reflexive discovery. Operator-configured
// STUN/TURN servers are not remote peer candidates and are unaffected.
func remoteICEPolicy(allowedCIDRs []string, loopback bool) (func(net.IP) bool, error) {
	allowed := make([]netip.Prefix, 0, len(allowedCIDRs))
	for _, raw := range allowedCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("webrtc: invalid allowed remote ICE CIDR %q", raw)
		}
		allowed = append(allowed, prefix.Masked())
	}
	shared := netip.MustParsePrefix("100.64.0.0/10")
	var permitted func(addr netip.Addr, embedded bool) bool
	permitted = func(addr netip.Addr, embedded bool) bool {
		if !addr.IsValid() {
			return false
		}
		addr = addr.Unmap()
		// Never permit metadata/link-local, multicast, unspecified or reserved
		// IPv4 class-E destinations, even through a broad operator exception.
		if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
			return false
		}
		if addr.Is4() {
			b := addr.As4()
			if b[0] == 0 || b[0] >= 240 {
				return false
			}
		}
		if addr.IsLoopback() && loopback && !embedded {
			return true
		}
		if addr.IsPrivate() || addr.IsLoopback() || shared.Contains(addr) {
			for _, prefix := range allowed {
				if prefix.Contains(addr) {
					return true
				}
			}
			return false
		}
		if addr.Is6() && !embedded {
			if inner, ok := embeddedIPv4(addr); ok {
				return permitted(inner, true)
			}
		}
		return addr.IsGlobalUnicast()
	}
	return func(ip net.IP) bool {
		addr, ok := netip.AddrFromSlice(ip)
		return ok && permitted(addr, false)
	}, nil
}
