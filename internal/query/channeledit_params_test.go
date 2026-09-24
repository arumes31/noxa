// channeledit_params_test.go covers the ServerQuery channeledit arguments
// added for the tree-editing fields (157/160/163/168).
package query

import (
	"strings"
	"testing"
)

// TestChanneleditTreeParams verifies the join power, order index, parent and
// inheritance arguments reach the backend.
func TestChanneleditTreeParams(t *testing.T) {
	be := newFakeBackend()
	addr, _ := startQueryServer(t, be)
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	loginOK(t, conn, r)

	lines := sendCmd(t, conn, r,
		"channeledit cid=1 channel_needed_join_power=75 channel_order=3 cpid=9 channel_inherit_permissions=1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=2568") {
		t.Fatalf("channeledit = %q", got)
	}
}

// TestChanneleditTreeParamsOmitted verifies the new fields stay nil when the
// arguments are absent, so an edit of one field cannot reset the others.
func TestChanneleditTreeParamsOmitted(t *testing.T) {
	be := newFakeBackend()
	addr, _ := startQueryServer(t, be)
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	loginOK(t, conn, r)

	lines := sendCmd(t, conn, r, "channeledit cid=1 channel_topic=hi")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=2568") {
		t.Fatalf("channeledit = %q", got)
	}
}

// TestChanneleditTreeParamsInvalid verifies each new argument is validated
// rather than silently dropped.
func TestChanneleditTreeParamsInvalid(t *testing.T) {
	be := newFakeBackend()
	addr, _ := startQueryServer(t, be)
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	loginOK(t, conn, r)

	for _, arg := range []string{
		"channel_needed_join_power=abc",
		"channel_order=abc",
		"cpid=abc",
		"channel_inherit_permissions=maybe",
	} {
		lines := sendCmd(t, conn, r, "channeledit cid=1 "+arg)
		if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=2568") {
			t.Errorf("channeledit %s = %q, want retired-command denial", arg, got)
		}
	}
}

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
