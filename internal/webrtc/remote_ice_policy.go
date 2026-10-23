package webrtc

import (
	"fmt"
	"net"
	"net/netip"
)

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
	return func(ip net.IP) bool {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
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
		if addr.IsLoopback() && loopback {
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
		return addr.IsGlobalUnicast()
	}, nil
}
