package config

import (
	"errors"
	"net/netip"
)

func (c TURNConfig) validateTLSProxy() []error {
	if c.TLSProxyListenAddr == "" && len(c.TLSProxyTrustedIPs) == 0 {
		return nil
	}
	var errs []error
	if !c.Managed {
		errs = append(errs, errors.New("turn TLS proxy requires managed TURN"))
	}
	endpoint, err := netip.ParseAddrPort(c.TLSProxyListenAddr)
	if err != nil || !endpoint.Addr().Is4() || endpoint.Port() == 0 || (!endpoint.Addr().IsPrivate() && !endpoint.Addr().IsLoopback()) {
		errs = append(errs, errors.New("turn.tls_proxy_listen_addr requires an explicit private or loopback IPv4:port"))
	}
	if c.TLSProxyListenAddr == c.ListenAddr {
		errs = append(errs, errors.New("TURN proxy and public listeners must differ"))
	}
	if len(c.TLSProxyTrustedIPs) == 0 {
		errs = append(errs, errors.New("turn.tls_proxy_trusted_ips must name the TLS proxy peers"))
	}
	for _, value := range c.TLSProxyTrustedIPs {
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is4() || (!ip.IsPrivate() && !ip.IsLoopback()) {
			errs = append(errs, errors.New("turn.tls_proxy_trusted_ips requires exact private or loopback IPv4 addresses"))
		}
	}
	return errs
}
