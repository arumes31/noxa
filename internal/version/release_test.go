package version

import "testing"

func TestNextRelease(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		tags       []string
		want       string
	}{
		{"initial", "0.4.3", nil, "0.4.3"},
		{"existing stable", "0.4.3", []string{"v0.4.1", "v0.4.2", "v0.4.0-main.102"}, "0.4.3"},
		{"next patch", "0.4.3", []string{"v0.4.3"}, "0.4.4"},
		{"numeric ordering", "0.4.3", []string{"v0.4.10", "v0.4.9", "v0.4.2"}, "0.4.11"},
		{"ignore previews", "0.4.3", []string{"v0.4.99-rc.1", "v0.4.3-main.200", "unrelated", "vbad"}, "0.4.3"},
		{"new minor", "0.5.0", []string{"v0.4.20"}, "0.5.0"},
		{"newer release line", "0.4.3", []string{"v0.5.0"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextRelease(tc.base, tc.tags)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected invalid release sequence to fail")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("next release = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
