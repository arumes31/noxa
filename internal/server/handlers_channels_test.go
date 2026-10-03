package server

import (
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"noxa/internal/broadcast"
	"noxa/internal/state"
)

func TestBroadcastChannelUpdatedCarriesEditableFields(t *testing.T) {
	sm := state.New(zap.NewNop())
	bc := broadcast.New(zap.NewNop(), sm)
	defer bc.Close()
	out, err := bc.Register("channel-event-observer")
	if err != nil {
		t.Fatal(err)
	}
	srv := &TCPServer{logger: zap.NewNop(), deps: &Deps{State: sm, Broadcast: bc}}
	ch := testChannel(1)
	ch.Topic, ch.Description = "Topic", "Description"
	ch.MaxClients, ch.OpusBitrate = 17, 96000
	ch.OpusFEC, ch.OpusDTX, ch.OpusStereo = true, true, true
	ch.SlowModeSeconds, ch.OrderIndex, ch.ParentID = 3, 8, 2
	sm.AddChannel(ch)
	srv.BroadcastChannelUpdated(ch.ChannelID)
	var envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	select {
	case raw := <-out:
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("channel update was not published")
	}
	if envelope.Type != eventChannelUpdated {
		t.Fatalf("channel event type = %q", envelope.Type)
	}
	var got channelUpdatedEvent
	if err := json.Unmarshal(envelope.Data, &got); err != nil {
		t.Fatal(err)
	}
	want := channelUpdatedEvent{
		ChannelID: 1, Topic: "Topic", Description: "Description",
		MaxClients: 17, OpusBitrate: 96000, OpusFEC: true, OpusDTX: true, OpusStereo: true,
		SlowModeSeconds: 3, OrderIndex: 8, ParentID: 2,
	}
	if got != want {
		t.Fatalf("channel update = %+v, want %+v", got, want)
	}
}
