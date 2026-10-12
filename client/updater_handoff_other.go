//go:build !windows

package main

func runUpdateSupervisorMode() (bool, error)       { return false, nil }
func prepareLegacyUpdateBootstrap() error          { return nil }
func prepareUpdateFrontendHandoff(*App) error      { return nil }
func completeUpdateFrontendHandoff(*App)           {}
func (*App) UpdateStartupPending() bool            { return false }
func launchClientUpdateAfterExit(exe string) error { return launchUpdatedClient(exe) }
