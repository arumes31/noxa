package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"
)

const updateReconnectEnv = "NOXA_UPDATE_RECONNECT"
const updateReconnectMaxBytes = 32 * 1024

// Credentials stay native and OS-protected, even while crossing the updater
// supervisor. Nothing is persisted in settings or exposed to the renderer.
type updateReconnectEnvelope struct {
	Version           int                 `json:"version"`
	Expires           int64               `json:"expires"`
	Addr              string              `json:"addr"`
	Nickname          string              `json:"nickname"`
	DisplayName       string              `json:"display_name"`
	Bookmark          string              `json:"bookmark"`
	ChannelID         int64               `json:"channel_id"`
	Identity          identity            `json:"identity"`
	Passwords         loginPasswords      `json:"passwords"`
	Muted             bool                `json:"muted"`
	Deafened          bool                `json:"deafened"`
	MutedBeforeDeafen bool                `json:"muted_before_deafen"`
	Stream            *updateStreamResume `json:"stream,omitempty"`
}

type updateStreamResume struct {
	Surface string `json:"surface"`
	Preset  string `json:"preset"`
	Custom  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
		FPS    int `json:"fps"`
	} `json:"custom"`
	BitrateCeiling int    `json:"bitrateCeiling"`
	AudioMode      string `json:"audioMode"`
}

func validUpdateStream(s *updateStreamResume) bool {
	if s == nil {
		return true
	}
	return slices.Contains([]string{"monitor", "window", "region", "studio"}, s.Surface) &&
		slices.Contains([]string{"balanced", "text", "motion", "hd", "hdMotion", "qhd", "uhd", "original", "custom"}, s.Preset) &&
		slices.Contains([]string{"none", "system", "application"}, s.AudioMode) &&
		slices.Contains([]int{0, 2000000, 4000000, 6000000, 8000000}, s.BitrateCeiling) &&
		(s.Preset != "custom" || (s.Custom.Width >= 160 && s.Custom.Width <= 8192 && s.Custom.Height >= 160 && s.Custom.Height <= 8192 && slices.Contains([]int{15, 30, 60}, s.Custom.FPS)))
}

// Preferences only: the new process opens the normal source picker again.
func (a *App) PrepareUpdateStreamResume(raw string) string {
	if len(raw) > 2048 {
		return "invalid stream settings"
	}
	var stream updateStreamResume
	if json.Unmarshal([]byte(raw), &stream) != nil || !validUpdateStream(&stream) {
		return "invalid stream settings"
	}
	a.update.mu.Lock()
	defer a.update.mu.Unlock()
	if a.update.running || a.updateReconnect == "" {
		return ""
	}
	saved, err := decodeUpdateReconnect(a.updateReconnect, time.Now(), unprotectBytes)
	if err != nil {
		return "cannot preserve stream settings"
	}
	current := a.snapshotUpdateConnection()
	if current == nil || saved.Addr != current.Addr || saved.ChannelID != current.ChannelID || saved.Identity.PublicKey != current.Identity.PublicKey {
		return ""
	}
	saved.Stream = &stream
	encoded, err := encodeUpdateReconnect(*saved, protectBytes)
	if err != nil {
		return "cannot preserve stream settings"
	}
	a.updateReconnect = encoded
	return ""
}

// PrepareUpdateReconnect captures only the currently connected server. A normal
// launch never consumes this envelope, and an intentional disconnect is excluded.
func (a *App) PrepareUpdateReconnect(muted, deafened, mutedBeforeDeafen bool) string {
	a.update.mu.Lock()
	defer a.update.mu.Unlock()
	if a.update.running {
		return "an update is already processing"
	}
	a.updateReconnect = ""
	snapshot := a.snapshotUpdateConnection()
	if snapshot == nil {
		// A chained update may run before the successor consumes the handoff.
		if startedAfterUpdate && !a.updateReconnectConsumed {
			a.updateReconnect = os.Getenv(updateReconnectEnv)
		}
		return ""
	}
	snapshot.Muted, snapshot.Deafened, snapshot.MutedBeforeDeafen = muted || deafened, deafened, mutedBeforeDeafen
	encoded, err := encodeUpdateReconnect(*snapshot, protectBytes)
	if err != nil {
		return "Cannot preserve the connection for restart: " + err.Error()
	}
	a.updateReconnect = encoded
	return ""
}

func (a *App) snapshotUpdateConnection() *updateReconnectEnvelope {
	a.tabsMu.Lock()
	tab := a.tabs[a.activeID]
	if tab == nil || tab.cm == nil || tab.reconnectSuppressed {
		a.tabsMu.Unlock()
		return nil
	}
	cm := tab.cm
	saved := updateReconnectEnvelope{Version: 1, Expires: time.Now().Add(10 * time.Minute).Unix(), Addr: tab.info.Addr,
		Nickname: tab.info.Nickname, Bookmark: tab.bookmark, ChannelID: tab.lastVoiceChannel}
	if tab.loginPasswords != nil {
		saved.Passwords = *tab.loginPasswords
	}
	a.tabsMu.Unlock()
	cm.mu.Lock()
	if cm.conn == nil || cm.closed || cm.id == nil {
		cm.mu.Unlock()
		return nil
	}
	saved.Identity = *cm.id
	saved.DisplayName = cm.displayName
	cm.mu.Unlock()
	a.tabsMu.Lock()
	current := a.tabs[a.activeID] == tab && !tab.reconnectSuppressed
	a.tabsMu.Unlock()
	if !current {
		return nil
	}
	return &saved
}

func encodeUpdateReconnect(saved updateReconnectEnvelope, protect func([]byte) ([]byte, error)) (string, error) {
	raw, err := json.Marshal(saved)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	protected, err := protect(raw)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(protected)
	if len(encoded) > updateReconnectMaxBytes {
		return "", errors.New("connection handoff is too large")
	}
	return encoded, nil
}

func decodeUpdateReconnect(encoded string, now time.Time, unprotect func([]byte) ([]byte, error)) (*updateReconnectEnvelope, error) {
	if len(encoded) > updateReconnectMaxBytes {
		return nil, errors.New("invalid connection handoff")
	}
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid connection handoff")
	}
	raw, err := unprotect(blob)
	if err != nil {
		return nil, errors.New("connection handoff cannot be unlocked")
	}
	defer clear(raw)
	var saved updateReconnectEnvelope
	if json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.Expires < now.Unix() || saved.Expires > now.Add(10*time.Minute).Unix() ||
		saved.Addr == "" || saved.Nickname == "" || saved.ChannelID < 0 || !validUpdateStream(saved.Stream) {
		return nil, errors.New("connection handoff is invalid or expired")
	}
	if _, err := saved.Identity.uniqueID(); err != nil {
		return nil, errors.New("connection identity is invalid")
	}
	return &saved, nil
}

// RestoreUpdateConnection is invoked after genuine frontend readiness, before
// bookmark auto-connect. Only the updater-launched process can consume it once.
func (a *App) RestoreUpdateConnection() map[string]any {
	a.update.mu.Lock()
	if a.updateReconnectConsumed || a.UpdateStartupPending() {
		a.update.mu.Unlock()
		return map[string]any{"attempted": false}
	}
	a.updateReconnectConsumed = true
	encoded := os.Getenv(updateReconnectEnv)
	_ = os.Unsetenv(updateReconnectEnv)
	a.update.mu.Unlock()
	if !startedAfterUpdate || encoded == "" {
		return map[string]any{"attempted": false}
	}
	result := map[string]any{"attempted": true}
	saved, err := decodeUpdateReconnect(encoded, time.Now(), unprotectBytes)
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	return a.restoreUpdateConnection(saved)
}

func (a *App) restoreUpdateConnection(saved *updateReconnectEnvelope) map[string]any {
	result := map[string]any{"attempted": true}
	result["addr"], result["nick"], result["displayName"], result["bookmark"] = saved.Addr, saved.Nickname, saved.DisplayName, saved.Bookmark
	// A user who has already connected manually owns the new process.
	if len(a.ListTabs()) > 0 {
		return result
	}
	a.emitPlain("update_connection_restoring", map[string]bool{"muted": saved.Muted, "deafened": saved.Deafened, "muted_before_deafen": saved.MutedBeforeDeafen})
	id, tab := a.newTabWithIdentity(&saved.Identity)
	committed := false
	defer func() {
		if !committed {
			a.CloseTab(id)
		}
	}()
	if failure := tab.cm.connectNamed(saved.Addr, saved.Nickname, saved.DisplayName, saved.Passwords.Password, saved.Passwords.ServerPassword); failure != "" {
		a.removeTab(id)
		result["error"] = failure
		return result
	}
	a.tabsMu.Lock()
	owned := a.tabs[id] == tab && !tab.reconnectSuppressed && a.activeID == "" && len(a.tabs) == 1
	if owned {
		tab.info.Addr, tab.info.Nickname, tab.bookmark = saved.Addr, saved.Nickname, saved.Bookmark
		tab.loginPasswords = &saved.Passwords
	}
	a.tabsMu.Unlock()
	if !owned {
		tab.cm.disconnect()
		result["error"] = "connection restoration was cancelled"
		return result
	}
	if saved.ChannelID > 0 {
		if warning := tab.cm.joinChannel(saved.ChannelID); warning != "" {
			result["warning"] = warning
		}
	}
	a.activationPublishMu.Lock()
	defer a.activationPublishMu.Unlock()
	connected := tab.cm.connected()
	a.tabsMu.Lock()
	if !connected || a.tabs[id] != tab || tab.reconnectSuppressed || a.activeID != "" || len(a.tabs) != 1 {
		a.tabsMu.Unlock()
		result["error"] = "connection restoration was cancelled"
		return result
	}
	cm, journal, generation, _ := a.activateLocked(id)
	a.tabsMu.Unlock()
	committed = true
	a.onTabConnected(cm, saved.Bookmark, saved.Addr, saved.Nickname, saved.DisplayName)
	if saved.ChannelID > 0 && result["warning"] == nil {
		a.emitPlain("tab_voice_restored", map[string]any{"tab_id": id, "client_id": tab.cm.clientIDSnapshot(), "channel_id": saved.ChannelID})
	}
	a.finishActivateSerialized(id, cm, journal, generation)
	result["tab_id"] = id
	if saved.Stream != nil && saved.ChannelID > 0 && result["warning"] == nil {
		result["stream"] = saved.Stream
		result["stream_channel_id"] = saved.ChannelID
	}
	return result
}
