package auth

import "testing"

func TestSourceAddressKeyGroupsIPv6Prefix(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":                   "203.0.113.7",
		"::ffff:203.0.113.7":            "203.0.113.7",
		"2001:db8:1:2:aaaa::1":          "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:1": "2001:db8:1:2::/64",
		"fe80::1%eth0":                  "fe80::/64",
		"pipe":                          "pipe",
	}
	for in, want := range cases {
		if got := SourceAddressKey(in); got != want {
			t.Errorf("SourceAddressKey(%q) = %q, want %q", in, got, want)
		}
	}
	if SourceAddressKey("2001:db8:1:2::1") == SourceAddressKey("2001:db8:1:3::1") {
		t.Fatal("different /64 prefixes share a source key")
	}
}
