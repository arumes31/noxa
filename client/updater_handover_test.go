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

func serveAsync(ctx context.Context, r *updateReadiness, pid int, exited <-chan error, committing *atomic.Int32) <-chan handoverOutcome {
	result := make(chan handoverOutcome, 1)
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
	if <-result != handoverMoved {
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
	if <-result != handoverKept {
		t.Fatal("an abandoned handover moved the session")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if <-serveAsync(ctx, handoverChannel(t), 7, nil, &committing) != handoverKept {
		t.Fatal("a silent child moved the session")
	}

	exited := make(chan error, 1)
	exited <- errors.New("crashed")
	if <-serveAsync(t.Context(), handoverChannel(t), 7, exited, &committing) != handoverKept {
		t.Fatal("an exited child moved the session")
	}
}

// A lost final signal after "committing" is reported as unconfirmed; the
// launcher then keeps the new process if the server moved the session.
func TestUpdateHandoverLostConfirmationIsUnconfirmed(t *testing.T) {
	var committing atomic.Int32
	r := handoverChannel(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	result := serveAsync(ctx, r, 9, nil, &committing)
	if err := sendUpdateHandoverSignal(r.address(), r.nonce, 9, handoverCommitting); err != nil {
		t.Fatal(err)
	}
	if <-result != handoverUnconfirmed {
		t.Fatal("a commit without confirmation must stay undecided")
	}

	beginHandoverSilence()
	if handoverSessionTaken() {
		t.Fatal("no connection closed, yet the session counts as taken")
	}
	deferHandoverDisconnect(&connManager{sink: &eventRecorder{}})
	if !handoverSessionTaken() {
		t.Fatal("a connection closed during the commit means the server moved the session")
	}
	endHandoverSilence(false)
}

func TestIncomingHandoverConfiguration(t *testing.T) {
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
	replayed, dropped := &eventRecorder{}, &eventRecorder{}
	if deferHandoverDisconnect(&connManager{sink: replayed}) {
		t.Fatal("deferred outside a handover")
	}
	beginHandoverSilence()
	if !deferHandoverDisconnect(&connManager{sink: replayed}) {
		t.Fatal("not deferred during a commit")
	}
	endHandoverSilence(true)
	if emitted(replayed) != 1 {
		t.Fatal("an abandoned handover must replay the disconnect")
	}
	beginHandoverSilence()
	deferHandoverDisconnect(&connManager{sink: dropped})
	endHandoverSilence(false)
	if emitted(dropped) != 0 {
		t.Fatal("a moved session must not announce its old connection's close")
	}
}

func TestApplyAndRestartHandsOverOrFallsBack(t *testing.T) {
	originalLaunch, originalQuit, originalHandover := restartLaunch, wailsQuit, handOverSession
	t.Cleanup(func() { restartLaunch, wailsQuit, handOverSession = originalLaunch, originalQuit, originalHandover })
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	app := &App{ctx: context.Background(), updateReconnect: "saved"}
	app.cmStore(&connManager{conn: client, supportsHandover: true, resumeToken: "token"})
	var order []string
	restartLaunch = func(string) error { order = append(order, "restart"); return nil }
	wailsQuit = func(context.Context) { order = append(order, "quit") }

	handOverSession = func(*App, string) (bool, bool, error) { order = append(order, "handover"); return true, false, nil }
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"handover", "quit"}) {
		t.Fatalf("moved: %q order %v", got, order)
	}
	quitForUpdate.Store(false)

	order, app.updateReconnect = nil, "saved"
	handOverSession = func(*App, string) (bool, bool, error) {
		order = append(order, "handover")
		return false, true, errors.New("abandoned")
	}
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"handover", "restart", "quit"}) {
		t.Fatalf("fallback: %q order %v", got, order)
	}
	quitForUpdate.Store(false)

	order, app.updateReconnect = nil, "saved"
	handOverSession = func(*App, string) (bool, bool, error) {
		order = append(order, "handover")
		return false, false, errors.New("replacement still running")
	}
	if got := app.ApplyAndRestart(); got == "" || !slices.Equal(order, []string{"handover"}) {
		t.Fatalf("unstoppable child: %q order %v", got, order)
	}

	// Without server support or a saved session the restart path runs directly.
	order, app.updateReconnect = nil, "saved"
	app.cmStore(&connManager{conn: client, resumeToken: "token"})
	if got := app.ApplyAndRestart(); got != "" || !slices.Equal(order, []string{"restart", "quit"}) {
		t.Fatalf("no server support: %q order %v", got, order)
	}
	quitForUpdate.Store(false)
	_ = os.Unsetenv(updateReconnectEnv)
}

// standbyServer answers a handover login with a standby and the commit with
// the resumed session.
func standbyServer(t *testing.T) (string, <-chan netproto.Authenticate, <-chan struct{}) {
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
		frame, err := netproto.ReadFrame(conn)
		var request netproto.Authenticate
		if err != nil || netproto.Decode(frame, &request) != nil {
			return
		}
		requests <- request
		write(conn, netproto.AuthResponse{OK: true, Standby: true, ClientID: "c-1", UniqueID: "u"})
		for {
			frame, err := netproto.ReadFrame(conn)
			if err != nil {
				return
			}
			if netproto.MessageType(frame.Type) == netproto.MsgHandoverCommit {
				commits <- struct{}{}
				write(conn, netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", ResumeToken: "rotated"})
			}
		}
	}()
	return listener.Addr().String(), requests, commits
}

func TestHandoverLoginCommitsOnlyAfterTheOldProcessAgrees(t *testing.T) {
	for _, agree := range []bool{true, false} {
		addr, requests, commits := standbyServer(t)
		cm := newConnManager(t.Context())
		cm.sink = &eventRecorder{}
		cm.id = mustTempIdentity(t)
		cm.resumeToken = "token"
		asked := 0
		cm.handoverBegin = func() bool { asked++; return agree }
		failure := cm.connectNamed(addr, "owner", "", "pw", "")
		request := <-requests
		if !request.Handover || !slices.Contains(request.ClientFeatures, netproto.ClientFeatureHandover) || request.ResumeToken != "token" {
			t.Fatalf("handover login = %+v", request)
		}
		if asked != 1 {
			t.Fatalf("old process asked %d times", asked)
		}
		if agree {
			if failure != "" {
				t.Fatal(failure)
			}
			<-commits
			if cm.clientIDSnapshot() != "c-1" || cm.resumeTokenSnapshot() != "rotated" {
				t.Fatal("the committed session was not installed")
			}
		} else {
			if failure == "" {
				t.Fatal("a refused handover connected")
			}
			select {
			case <-commits:
				t.Fatal("committed without the old process's agreement")
			case <-time.After(100 * time.Millisecond):
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
func commitThenDropServer(t *testing.T) (string, *atomic.Bool) {
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
		write(conn, netproto.AuthResponse{OK: true, Standby: true, ClientID: "c-1", UniqueID: "u"})
		for {
			frame, err := netproto.ReadFrame(conn)
			if err != nil {
				return
			}
			switch netproto.MessageType(frame.Type) {
			case netproto.MsgHandoverCommit:
				write(conn, netproto.AuthResponse{OK: true, Resumed: true, ClientID: "c-1", UniqueID: "u", Nickname: "account", ResumeToken: "rotated"})
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
	originalShow, originalHandover := showUpdateWindow, incomingHandover
	t.Cleanup(func() { showUpdateWindow, incomingHandover = originalShow, originalHandover })
	showUpdateWindow = func(context.Context) {}
	r := handoverChannel(t)
	incomingHandover = &incomingUpdateHandover{address: r.address(), nonce: r.nonce, parentGone: make(chan struct{})}
	var committing atomic.Int32
	outcome := serveAsync(t.Context(), r, os.Getpid(), nil, &committing)

	addr, goodbye := commitThenDropServer(t)
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
	if waitReconnect(t, outcome) != handoverMoved {
		t.Fatal("the old process was not released")
	}
	if a.activeID != tabID || a.tabs[tabID].cm.resumeTokenSnapshot() != "rotated" {
		t.Fatal("the restored tab must stay selected with the rotated token for its resume")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		reconnecting := slices.Contains(events, "tab_disconnected")
		mu.Unlock()
		if reconnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restored tab did not start its normal reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if goodbye.Load() {
		t.Fatal("a moved session was ended with a goodbye")
	}
}
