package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

// DefaultWebAddr is the mobile web listener. TCP services may reuse numbers in
// the TURN relay subrange (see ports.go); production puts it behind :443.
const DefaultWebAddr = ":12342"

// WebConfig configures the mobile web app listener: the PWA bundle, the
// WebSocket control transport and the device-link, passkey and push routes.
type WebConfig struct {
	// Enabled starts the listener. Mobile devices cannot link without it.
	Enabled bool   `mapstructure:"enabled"`
	Addr    string `mapstructure:"addr"`
	// PublicURL is the HTTPS origin phones open, e.g. https://chat.example.
	// Its host is the passkey relying-party ID, so it must be a domain name.
	PublicURL string `mapstructure:"public_url"`
	// TLS is terminated natively with CertFile/KeyFile, natively through ACME,
	// or by a reverse proxy listed in TrustedProxies.
	CertFile       string   `mapstructure:"cert_file"`
	KeyFile        string   `mapstructure:"key_file"`
	ACME           bool     `mapstructure:"acme"`
	ACMEEmail      string   `mapstructure:"acme_email"`
	ACMECacheDir   string   `mapstructure:"acme_cache_dir"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	// AllowedOrigins extends PublicURL for /ws origin checks.
	AllowedOrigins []string `mapstructure:"allowed_origins"`
	// AppDir holds the built PWA bundle served under /app/.
	AppDir string `mapstructure:"app_dir"`
	// MaxDevicesPerAccount bounds linked devices, including the first desktop.
	MaxDevicesPerAccount int `mapstructure:"max_devices_per_account"`
	// WebSocket admission per client IP.
	WSConnectionsPerIP  int `mapstructure:"ws_connections_per_ip"`
	WSAttemptsPerMinute int `mapstructure:"ws_attempts_per_minute"`
	// Web Push. Push needs outbound HTTPS to the browsers' push services.
	PushEnabled bool `mapstructure:"push_enabled"`
	// PushVAPIDKeyFile holds the server's push signing key, created on start.
	PushVAPIDKeyFile string `mapstructure:"push_vapid_key_file"`
	// PushSubject is the contact push services see (mailto: or https:);
	// empty uses PublicURL.
	PushSubject string `mapstructure:"push_subject"`
}

func setWebDefaults(v *viper.Viper) {
	v.SetDefault("web.enabled", false)
	v.SetDefault("web.addr", DefaultWebAddr)
	v.SetDefault("web.public_url", "")
	v.SetDefault("web.cert_file", "")
	v.SetDefault("web.key_file", "")
	v.SetDefault("web.acme", false)
	v.SetDefault("web.acme_email", "")
	v.SetDefault("web.acme_cache_dir", "./data/acme")
	v.SetDefault("web.trusted_proxies", []string{})
	v.SetDefault("web.allowed_origins", []string{})
	v.SetDefault("web.app_dir", "./web/app")
	v.SetDefault("web.max_devices_per_account", DefaultMaxDevicesPerAccount)
	v.SetDefault("web.ws_connections_per_ip", 8)
	v.SetDefault("web.ws_attempts_per_minute", 30)
	v.SetDefault("web.push_enabled", true)
	v.SetDefault("web.push_vapid_key_file", "./data/keys/vapid.pem")
	v.SetDefault("web.push_subject", "")
}

// PushContact returns the VAPID subject: PushSubject or the public origin.
func (w WebConfig) PushContact() string {
	if s := strings.TrimSpace(w.PushSubject); s != "" {
		return s
	}
	origin, err := w.PublicOrigin()
	if err != nil {
		return ""
	}
	return origin
}

// DefaultMaxDevicesPerAccount applies when max_devices_per_account is unset.
const DefaultMaxDevicesPerAccount = 10

// DeviceLimit returns the effective linked-device limit per account.
func (w WebConfig) DeviceLimit() int {
	if w.MaxDevicesPerAccount <= 0 {
		return DefaultMaxDevicesPerAccount
	}
	return w.MaxDevicesPerAccount
}

// validate reports configuration errors; a disabled listener only needs a
// valid device limit because linking also bounds desktop identities.
func (w WebConfig) validate() []error {
	var errs []error
	if w.MaxDevicesPerAccount < 0 || w.MaxDevicesPerAccount > 100 {
		errs = append(errs, fmt.Errorf("web.max_devices_per_account %d must be between 1 and 100", w.MaxDevicesPerAccount))
	}
	if !w.Enabled {
		return errs
	}
	if err := validateAddress("web.addr", w.Addr); err != nil {
		errs = append(errs, err)
	}
	if _, err := w.PublicOrigin(); err != nil {
		errs = append(errs, err)
	}
	for _, raw := range w.AllowedOrigins {
		if _, err := ValidateHTTPSOrigin(raw); err != nil {
			errs = append(errs, errors.New("web.allowed_origins must contain HTTPS origins without credentials, path, query or fragment"))
			break
		}
	}
	fileTLS := w.CertFile != "" || w.KeyFile != ""
	if fileTLS && (w.CertFile == "" || w.KeyFile == "") {
		errs = append(errs, errors.New("web.cert_file and web.key_file must be set together"))
	}
	if fileTLS && w.ACME {
		errs = append(errs, errors.New("web.acme excludes web.cert_file and web.key_file"))
	}
	if w.ACME && strings.TrimSpace(w.ACMECacheDir) == "" {
		errs = append(errs, errors.New("web.acme_cache_dir must not be empty when web.acme=true"))
	}
	if !fileTLS && !w.ACME && len(w.TrustedProxies) == 0 {
		errs = append(errs, errors.New("web requires cert_file/key_file, acme, or trusted_proxies for a TLS-terminating proxy"))
	}
	for _, raw := range w.TrustedProxies {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			errs = append(errs, errors.New("web.trusted_proxies must contain canonical, specific IPv4/IPv6 CIDRs (not /0 or mapped IPv6)"))
			break
		}
	}
	if w.WSConnectionsPerIP < 1 {
		errs = append(errs, errors.New("web.ws_connections_per_ip must be positive"))
	}
	if w.WSAttemptsPerMinute < 1 {
		errs = append(errs, errors.New("web.ws_attempts_per_minute must be positive"))
	}
	if w.PushEnabled {
		if strings.TrimSpace(w.PushVAPIDKeyFile) == "" {
			errs = append(errs, errors.New("web.push_vapid_key_file must not be empty when web.push_enabled=true"))
		}
		if s := strings.TrimSpace(w.PushSubject); s != "" && !strings.HasPrefix(s, "mailto:") && !strings.HasPrefix(s, "https://") {
			errs = append(errs, errors.New("web.push_subject must be a mailto: or https: URL"))
		}
	}
	return errs
}

// PublicOrigin returns the canonical HTTPS origin of PublicURL. Passkeys bind
// to its host, so IP literals and single-label names are rejected.
func (w WebConfig) PublicOrigin() (string, error) {
	invalid := errors.New("web.public_url must be an HTTPS origin on a domain name, without credentials, path, query or fragment")
	origin, err := ValidateHTTPSOrigin(w.PublicURL)
	if err != nil {
		return "", invalid
	}
	u, _ := url.Parse(origin)
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err == nil || !strings.Contains(host, ".") {
		return "", invalid
	}
	return origin, nil
}

// RPID is the WebAuthn relying-party ID: the public host without port.
func (w WebConfig) RPID() string {
	origin, err := w.PublicOrigin()
	if err != nil {
		return ""
	}
	u, _ := url.Parse(origin)
	return u.Hostname()
}

// Origins returns every origin accepted on /ws: PublicURL plus extras.
func (w WebConfig) Origins() []string {
	var origins []string
	if origin, err := w.PublicOrigin(); err == nil {
		origins = append(origins, origin)
	}
	for _, raw := range w.AllowedOrigins {
		if origin, err := ValidateHTTPSOrigin(raw); err == nil {
			origins = append(origins, origin)
		}
	}
	return origins
}

// ProxyNetworks returns validated immediate proxy networks for the web
// listener. Invalid entries are ignored; Validate rejects them at startup.
func (w WebConfig) ProxyNetworks() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range w.TrustedProxies {
		p, err := netip.ParsePrefix(raw)
		if err == nil && p.Bits() > 0 && !p.Addr().Is4In6() && p == p.Masked() {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes
}
