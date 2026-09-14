package main

import "testing"

func TestResultRejectsIncompleteLoad(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		connects, auth, media            int64
		connectFail, authFail, mediaFail int64
		receivers                        int
		wantError                        bool
	}{
		{"success", 3, 3, 3, 0, 0, 0, 3, false},
		{"connect_failure", 2, 2, 2, 1, 0, 0, 2, true},
		{"observed_auth_failure", 3, 2, 2, 0, 1, 0, 2, true},
		{"webrtc_failure", 3, 3, 2, 0, 0, 1, 2, true},
		{"clients_never_started", 2, 2, 2, 0, 0, 0, 2, true},
		{"sender_only_evidence", 3, 3, 3, 0, 0, 0, 0, true},
		{"one_receiver_missing", 3, 3, 3, 0, 0, 0, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var st stats
			st.connectsOK.Store(tc.connects)
			st.authOK.Store(tc.auth)
			st.webrtcOK.Store(tc.media)
			st.connectsFail.Store(tc.connectFail)
			st.authFail.Store(tc.authFail)
			st.webrtcFail.Store(tc.mediaFail)
			st.rtpSent.Store(100)
			for i := 0; i < tc.receivers; i++ {
				st.recordReceivedRTP(i)
			}
			err := st.result(options{clients: 3, webrtc: true, channel: 1})
			if (err != nil) != tc.wantError {
				t.Fatalf("result = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func TestReceiverEvidenceCountsActualPacketsPerClient(t *testing.T) {
	var st stats
	st.recordReceivedRTP(0)
	st.recordReceivedRTP(0)
	st.recordReceivedRTP(1)
	if st.rtpRecv.Load() != 3 || st.rtpReceivers.Load() != 2 {
		t.Fatalf("received=%d receivers=%d", st.rtpRecv.Load(), st.rtpReceivers.Load())
	}
}

func TestResultRejectsPrematureControlSessionFailure(t *testing.T) {
	var st stats
	st.connectsOK.Store(1)
	st.authOK.Store(1)
	st.sessionFail.Store(1)
	if err := st.result(options{clients: 1}); err == nil {
		t.Fatal("a client that disconnected early passed the requested load")
	}
}
