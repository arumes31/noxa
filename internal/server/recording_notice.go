package server

import (
	"context"
	"strconv"
	"time"
)

type recordingNotice struct {
	stopActor string
	actor     string
	done      <-chan struct{}
}

// Caller holds roleRecordingMu. Lifecycle completion covers explicit stops,
// revocation, deletion, capture errors and shutdown, without reentering policy.
func (s *TCPServer) startRecordingNotice(channelID int64, actor string, done <-chan struct{}) {
	s.finishRecordingNotice(channelID, nil)
	notice := &recordingNotice{actor: actor, done: done}
	s.recordingNotices.Store(channelID, notice)
	s.publishRecordingNotice(channelID, notice.actor, true)
	if done != nil {
		if s.recordingNoticeStop == nil {
			s.recordingNoticeStop = make(chan struct{})
		}
		stop := s.recordingNoticeStop
		s.recordingNoticeWG.Add(1)
		go func() {
			defer s.recordingNoticeWG.Done()
			select {
			case <-done:
			case <-stop:
				select {
				case <-done:
				default:
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					s.auditInChannels(ctx, "system", "recording_cleanup_incomplete", strconv.FormatInt(channelID, 10), "capture completion was not confirmed at shutdown", channelID)
					return
				}
			}
			s.roleRecordingMu.Lock()
			defer s.roleRecordingMu.Unlock()
			s.finishRecordingNotice(channelID, notice)
		}()
	}
}

// A completion for an older capture must never clear a restarted capture.
// Caller holds roleRecordingMu. Explicit stop may finalize before its watcher.
func (s *TCPServer) finishRecordingNotice(channelID int64, expected *recordingNotice) {
	value, ok := s.recordingNotices.Load(channelID)
	if !ok || expected != nil && value != expected {
		return
	}
	notice := value.(*recordingNotice)
	s.recordingNotices.Delete(channelID)
	s.roleRecordingOwners.Delete(channelID)
	s.roleRevokedRecordings.Delete(channelID)
	actor := notice.stopActor
	if actor == "" {
		actor = "system"
	}
	s.publishRecordingNotice(channelID, actor, false)
}

func (s *TCPServer) publishRecordingNotice(channelID int64, actor string, active bool) {
	if s.deps == nil || s.deps.State == nil {
		return
	}
	s.deps.State.SetChannelRecording(channelID, active)
	action := "recording_stop"
	if active {
		action = "recording_start"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.auditInChannels(ctx, actor, action, strconv.FormatInt(channelID, 10), "", channelID)
	// The existing channel update path emits a fresh, permission-filtered
	// snapshot at delivery. Late joiners also receive this runtime state.
	s.BroadcastChannelUpdated(channelID)
}
