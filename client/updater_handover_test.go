package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// handoverChannel starts A's side of the signal channel for child PID pid.
func handoverChannel(t *testing.T) *updateReadiness {
	t.Helper()
	r, err := newUpdateReadiness()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.close)
	return r
}

func serveAsync(ctx context.Context, r *updateReadiness, pid int, exited <-chan error, committing *atomic.Int32) <-chan bool {
	result := make(chan bool, 1)
	go func() {
		result <- serveUpdateHandover(ctx, r, pid, exited, func(got int) {
			time.Sleep(50 * time.Millisecond) // the acknowledgement must wait for this
			if got == pid {
				committing.Add(1)
			}
		})
	}()
	return result
}

// wrongNonce flips one bit, so it always differs from nonce.
func wrongNonce(t *testing.T, nonce string) string {
	t.Helper()
	raw, err := hex.DecodeString(nonce)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 1
	return hex.EncodeToString(raw)
}

func TestUpdateHandoverCommitSilencesThenMoves(t *testing.T) {
	r := handoverChannel(t)
	var committing atomic.Int32
	result := serveAsync(t.Context(), r, 4242, nil, &committing)
	// Signals with a wrong challenge or process are ignored.
	if err := sendUpdateHandoverSignal(r.address(), wrongNonce(t, r.nonce), 4242, handoverCommitted); err == nil {
		t.Fatal("a wrong challenge was acknowledged")
	}
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 1, handoverCommitted); err == nil {
		t.Fatal("another process was acknowledged")
	}
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 4242, handoverCommitting); err != nil {
		t.Fatal(err)
	}
	if committing.Load() != 1 {
		t.Fatal("committing was acknowledged before the old process went silent")
	}
	for _, kind := range []byte{handoverCommitting, handoverCommitted} {
		if err := sendUpdateHandoverSignal(r.address(), r.nonce, 4242, kind); err != nil {
			t.Fatalf("signal %d: %v", kind, err)
		}
	}
	if !<-result {
		t.Fatal("a committed handover did not move the session")
	}
	if committing.Load() != 1 {
		t.Fatalf("old process silenced %d times, want once", committing.Load())
	}
}

func TestUpdateHandoverAbandonTimeoutAndExitKeepSession(t *testing.T) {
	var committing atomic.Int32
	r := handoverChannel(t)
	result := serveAsync(t.Context(), r, 7, nil, &committing)
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 7, handoverCommitting); err != nil {
		t.Fatal(err)
	}
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 7, handoverAbandoned); err != nil {
		t.Fatal(err)
	}
	if <-result {
		t.Fatal("an abandoned handover moved the session")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if <-serveAsync(ctx, handoverChannel(t), 7, nil, &committing) {
		t.Fatal("a silent child moved the session")
	}

	exited := make(chan error, 1)
	exited <- errors.New("crashed")
	if <-serveAsync(t.Context(), handoverChannel(t), 7, exited, &committing) {
		t.Fatal("an exited child moved the session")
	}
}

// A lost final signal after "committing" is no move: the old process restarts,
// which is safe because a commit keeps the session's token.
func TestUpdateHandoverLostConfirmationRestarts(t *testing.T) {
	var committing atomic.Int32
	r := handoverChannel(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	result := serveAsync(ctx, r, 9, nil, &committing)
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 9, handoverCommitting); err != nil {
		t.Fatal(err)
	}
	if <-result {
		t.Fatal("an unconfirmed commit counted as a move")
	}
}

func TestIncomingHandoverConfiguration(t *testing.T) {
	enableHandover(t)
	valid := `{"version":1,"previous":"C:\\noxa\\.noxa-previous-1.exe","old_hash":"` + string(make64('a')) + `","browser_args":"--x"}`
	env := map[string]string{updateHandoverEnv: valid, updateReadyAddressEnv: "127.0.0.1:1", updateReadyNonceEnv: "ab"}
	getenv := func(key string) string { return env[key] }
	h := loadIncomingHandover(getenv, true)
	if h == nil || h.config.BrowserArgs != "--x" || h.address != "127.0.0.1:1" || h.nonce != "ab" {
		t.Fatalf("handover = %+v", h)
	}
	if loadIncomingHandover(getenv, false) != nil {
		t.Fatal("an ordinary launch must never act as a handover")
	}
	for _, raw := range []string{"", "{", `{"version":2}`, `{"version":1,"old_hash":"zz"}`} {
		env[updateHandoverEnv] = raw
		if loadIncomingHandover(getenv, true) != nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func make64(c byte) []byte {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return b
}

func TestHandoverSilenceDefersAndReplaysDisconnect(t *testing.T) {
	emitted := func(r *eventRecorder) int {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.events)
	}
	replayed, dropped, other := &eventRecorder{}, &eventRecorder{}, &eventRecorder{}
	target := &connManager{sink: replayed}
	if deferHandoverDisconnect(target) {
		t.Fatal("deferred outside a handover")
	}
	beginHandoverSilence(target)
	if deferHandoverDisconnect(&connManager{sink: other}) {
		t.Fatal("another tab's disconnect was deferred")
	}
	if !deferHandoverDisconnect(target) {
		t.Fatal("not deferred during a commit")
	}
	endHandoverSilence(true)
	if emitted(replayed) != 1 {
		t.Fatal("an abandoned handover must replay the disconnect")
	}
	moved := &connManager{sink: dropped}
	beginHandoverSilence(moved)
	deferHandoverDisconnect(moved)
	endHandoverSilence(false)
	if emitted(dropped) != 0 {
		t.Fatal("a moved session must not announce its old connection's close")
	}
}

func enableHandover(t *testing.T) {
	t.Helper()
	original := updateHandoverSupported
	updateHandoverSupported = true
	t.Cleanup(func() { updateHandoverSupported = original })
}

func TestHandoverIsDisabledWithoutPlatformSupport(t *testing.T) {
	original := updateHandoverSupported
	updateHandoverSupported = false
	t.Cleanup(func() { updateHandoverSupported = original })
	env := map[string]string{updateHandoverEnv: `{"version":1,"old_hash":"` + string(make64('a')) + `"}`}
	if loadIncomingHandover(func(k string) string { return env[k] }, true) != nil {
		t.Fatal("a handover started hidden where the old process cannot be watched")
	}
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	cm := &connManager{conn: client, supportsHandover: true, resumeToken: "t"}
	if (&App{ctx: context.Background()}).updateHandoverPossible("saved", cm, "t") {
		t.Fatal("the old process offered a handover without platform support")
	}
}

func TestApplyAndRestartHandsOverOrFallsBack(t *testing.T) {
	enableHandover(t)
	originalLaunch, originalQuit, originalHandover := restartLaunch, wailsQuit, handOverSession
	t.Cleanup(func() { restartLaunch, wailsQuit, handOverSession = originalLaunch, originalQuit, originalHandover })
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	target := &connManager{conn: client, supportsHandover: true, resumeToken: "token"}
	app := &App{ctx: context.Background()}
	prepare := func() { app.updateReconnect, app.updateHandoverCM, app.updateHandoverToken = "saved", target, "token" }
	var order []string
	restartLaunch = func(string) error { order = append(order, "restart"); return nil }
	wailsQuit = func(context.Context) { order = append(order, "quit") }
	handover := func(moved, fallback bool, err error) {
		handOverSession = func(_ *App, _ string, got *connManager) (bool, bool, error) {
			if got != target {
				t.Error("the handover did not use the prepared session")
			}
			order = append(order, "handover")
			return moved, fallback, err
		}
	}

	order = nil
	prepare()
	handover(true, false, nil)
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"handover", "quit"}) {
		t.Fatalf("moved: %q order %v", got, order)
	}
	quitForUpdate.Store(false)

	order = nil
	prepare()
	handover(false, true, errors.New("abandoned"))
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"handover", "restart", "quit"}) {
		t.Fatalf("fallback: %q order %v", got, order)
	}
	quitForUpdate.Store(false)

	order = nil
	prepare()
	handover(false, false, errors.New("replacement still running"))
	if got := app.ApplyAndRestart(); got == "" || !slices.Equal(order, []string{"handover"}) {
		t.Fatalf("unstoppable child: %q order %v", got, order)
	}

	// A token that changed since preparation (or no server support) means
	// the restart path runs directly.
	order = nil
	prepare()
	app.updateHandoverToken = "stale"
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"restart", "quit"}) {
		t.Fatalf("stale token: %q order %v", got, order)
	}
	quitForUpdate.Store(false)
	_ = os.Unsetenv(updateReconnectEnv)
}

// standbyServer answers a handover login with a standby and the commit with
// the resumed session.
func standbyServer(t *testing.T, mode string) (string, <-chan netproto.Authenticate, <-chan struct{}) {
	t.Helper()
	cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests, commits := make(chan netproto.Authenticate, 1), make(chan struct{}, 1)
	write := func(conn net.Conn, resp netproto.AuthResponse) {
		resp.AuthorizationModel = netproto.AuthorizationModelRolesV1
		f, _ := netproto.Encode(netproto.MsgAuthResponse, resp)
		_ = netproto.WriteFrame(conn, f)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		frame, err := readLoginFrame(conn)
		var request netproto.Authenticate
		if err != nil || netproto.Decode(frame, &request) != nil {
			return
		}
		requests <- request
		if mode == "no standby" {
			// A server that ignores the standby request moves the session at once.
			write(conn, netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", ResumeToken: "token"})
			for {
				if _, err := netproto.ReadFrame(conn); err != nil {
					return
				}
			}
		}
		write(conn, netproto.AuthResponse{OK: true, Standby: true, ClientID: "c-1", UniqueID: "u"})
		for {
			frame, err := netproto.ReadFrame(conn)
			if err != nil {
				return
			}
			if netproto.MessageType(frame.Type) == netproto.MsgHandoverCommit {
				commits <- struct{}{}
				if mode == "reply lost" {
					return // the commit reply never arrives
				}
				write(conn, netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", ResumeToken: "token"})
			}
		}
	}()
	return listener.Addr().String(), requests, commits
}

func TestHandoverLoginCommitsOnlyAfterTheOldProcessAgrees(t *testing.T) {
	for _, mode := range []string{"agree", "refuse", "reply lost", "no standby"} {
		addr, requests, commits := standbyServer(t, mode)
		cm := newConnManager(t.Context())
		cm.sink = &eventRecorder{}
		cm.id = mustTempIdentity(t)
		cm.resumeToken = "token"
		asked := 0
		cm.handoverBegin = func() bool { asked++; return mode != "refuse" }
		failure := cm.connectNamed(addr, "owner", "", "pw", "")
		request := <-requests
		if !request.Handover || !slices.Contains(request.ClientFeatures, netproto.ClientFeatureHandover) || request.ResumeToken != "token" {
			t.Fatalf("%s: handover login = %+v", mode, request)
		}
		cm.mu.Lock()
		begin, committed, token := cm.handoverBegin, cm.handoverCommitted, cm.resumeToken
		cm.mu.Unlock()
		wantAsked := 1
		if mode == "no standby" {
			wantAsked = 0
		}
		if asked != wantAsked || begin != nil {
			t.Fatalf("%s: old process asked %d times; the hook must be used once", mode, asked)
		}
		switch mode {
		case "agree":
			if failure != "" || !committed {
				t.Fatalf("agree: %q committed %v", failure, committed)
			}
			<-commits
			if cm.clientIDSnapshot() != "c-1" || token != "token" {
				t.Fatal("the committed session was not installed")
			}
		case "refuse":
			if failure == "" || committed {
				t.Fatal("a refused handover connected or counted as committed")
			}
			select {
			case <-commits:
				t.Fatal("committed without the old process's agreement")
			case <-time.After(100 * time.Millisecond):
			}
		case "no standby":
			// Never a silent bypass: the session moved, so it is kept as committed.
			if failure != "" || !committed {
				t.Fatalf("no standby: %q committed %v", failure, committed)
			}
		case "reply lost":
			<-commits
			// The commit was sent: the session may live here now, under the
			// token both processes hold. It must never be handed back.
			if failure == "" || !committed || token != "token" {
				t.Fatalf("reply lost: %q committed %v token %q", failure, committed, token)
			}
		}
		cm.disconnect()
	}
}

func TestHandoverWindowAppearsWhenOldProcessExits(t *testing.T) {
	original := showUpdateWindow
	t.Cleanup(func() { showUpdateWindow = original })
	shows := make(chan struct{}, 4)
	showUpdateWindow = func(context.Context) { shows <- struct{}{} }
	for _, quitting := range []bool{false, true} {
		release := make(chan struct{})
		h := &incomingUpdateHandover{parentGone: make(chan struct{}),
			parentWait: func(context.Context) error { <-release; return nil }}
		a := &App{ctx: context.Background()}
		a.quitting.Store(quitting)
		h.watchParent(a)
		select {
		case <-shows:
			t.Fatal("shown while the old process still runs")
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		<-h.parentGone
		select {
		case <-shows:
			if quitting {
				t.Fatal("a quitting handover process showed its window")
			}
		case <-time.After(300 * time.Millisecond):
			if !quitting {
				t.Fatal("a handover process stayed hidden after the old process exited")
			}
		}
	}
}

// commitThenDropServer commits a standby login, then drops the connection
// when the restored client rejoins its channel. It reports any goodbye.
func commitThenDropServer(t *testing.T, mode string) (string, *atomic.Bool) {
	t.Helper()
	cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var goodbye atomic.Bool
	write := func(conn net.Conn, resp netproto.AuthResponse) {
		resp.AuthorizationModel = netproto.AuthorizationModelRolesV1
		f, _ := netproto.Encode(netproto.MsgAuthResponse, resp)
		_ = netproto.WriteFrame(conn, f)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := netproto.ReadFrame(conn); err != nil {
			return
		}
		if mode == "no standby" {
			// A server that ignores the standby request moves the session at once.
			write(conn, netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", ResumeToken: "token"})
			for {
				if _, err := netproto.ReadFrame(conn); err != nil {
					return
				}
			}
		}
		write(conn, netproto.AuthResponse{OK: true, Standby: true, ClientID: "c-1", UniqueID: "u"})
		for {
			frame, err := netproto.ReadFrame(conn)
			if err != nil {
				return
			}
			switch netproto.MessageType(frame.Type) {
			case netproto.MsgHandoverCommit:
				if mode == "reply lost" {
					return // the server committed, but its reply never arrives
				}
				// A commit keeps the token the standby presented.
				done := netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", Nickname: "account", ResumeToken: "token"}
				if mode == "setup failed after commit" {
					done.MediaLimitsRevision = 5 // fails the client's setup after the commit
				}
				write(conn, done)
			case netproto.MsgGoodbye:
				goodbye.Store(true)
			case netproto.MsgJoinChannel:
				return // the connection drops right after the commit
			}
		}
	}()
	return listener.Addr().String(), &goodbye
}

func TestHandoverKeepsMovedSessionWhenRestorationLosesConnection(t *testing.T) {
	for _, mode := range []string{"dropped at rejoin", "setup failed after commit", "reply lost"} {
		t.Run(mode, func(t *testing.T) { testHandoverKeepsMovedSession(t, mode) })
	}
}

func testHandoverKeepsMovedSession(t *testing.T, mode string) {
	originalShow, originalHandover := showUpdateWindow, incomingHandover
	t.Cleanup(func() { showUpdateWindow, incomingHandover = originalShow, originalHandover })
	showUpdateWindow = func(context.Context) {}
	r := handoverChannel(t)
	incomingHandover = &incomingUpdateHandover{address: r.address(), nonce: r.nonce, parentGone: make(chan struct{})}
	var committing atomic.Int32
	outcome := serveAsync(t.Context(), r, os.Getpid(), nil, &committing)

	addr, goodbye := commitThenDropServer(t, mode)
	a := newTabApp(t)
	var mu sync.Mutex
	var events []string
	a.eventEmit = func(name string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, name)
	}
	saved := testUpdateEnvelope(t)
	saved.Addr = addr
	saved.ResumeToken = "token"
	got := a.restoreUpdateConnection(&saved)
	tabID, _ := got["tab_id"].(string)
	if tabID == "" || got["error"] != nil {
		t.Fatalf("a moved session was given up: %v", got)
	}
	defer a.CloseTab(tabID)
	if !waitReconnect(t, outcome) {
		t.Fatal("the old process was not released")
	}
	if a.activeID != tabID || a.tabs[tabID].cm.resumeTokenSnapshot() != "token" || a.tabs[tabID].lastVoiceChannel != saved.ChannelID {
		t.Fatal("the restored tab must stay selected with the session's token and voice channel for its resume")
	}
	// The frontend starts the reconnect once it recorded the tab; the native
	// side must allow it, and only after the old process confirmed.
	a.tabsMu.Lock()
	allowed := a.tabs[tabID].reconnectAllowed
	a.tabsMu.Unlock()
	if got["reconnect"] != true || !allowed {
		t.Fatalf("the kept tab is not set up for its reconnect: %v allowed %v", got, allowed)
	}
	if !incomingHandover.settled() {
		t.Fatal("a confirmed handover must allow ordinary reconnects")
	}
	if goodbye.Load() {
		t.Fatal("a moved session was ended with a goodbye")
	}
}

func TestSkippedRestorationNeverAbandonsTheHandover(t *testing.T) {
	original := incomingHandover
	t.Cleanup(func() { incomingHandover = original })
	incomingHandover = &incomingUpdateHandover{parentGone: make(chan struct{})}
	a := newTabApp(t)
	a.updateReconnectConsumed = true // another call owns the restoration
	if got := a.RestoreUpdateConnection(); got["attempted"] != false {
		t.Fatalf("result = %v", got)
	}
	if a.quitting.Load() {
		t.Fatal("a repeated restoration call abandoned the handover in progress")
	}
}

func TestHandoverReplacementWaitsBeforeRotatingTheToken(t *testing.T) {
	original := incomingHandover
	t.Cleanup(func() { incomingHandover = original })
	h := &incomingUpdateHandover{parentGone: make(chan struct{})}
	incomingHandover = h
	a := newTabApp(t)
	if got := a.ReconnectTab("any", "", ""); got.Error != "waiting for the previous version to close" || got.Terminal {
		t.Fatalf("unconfirmed handover reconnect = %+v", got)
	}
	close(h.parentGone) // the old process exited: it can no longer fall back
	if !h.settled() {
		t.Fatal("an exited old process must allow reconnects")
	}
	h2 := &incomingUpdateHandover{parentGone: make(chan struct{})}
	h2.confirmed.Store(true)
	if !h2.settled() {
		t.Fatal("a confirmed commit must allow reconnects")
	}
}

func TestAbandonKeepsTheReplacementWhenTheOldProcessIsGone(t *testing.T) {
	original := showUpdateWindow
	t.Cleanup(func() { showUpdateWindow = original })
	shown := 0
	showUpdateWindow = func(context.Context) { shown++ }
	h := &incomingUpdateHandover{parentGone: make(chan struct{})}
	close(h.parentGone)
	a := &App{ctx: context.Background()}
	h.abandon(a)
	if a.quitting.Load() || shown != 1 {
		t.Fatalf("quitting %v shown %d: with no old process left the replacement must stay open and visible", a.quitting.Load(), shown)
	}
}
