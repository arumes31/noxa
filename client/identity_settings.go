package main

import (
	"errors"
	"fmt"
)

// ApplyIdentityProtection rewrites existing identities using the committed
// preference. Each credential is replaced atomically; failures are reported
// without discarding the original file or hiding partial completion.
func (a *App) ApplyIdentityProtection() error {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	a.settingsMu.Lock()
	wanted := a.settings.IdentityKeyProtection != "off"
	a.settingsMu.Unlock()
	dir, err := identitiesDir()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range identityIDs(dir) {
		path, pathErr := identityPathFor(id)
		if pathErr != nil {
			failures = append(failures, pathErr)
			continue
		}
		loaded, loadErr := loadIdentityAtStrict(path)
		if loadErr != nil {
			failures = append(failures, fmt.Errorf("identity %s: %w", id, loadErr))
			continue
		}
		if writeErr := saveIdentityWithProtection(path, loaded, wanted, true); writeErr != nil {
			failures = append(failures, fmt.Errorf("identity %s: %w", id, writeErr))
		}
	}
	return errors.Join(failures...)
}

// CancelIdentityLevel stops the current search for this identity. Its best
// result is retained, so cancellation does not throw away completed work.
func (a *App) CancelIdentityLevel(id string) bool {
	a.identityLevelMu.Lock()
	defer a.identityLevelMu.Unlock()
	if a.identityLevelID != id || a.identityLevelCancel == nil {
		return false
	}
	a.identityLevelCancel()
	return true
}

// ResetIdentity replaces exactly the key shown in the confirmation dialog.
// A stale selection must never reset another identity or a newer key.
func (a *App) ResetIdentity(id, expectedUID string) error {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	path, err := identityPathFor(id)
	if err != nil {
		return err
	}
	loaded, err := loadIdentityAtStrict(path)
	if err != nil {
		return err
	}
	uid, err := loaded.uniqueID()
	if err != nil {
		return err
	}
	if uid != expectedUID {
		return errors.New("identity changed; refresh before resetting")
	}
	replacement, err := newIdentity(loaded.Name)
	if err != nil {
		return err
	}
	if err := saveIdentityAt(path, replacement); err != nil {
		return err
	}
	a.invalidateIdentityContexts()
	return nil
}
