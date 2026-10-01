package server

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type mediaControlFence struct {
	saved  []netproto.MediaControlSaved
	errors []netproto.Error
	err    error
}

func readMediaControlFence(conn net.Conn, accepted chan<- struct{}) mediaControlFence {
	var got mediaControlFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			got.err = err
			return got
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgMediaControlSaved:
			var saved netproto.MediaControlSaved
			got.err = netproto.Decode(f, &saved)
			got.saved = append(got.saved, saved)
			accepted <- struct{}{}
		case netproto.MsgError:
			var e netproto.Error
			got.err = netproto.Decode(f, &e)
			got.errors = append(got.errors, e)
		case netproto.MsgPong:
			return got
		}
		if got.err != nil {
			return got
		}
	}
}

func TestMediaControlConfirmationFollowsEffect(t *testing.T) {
	for _, operation := range []netproto.MessageType{netproto.MsgPrioritySpeaker, netproto.MsgWhisperSet, netproto.MsgScreenShare, netproto.MsgVideoQuality} {
		for _, outcome := range []string{"saved", "disabled", "unrequested", "denied", "missing session", "backend failure"} {
			if outcome == "backend failure" && operation != netproto.MsgVideoQuality {
				continue
			}
			t.Run(operation.String()+"/"+outcome, func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				voice := &fakeVoice{}
				if outcome == "backend failure" {
					voice.qualityErr = errors.New("quality rejected")
				}
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					d.Voice = voice
					d.Authority = authority
				})
				defer env.stop()
				env.state.AddChannel(testChannel(1))
				uid := "user-uid"
				if outcome == "denied" {
					uid = "admin-uid"
				}
				conn, id := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				if err := env.state.MoveClient(id, 1); err != nil {
					t.Fatal(err)
				}
				if outcome == "missing session" {
					env.state.RemoveClient(id)
				}
				ack, active := outcome != "unrequested", outcome != "disabled"
				want := netproto.MediaControlSaved{Operation: operation, ClientID: id, UniqueIDs: []string{}, ChannelIDs: []int64{}}
				var msg any
				switch operation {
				case netproto.MsgPrioritySpeaker:
					msg = netproto.PrioritySpeaker{Active: active, AckRequested: ack}
					want.Active = active
				case netproto.MsgWhisperSet:
					msg = netproto.WhisperSet{ChannelIDs: []int64{1}, Active: active, AckRequested: ack}
					want.Active, want.ChannelIDs = active, []int64{1}
				case netproto.MsgScreenShare:
					msg = netproto.ScreenShare{Active: active, MaxHeight: 720, AckRequested: ack}
					want.Active, want.MaxHeight = active, 720
				case netproto.MsgVideoQuality:
					msg = netproto.VideoQuality{Quality: "mid", AckRequested: ack}
					want.Quality = "mid"
				}
				denied := outcome == "denied" || outcome == "missing session" || outcome == "backend failure"
				held := !denied
				var once sync.Once
				unblock := func() {
					if held {
						once.Do(env.srv.roleMetadataMu.Unlock)
					}
				}
				if held {
					env.srv.roleMetadataMu.Lock()
				}
				defer unblock()
				send(t, conn, operation, msg)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				accepted, done := make(chan struct{}, 2), make(chan mediaControlFence, 1)
				go func() { done <- readMediaControlFence(conn, accepted) }()
				if held {
					select {
					case <-accepted:
						t.Fatal("confirmed before effect")
					case result := <-done:
						t.Fatalf("completed before effect: %+v", result)
					case <-time.After(25 * time.Millisecond):
					}
					unblock()
				}
				result := <-done
				if result.err != nil {
					t.Fatal(result.err)
				}
				switch {
				case denied:
					if len(result.errors) != 1 || len(result.saved) != 0 {
						t.Fatalf("denial: %+v", result)
					}
				case outcome == "unrequested":
					if len(result.errors) != 0 || len(result.saved) != 0 {
						t.Fatalf("unrequested reply: %+v", result)
					}
				case len(result.errors) != 0 || len(result.saved) != 1 || !reflect.DeepEqual(result.saved[0], want):
					t.Fatalf("result: %+v; want %+v", result, want)
				}
				if current, ok := env.state.GetClient(id); ok {
					if operation == netproto.MsgPrioritySpeaker && current.PrioritySpeaker != (!denied && active) {
						t.Fatalf("priority state: %+v", current)
					}
					if operation == netproto.MsgScreenShare && current.Sharing != (!denied && active) {
						t.Fatalf("sharing state: %+v", current)
					}
				}
				voice.mu.Lock()
				defer voice.mu.Unlock()
				if operation == netproto.MsgWhisperSet {
					if denied && len(voice.whispers) != 0 || !denied && (len(voice.whispers) != 1 || voice.whispers[0].active != active) {
						t.Fatalf("whisper effects: %+v", voice.whispers)
					}
				}
				if operation == netproto.MsgVideoQuality && outcome != "backend failure" {
					if denied && len(voice.qualities) != 0 || !denied && !reflect.DeepEqual(voice.qualities, [][2]string{{id, "mid"}}) {
						t.Fatalf("quality effects: %+v", voice.qualities)
					}
				}
			})
		}
	}
}
