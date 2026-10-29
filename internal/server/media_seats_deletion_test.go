package server

import (
	"fmt"
	"testing"

	"noxa/internal/channels"
	"noxa/internal/state"
)

// Review 2b P3-1 (repro adopted): ApplyChannelDeletion runs after the channel manager released
// its lock (and after the channel_deleted broadcast). A member that already
// joined another channel in that window has its NEW seat parked, because park
// is not scoped to the deleted channel.
func TestMediaSeatsDeletionParksOnlyTheDeletedChannel(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	env.state.AddChannel(testChannel(5))
	env.state.AddClient(&state.Client{ClientID: "m9", UniqueID: "u9", MediaDevice: "d9"})
	seatForTest(t, env.srv, 9, "m9", "u9", "d9")
	// Deletion of 9 committed (lock released); m9 joins channel 5 before the
	// deletion sink runs: its seat is carried into 5.
	if err := env.state.MoveClient("m9", 5); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := env.srv.mediaSeats.carry(5, "m9", nil, MediaE2EEMaxVoice, nil); !ok {
		t.Fatal("carry")
	}
	env.srv.ApplyChannelDeletion(channels.DeleteResult{RootID: 9, ChannelIDs: []int64{9}, Members: []channels.DeletedMember{{ClientID: "m9", ChannelID: 9}}}, "deleted")
	admitted := 0
	for i := 0; i < MediaE2EEMaxVoice; i++ {
		id := fmt.Sprintf("n-%d", i)
		if _, _, ok := env.srv.mediaSeats.take(5, id, mediaSeatID{id, "dev-" + id}, MediaE2EEMaxVoice, nil); ok {
			admitted++
		}
	}
	c, _ := env.state.GetClient("m9")
	t.Logf("m9 in channel %d media_device=%q seat=%d; newcomers admitted to 5: %d (cap %d) -> %d voice members with a media device",
		c.ChannelID, c.MediaDevice, env.srv.mediaSeats.seat("m9"), admitted, MediaE2EEMaxVoice, admitted+1)
	if admitted+1 > MediaE2EEMaxVoice {
		t.Errorf("cap exceeded")
	}
}

func TestMediaSeatsParkFromIgnoresAnotherChannel(t *testing.T) {
	var m mediaSeats
	m.take(5, "a", mediaSeatID{"u", "d"}, 64, nil)
	m.parkFrom("a", 9)
	if m.seat("a") != 5 {
		t.Fatalf("seat in 5 parked by a deletion of 9: %d", m.seat("a"))
	}
	m.parkFrom("a", 5)
	if m.seat("a") != 0 || m.others(5, "") != 0 {
		t.Fatal("seat in the deleted channel not parked")
	}
}
