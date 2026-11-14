package main

import "testing"

func TestExternalURLOnlyOpensWebLinks(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://example.com/a?b=c#d": true,
		"HTTP://example.com":          true,
		"http://[::1]:8080/":          true,
		"file:///C:/Windows/calc.exe": false,
		`\\host\share\x.exe`:          false,
		"ms-settings:":                false,
		"javascript:alert(1)":         false,
		"https:///nohost":             false,
		"https:example.com":           false,
		"//example.com":               false,
		"https://example.com/\x00":    false,
		"":                            false,
	} {
		if _, ok := externalURL(raw); ok != want {
			t.Errorf("externalURL(%q) = %v, want %v", raw, ok, want)
		}
	}
	if (&App{}).OpenExternalURL("file:///etc/passwd") == "" {
		t.Fatal("refused scheme reported as opened")
	}
}
