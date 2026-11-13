// channeledit_params_test.go keeps the retired ServerQuery channeledit
// tree-editing arguments (157/160/163/168) out of the advertised help. The
// command itself is denied by TestRoleQueryLegacyOperationInventoryHasNoFallback.
package query

import (
	"strings"
	"testing"
)

// TestHelpListsChanneleditTreeParams keeps the advertised usage in step with
// what the command actually parses.
func TestHelpListsChanneleditTreeParams(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	lines := sendCmd(t, conn, r, "help")
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"channel_needed_join_power", "channel_order", "cpid",
		"channel_inherit_permissions",
	} {
		if strings.Contains(joined, want) {
			t.Errorf("help still advertises retired argument %q", want)
		}
	}
}
