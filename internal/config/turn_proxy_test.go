package config

import "testing"

func TestTURNProxyConfiguration(t *testing.T) {
	valid := TURNConfig{Managed: true, ListenAddr: "0.0.0.0:12340", TLSProxyListenAddr: "10.12.0.3:12341", TLSProxyTrustedIPs: []string{"10.12.0.2"}}
	if errs := valid.validateTLSProxy(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := (TURNConfig{}).validateTLSProxy(); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, bind := range []string{"", ":12341", "0.0.0.0:12341", "8.8.8.8:12341", "proxy:12341", "10.12.0.3:0", "[::]:12341"} {
		c := valid
		c.TLSProxyListenAddr = bind
		if len(c.validateTLSProxy()) == 0 {
			t.Errorf("accepted bind %q", bind)
		}
	}
	for _, peers := range [][]string{nil, {"10.12.0.0/24"}, {"0.0.0.0"}, {"8.8.8.8"}, {"localhost"}, {"::ffff:10.12.0.2"}} {
		c := valid
		c.TLSProxyTrustedIPs = peers
		if len(c.validateTLSProxy()) == 0 {
			t.Errorf("accepted peers %v", peers)
		}
	}
	c := valid
	c.Managed = false
	if len(c.validateTLSProxy()) == 0 {
		t.Fatal("accepted unmanaged TLS backend")
	}
}
