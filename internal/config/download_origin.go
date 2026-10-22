package config

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ValidateHTTPSOrigin accepts only a fixed HTTPS authority without userinfo,
// paths, queries or fragments. Errors deliberately omit operator input.
func ValidateHTTPSOrigin(value string) (string, error) {
	invalid := errors.New("download_public_origin must be an HTTPS origin without credentials, path, query or fragment")
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsAny(value, "\\#? \t\r\n") || strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return "", invalid
		}
	} else {
		if len(host) > 253 {
			return "", invalid
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", invalid
			}
			for _, c := range label {
				allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-'
				if !allowed {
					return "", invalid
				}
			}
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", invalid
		}
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// HealthProxyNetworks returns validated immediate proxy networks. Invalid
// entries are ignored defensively; Config.Validate rejects them at startup.
func (c *Config) HealthProxyNetworks() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range c.HealthTrustedProxies {
		p, err := netip.ParsePrefix(raw)
		if err == nil && p.Bits() > 0 && !p.Addr().Is4In6() && p == p.Masked() {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes
}
