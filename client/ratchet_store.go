package main

import (
	"noxa/internal/e2ee/durable"
)

// openDurableRatchets is the storage adapter for the next live DM migration.
// Windows requires user-bound DPAPI. Other platforms explicitly derive local
// protection from the existing identity secret; they do not gain an OS vault.
// Callers must authenticate the namespace and never open backups for sending.
func openDurableRatchets(dir string, namespace durable.Namespace, identitySecret []byte) (*durable.Store, error) {
	protection := durable.IdentityProtection(identitySecret)
	if keyProtectionAvailable() {
		protection = durable.Protection{Seal: protectBytes, Open: unprotectBytes}
	} else if keyProtectionRequired() {
		return nil, durable.ErrUnavailable
	}
	return durable.Open(dir, namespace, protection, 0)
}
