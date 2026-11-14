//go:build !windows

package main

func updateBrowserArguments() string { return "" }
func allowUpdateForeground(int)      {}
func prepareIncomingHandover()       {}
