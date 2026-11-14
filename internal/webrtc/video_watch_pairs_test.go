package webrtc

import (
	"slices"
	"testing"
)

func TestVideoWatchPairsListsActiveWatchesBothWays(t *testing.T) {
	r := NewRouter(nil)
	r.watches = map[watchKey]videoWatch{
		{subscriber: "me", publisher: "pub", slot: SlotScreen}:    {active: true},
		{subscriber: "me", publisher: "idle", slot: SlotCam}:      {active: false},
		{subscriber: "viewer", publisher: "me", slot: SlotCam}:    {active: true},
		{subscriber: "other", publisher: "pub", slot: SlotScreen}: {active: true},
	}
	watching, watchers := r.videoWatchPairs("me")
	if !slices.Equal(watching, [][2]string{{"pub", SlotScreen}}) || !slices.Equal(watchers, [][2]string{{"viewer", SlotCam}}) {
		t.Fatalf("watching %v watchers %v", watching, watchers)
	}
}
