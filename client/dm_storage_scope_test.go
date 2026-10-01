package main

import (
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestDMReadRetainsIdentityAcrossActivation(t *testing.T) {
	for _, operation := range []string{"load", "search", "export"} {
		t.Run(operation, func(t *testing.T) {
			_, first := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
			_, second := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
			a := newLocalApp(t, first)
			for _, seed := range []struct {
				cm   *connManager
				body string
			}{{first, "identity A"}, {second, "identity B"}} {
				a.cmStore(seed.cm)
				if err := a.DMHistoryAppend("peer", "Peer", DMEntry{Body: seed.body, SentAt: 1}); err != "" {
					t.Fatal(err)
				}
			}
			a.cmStore(first)
			oldHook := dmBeforeRead
			dmBeforeRead = func() { a.cmStore(second) }
			t.Cleanup(func() { dmBeforeRead = oldHook })
			switch operation {
			case "load":
				rows, err := a.DMHistoryLoad("peer")
				if err != nil || len(rows) != 1 || rows[0].Body != "identity A" {
					t.Fatalf("wrong identity history: %+v / %v", rows, err)
				}
			case "search":
				res, err := a.DMSearch("", "identity", 0)
				if err != nil || len(res.Messages) != 1 || res.Messages[0].Body != "identity A" {
					t.Fatalf("mixed identity search: %+v / %v", res, err)
				}
			case "export":
				res, err := a.DMExportHistory("peer")
				if err != nil || !strings.Contains(res.Text, "identity A") || strings.Contains(res.Text, "identity B") {
					t.Fatalf("wrong identity transcript: %+v / %v", res, err)
				}
			}
		})
	}
}

func TestDMCapturedStoreRetainsIdentityForWritesAndPeerScan(t *testing.T) {
	_, first := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	_, second := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	a := newLocalApp(t, first)
	store, err := a.captureDMHistory()
	if err != nil {
		t.Fatal(err)
	}
	a.cmStore(second)
	if err := a.DMHistoryAppend("peer", "B", DMEntry{Body: "identity B", SentAt: 2}); err != "" {
		t.Fatal(err)
	}
	if err := store.save(dmLog{Peer: "peer", Nickname: "A", Messages: []DMEntry{{Body: "identity A", SentAt: 1}}}); err != nil {
		t.Fatal(err)
	}
	peers := store.peers()
	if len(peers) != 1 || peers[0].Nickname != "A" || peers[0].LastAt != 1 {
		t.Fatalf("captured store enumerated another identity: %+v", peers)
	}
	rows, err := a.DMHistoryLoad("peer")
	if err != nil || len(rows) != 1 || rows[0].Body != "identity B" {
		t.Fatalf("captured write changed current identity: %+v / %v", rows, err)
	}
	a.cmStore(first)
	rows, err = a.DMHistoryLoad("peer")
	if err != nil || len(rows) != 1 || rows[0].Body != "identity A" {
		t.Fatalf("captured write lost original identity: %+v / %v", rows, err)
	}
}
