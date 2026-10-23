package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/state"
	"testing"
	"time"
)

func TestRecordingNoticeLifecycleAndRestart(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	s := env.srv
	first, second := make(chan struct{}), make(chan struct{})
	s.roleRecordingMu.Lock()
	s.startRecordingNotice(1, "operator", first)
	old, _ := s.recordingNotices.Load(int64(1))
	s.roleRecordingMu.Unlock()
	if ch, _ := env.state.GetChannel(1); !ch.Recording {
		t.Fatal("capture not advertised")
	}
	s.roleRecordingMu.Lock()
	s.startRecordingNotice(1, "operator", second)
	s.finishRecordingNotice(1, old.(*recordingNotice))
	s.roleRecordingMu.Unlock()
	close(first)
	if ch, _ := env.state.GetChannel(1); !ch.Recording {
		t.Fatal("old completion cleared replacement")
	}
	close(second)
	waitFor(t, "recording completion", func() bool { ch, _ := env.state.GetChannel(1); return !ch.Recording })
	s.recordingNoticeWG.Wait()
	rows, err := env.groups.AuditList(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	starts, stops := 0, 0
	for _, row := range rows {
		if row.Action == "recording_start" {
			starts++
			if row.Actor != "operator" {
				t.Fatal(row)
			}
		}
		if row.Action == "recording_stop" {
			stops++
		}
	}
	if starts != 2 || stops != 2 {
		t.Fatalf("audit starts/stops = %d/%d", starts, stops)
	}
}

func TestRecordingSnapshotHiddenAndLateJoin(t *testing.T) {
	sm := state.New(testLogger())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "private-recording", Recording: true})
	p := serverRoleFixture().policy
	p.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	p.Channels = []authorization.ChannelPolicy{{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := buildRoleSnapshot(sm, e, 1, "viewer"); len(got.RootChannels) != 0 {
		t.Fatal("hidden capture disclosed")
	}
	got := buildRoleSnapshot(sm, e, 2, "owner")
	if len(got.RootChannels) != 1 || !got.RootChannels[0].Recording {
		t.Fatal("late joiner missed active capture")
	}
	sm.SetChannelRecording(1, false)
	if got := buildRoleSnapshot(sm, e, 2, "owner"); got.RootChannels[0].Recording {
		t.Fatal("completed capture still active")
	}
}

func TestRecordingShutdownDoesNotWaitForUnconfirmedCapture(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	env.srv.roleRecordingMu.Lock()
	env.srv.startRecordingNotice(1, "operator", make(chan struct{}))
	env.srv.roleRecordingMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- env.srv.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited forever for capture completion")
	}
	rows, err := env.groups.AuditList(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	pending := false
	for _, row := range rows {
		if row.Action == "recording_stop" {
			t.Fatal("unconfirmed capture falsely reported stopped")
		}
		if row.Action == "recording_cleanup_incomplete" {
			pending = true
		}
	}
	if !pending {
		t.Fatal("incomplete cleanup omitted from audit")
	}
}
