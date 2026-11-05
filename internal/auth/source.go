package auth

import "net/netip"

// sourcePrefixBitsIPv6 is the IPv6 prefix treated as one source. A single
// host or subscriber is routinely delegated a whole /64, so keying limits by
// the full address would give it 2^64 fresh budgets.
const sourcePrefixBitsIPv6 = 64

// SourceAddressKey returns the key that per-source rate limits and admission
// counters use for a remote host: an IPv4 address (or IPv4-mapped IPv6
// address) unchanged, and an IPv6 address reduced to its /64 prefix. Input
// that is not an IP address is returned unchanged. Ban checks must keep using
// the exact address.
func SourceAddressKey(host string) string {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.WithZone("").Prefix(sourcePrefixBitsIPv6)
	if err != nil {
		return host
	}
	return prefix.String()
}
