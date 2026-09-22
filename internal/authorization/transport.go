package authorization

import "errors"

// ErrIncompatibleAdministration rejects legacy administration APIs that cannot
// carry a role principal or enforce the current policy for every operation.
var ErrIncompatibleAdministration = errors.New("this administration API does not support roles-v1; use a compatible client")
