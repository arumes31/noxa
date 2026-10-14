//go:build windows

package main

import "github.com/wailsapp/go-webview2/pkg/edge"

func supportsNativeSenderTrace() bool                   { return true }
func armNativeSenderTrace(scope string) (string, error) { return edge.ArmSenderTrace(scope) }
