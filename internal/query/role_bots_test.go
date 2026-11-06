package query

import (
	"slices"
	"strings"
	"testing"

	"noxa/internal/auth"
)

// Every protected command in the help text needs a scope, so a scoped
// integration can never reach a command the ceiling does not name.
func TestEveryProtectedCommandHasAScope(t *testing.T) {
	unprotected := []string{"login", "logout", "help", "version", "quit", "SSH:", "Other"}
	for _, line := range strings.Split(roleHelpText, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.Contains(fields[0], "=") || slices.Contains(unprotected, fields[0]) {
			continue
		}
		scope, ok := CommandScope(fields[0])
		if !ok || !slices.Contains(auth.IntegrationScopes, scope) {
			t.Errorf("command %q has no valid scope (%q)", fields[0], scope)
		}
	}
	for name, scope := range commandScopes {
		if !slices.Contains(auth.IntegrationScopes, scope) {
			t.Errorf("command %q maps to unknown scope %q", name, scope)
		}
		if !strings.Contains(roleHelpText, "\n"+name) && !strings.HasPrefix(roleHelpText, name) {
			t.Errorf("scoped command %q is not documented in help", name)
		}
	}
}

func TestUnscopedIntegrationKeepsEveryCommand(t *testing.T) {
	for name := range commandScopes {
		if !commandAllowedByScope(auth.IntegrationPrincipal{}, name) {
			t.Fatalf("legacy integration lost %q", name)
		}
	}
}
