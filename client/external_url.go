package main

import (
	"net/url"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// OpenExternalURL opens a link in the system browser. It is the frontend's
// only way to open links, so webview content can never hand the shell a
// file:, UNC or custom-scheme target. An empty result means it was opened.
func (a *App) OpenExternalURL(raw string) string {
	target, ok := externalURL(raw)
	if !ok {
		return "only http and https links can be opened"
	}
	if a.ctx == nil {
		return "application window is not available"
	}
	wailsRuntime.BrowserOpenURL(a.ctx, target)
	return ""
}

// externalURL accepts only absolute http(s) URLs with a host and returns the
// normalised form that is actually opened.
func externalURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return "", false
	}
	return u.String(), true
}
