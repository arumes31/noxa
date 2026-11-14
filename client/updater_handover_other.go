//go:build !windows

package main

import "context"

func updateBrowserArguments() string { return "" }
func allowUpdateForeground(int)      {}
func prepareIncomingHandover()       {}
func holdUpdateParent() func(context.Context) error {
	return func(context.Context) error { return nil }
}
