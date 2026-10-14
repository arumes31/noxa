package main

import "errors"

// SupportsSenderTrace reports whether this build includes the local native
// bridge. Runtime marker availability is checked by each explicit recording.
func (a *App) SupportsSenderTrace() bool { return supportsNativeSenderTrace() }

// ArmSenderTraceForTab issues a short-lived one-use capability. This does not
// start collection, send a server request or change media settings.
func (a *App) ArmSenderTraceForTab(tabID, scope string) (string, error) {
	return a.armSenderTraceForTab(tabID, scope, armNativeSenderTrace)
}

func (a *App) armSenderTraceForTab(tabID, scope string, arm func(string) (string, error)) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.conn == nil || cm.clientID == "" {
		return "", errors.New("not authenticated")
	}
	return arm(scope)
}
