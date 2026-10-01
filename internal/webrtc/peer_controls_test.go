package webrtc

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPeerRebuildPreservesWhisperRoutingAndQuality(t *testing.T) {
	for _, channelTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "client target", true: "channel target"}[channelTarget], func(t *testing.T) {
			v := runtimeVoice(t)
			r := v.router
			for id, channel := range map[string]int64{"speaker": 1, "bystander": 1, "listener": 2} {
				v.JoinChannel(id, channel)
				establishVoiceSession(t, v, newClientPC(t), id)
			}
			if channelTarget {
				v.SetWhisper("speaker", nil, []int64{2}, true)
			} else {
				v.SetWhisper("speaker", []string{"listener"}, nil, true)
			}
			if err := v.SetVideoQuality("speaker", "low"); err != nil {
				t.Fatal(err)
			}
			for _, rebuilt := range []string{"speaker", "listener", "speaker"} {
				establishVoiceSession(t, v, newClientPC(t), rebuilt)
				if got := v.WhisperTargets("speaker"); !slices.Equal(got, []string{"listener"}) {
					t.Fatalf("after rebuilding %s: whisper targets = %v", rebuilt, got)
				}
				if pubTrackFor(r, "listener", "speaker") == nil {
					t.Fatalf("after rebuilding %s: cross-channel whisper track missing", rebuilt)
				}
				r.mu.RLock()
				quality := r.layerPrefLocked("speaker")
				r.mu.RUnlock()
				if quality != "q" {
					t.Fatalf("receive quality lost on rebuild: %q", quality)
				}
				var recipients []string
				var recipientsMu sync.Mutex
				r.SetMediaGuard(func(delivery MediaDelivery, write func() error) error {
					if !delivery.Whisper {
						t.Error("rebuild turned private audio into channel audio")
					}
					if delivery.RecipientID != "listener" {
						t.Errorf("private packet reached unexpected recipient %q", delivery.RecipientID)
					}
					recipientsMu.Lock()
					recipients = append(recipients, delivery.RecipientID)
					recipientsMu.Unlock()
					return write()
				})
				r.ForwardRTP("speaker", SlotMic, makeAudioPacket(t, 1, -1))
				recipientsMu.Lock()
				got := slices.Compact(slices.Clone(recipients))
				recipientsMu.Unlock()
				if !slices.Equal(got, []string{"listener"}) {
					t.Fatalf("private packet recipients = %v", got)
				}
			}
			v.SetWhisper("speaker", nil, nil, false)
			if pubTrackFor(r, "listener", "speaker") != nil {
				t.Fatal("stopping rebuilt whisper retained its cross-channel tracks")
			}
			if pubTrackFor(r, "bystander", "speaker") == nil {
				t.Fatal("stopping whisper removed ordinary channel tracks")
			}
			if err := v.ClosePeer("speaker"); err != nil {
				t.Fatal(err)
			}
			r.mu.RLock()
			defer r.mu.RUnlock()
			if r.whispers["speaker"] != nil || r.layerPrefs["speaker"] != "" {
				t.Fatal("final teardown retained session controls")
			}
		})
	}
}

func TestPeerRebuildUsesCurrentControlsAndPublisherGuard(t *testing.T) {
	v := runtimeVoice(t)
	r := v.router
	for id, channel := range map[string]int64{"speaker": 1, "old": 2, "current": 3} {
		v.JoinChannel(id, channel)
		establishVoiceSession(t, v, newClientPC(t), id)
	}
	v.SetWhisper("speaker", []string{"old"}, nil, true)
	r.DetachPeerKeepChannel("speaker")
	if err := v.engine.ClosePeerConnection("speaker"); err != nil {
		t.Fatal(err)
	}
	// Commands and movement between detach and attachment must not be rolled
	// back by restoring a pre-rebuild snapshot.
	v.JoinChannel("speaker", 4)
	v.SetWhisper("speaker", []string{"current"}, nil, true)
	if err := v.SetVideoQuality("speaker", "mid"); err != nil {
		t.Fatal(err)
	}
	r.SetPublisherGuard(func(pair PublisherAccess) bool { return pair.SubscriberID != "current" })
	attachFakePeer(t, v.engine, r, "speaker")
	r.EnsurePublishers("speaker")
	r.PrepareSubscriber("speaker")
	if got := v.WhisperTargets("speaker"); !slices.Equal(got, []string{"current"}) {
		t.Fatalf("current whisper overwritten: %v", got)
	}
	if pubTrackFor(r, "old", "speaker") != nil || pubTrackFor(r, "current", "speaker") != nil {
		t.Fatal("rebuild exposed obsolete or forbidden whisper metadata")
	}
	r.mu.RLock()
	channel, quality := r.clientChan["speaker"], r.layerPrefLocked("speaker")
	r.mu.RUnlock()
	if channel != 4 || quality != "h" {
		t.Fatalf("current membership/quality overwritten: %d %q", channel, quality)
	}
	r.SetPublisherGuard(func(PublisherAccess) bool { return true })
	r.EnsurePublishers("speaker")
	if pubTrackFor(r, "current", "speaker") == nil {
		t.Fatal("allowed current whisper did not recover")
	}
	// Retaining user intent is not retaining authorization: each packet must
	// still pass the current media guard after a rebuild.
	calls := 0
	r.SetMediaGuard(func(delivery MediaDelivery, _ func() error) error {
		calls++
		if !delivery.Whisper || delivery.ChannelID != 4 || delivery.RecipientChannelID != 3 {
			t.Errorf("incorrect current delivery: %+v", delivery)
		}
		return nil
	})
	if sent := r.ForwardRTP("speaker", SlotMic, makeAudioPacket(t, 1, -1)); sent != 0 || calls != 1 {
		t.Fatalf("revoked packet bypassed guard: writes=%d checks=%d", sent, calls)
	}
}

func TestPeerRebuildRenegotiatesWhisperSubscriber(t *testing.T) {
	v := runtimeVoice(t)
	clock := newManualRenegClock(time.Now())
	useManualRenegClock(v, clock, time.Millisecond, time.Millisecond)
	v.JoinChannel("speaker", 1)
	v.JoinChannel("listener", 2)
	establishVoiceSession(t, v, newClientPC(t), "speaker")
	establishVoiceSession(t, v, newClientPC(t), "listener")
	v.SetWhisper("speaker", []string{"listener"}, nil, true)
	// Discard setup scheduling so only this rebuild can supply the offer.
	v.stopReneg("speaker")
	v.stopReneg("listener")
	rec := &offerRecorder{}
	v.SetOfferSender(func(id, sdp string) error {
		if id == "listener" {
			rec.add(sdp)
		}
		return nil
	})
	establishVoiceSession(t, v, newClientPC(t), "speaker")
	clock.Advance(time.Millisecond)
	if rec.count() != 1 || !strings.Contains(rec.sdp(0), "noxa-speaker") {
		t.Fatal("existing whisper listener did not receive restored publisher SDP")
	}
}

func TestPeerFailedRebuildRetainsPrivateIntentForRetry(t *testing.T) {
	v := runtimeVoice(t)
	v.JoinChannel("speaker", 1)
	v.JoinChannel("listener", 2)
	establishVoiceSession(t, v, newClientPC(t), "speaker")
	v.SetWhisper("speaker", []string{"listener"}, nil, true)
	if err := v.SetVideoQuality("speaker", "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.HandleOffer("speaker", "invalid SDP", nil); err == nil {
		t.Fatal("invalid SDP accepted")
	}
	establishVoiceSession(t, v, newClientPC(t), "speaker")
	if got := v.WhisperTargets("speaker"); !slices.Equal(got, []string{"listener"}) {
		t.Fatalf("failed rebuild reset whisper: %v", got)
	}
}
