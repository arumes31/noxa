package main

// native_toast.go — (501) Windows Action Center notifications with an inline
// quick-reply input and a "Mark as read" action.
//
// Design:
//   - Toasts are shown through WinRT for the per-user AppUserModelID
//     nativeToastAppID, registered under HKCU together with a COM activator
//     class (internal/wintoast). Windows calls that activator in the running
//     app; when noXa is not running, COM starts the executable instead.
//   - A toast carries no conversation identity, only an opaque random token
//     the frontend issued ("noxa-toast:v1:<action>:<token>"). Go remembers
//     which tokens it showed; an activation with an unknown, expired, used or
//     malformed token is ignored. The frontend maps the token back to the
//     notification destination it captured (tab, signed-in identity and
//     conversation) and revalidates all of it before acting.
//   - Quick-reply text is handed to the frontend, which sends it through the
//     same composer and end-to-end encrypted send path as typed text. Nothing
//     is sent from this layer.
//   - One toast per conversation: the tag is a hash of the conversation key,
//     so a new message replaces the previous toast, and reading the
//     conversation removes it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"noxa/client/internal/wintoast"
)

const (
	nativeToastAppID   = "noXa.Desktop"
	nativeToastCLSID   = "{13143946-1C65-46E6-A846-858A8077F17A}"
	nativeToastGroup   = "noxa"
	nativeToastPrefix  = "noxa-toast:v1:"
	nativeToastInputID = "reply"

	nativeToastMaxTitle  = 128
	nativeToastMaxBody   = 512
	nativeToastMaxLabel  = 64
	nativeToastMaxReply  = 8192
	nativeToastMaxTokens = 200
	nativeToastTokenTTL  = 24 * time.Hour
	nativeToastEvent     = "native_toast_activation"
)

// NativeToastRequest is one toast the frontend asks for. Title and body are
// already reduced to what the user's privacy settings allow.
type NativeToastRequest struct {
	Token        string `json:"token"`        // 32 lowercase hex characters
	Conversation string `json:"conversation"` // opaque key: one toast per key
	Title        string `json:"title"`
	Body         string `json:"body"`
	Replyable    bool   `json:"replyable"`
	ReplyLabel   string `json:"reply_label"`
	ReadLabel    string `json:"read_label"`
	Placeholder  string `json:"placeholder"`
}

// NativeToastActivation is emitted to the frontend as nativeToastEvent.
type NativeToastActivation struct {
	Token        string `json:"token"`
	Action       string `json:"action"` // open | reply | read
	Conversation string `json:"conversation"`
	Reply        string `json:"reply,omitempty"`
}

type nativeToastEntry struct {
	conversation string
	expires      time.Time
}

// nativeToastState holds the registration result and the tokens this process
// showed. Its zero value is usable.
type nativeToastState struct {
	mu        sync.Mutex
	available bool
	tokens    map[string]nativeToastEntry
}

// issue remembers token for conversation, dropping expired and, beyond the
// bound, the oldest entries.
func (s *nativeToastState) issue(token, conversation string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = make(map[string]nativeToastEntry)
	}
	for key, entry := range s.tokens {
		if !now.Before(entry.expires) {
			delete(s.tokens, key)
		}
	}
	for len(s.tokens) >= nativeToastMaxTokens {
		oldest, at := "", time.Time{}
		for key, entry := range s.tokens {
			if oldest == "" || entry.expires.Before(at) {
				oldest, at = key, entry.expires
			}
		}
		delete(s.tokens, oldest)
	}
	s.tokens[token] = nativeToastEntry{conversation: conversation, expires: now.Add(nativeToastTokenTTL)}
}

// take consumes token: a toast acts at most once.
func (s *nativeToastState) take(token string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tokens[token]
	if !ok {
		return "", false
	}
	delete(s.tokens, token)
	if !now.Before(entry.expires) {
		return "", false
	}
	return entry.conversation, true
}

// forget drops every token of conversation ("" drops all).
func (s *nativeToastState) forget(conversation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.tokens {
		if conversation == "" || entry.conversation == conversation {
			delete(s.tokens, key)
		}
	}
}

func validToastToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// conversationToastTag maps a conversation key to a stable toast tag that
// does not reveal the key (Action Center tags are readable by other apps).
func conversationToastTag(conversation string) string {
	sum := sha256.Sum256([]byte("noxa-toast-tag\x00" + conversation))
	return hex.EncodeToString(sum[:16])
}

func clipRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func escapeToastText(b *strings.Builder, value string) {
	_ = xml.EscapeText(b, []byte(value))
}

var errInvalidToast = errors.New("invalid native notification")

// buildToastXML renders the toast. Every user-controlled value is escaped;
// the only arguments are fixed action names and the validated token.
func buildToastXML(req NativeToastRequest) (string, error) {
	if !validToastToken(req.Token) || strings.TrimSpace(req.Conversation) == "" {
		return "", errInvalidToast
	}
	title := clipRunes(req.Title, nativeToastMaxTitle)
	if title == "" {
		title = "noXa"
	}
	body := clipRunes(req.Body, nativeToastMaxBody)
	var b strings.Builder
	b.WriteString(`<toast launch="` + nativeToastPrefix + "open:" + req.Token + `" activationType="foreground">`)
	b.WriteString(`<visual><binding template="ToastGeneric"><text hint-maxLines="1">`)
	escapeToastText(&b, title)
	b.WriteString(`</text>`)
	if body != "" {
		b.WriteString(`<text>`)
		escapeToastText(&b, body)
		b.WriteString(`</text>`)
	}
	// Sounds follow the in-app audio settings, never the system toast sound.
	b.WriteString(`</binding></visual><audio silent="true"/>`)
	readLabel := clipRunes(req.ReadLabel, nativeToastMaxLabel)
	if readLabel == "" {
		readLabel = "Mark as read"
	}
	b.WriteString(`<actions>`)
	if req.Replyable {
		replyLabel := clipRunes(req.ReplyLabel, nativeToastMaxLabel)
		if replyLabel == "" {
			replyLabel = "Reply"
		}
		b.WriteString(`<input id="` + nativeToastInputID + `" type="text" placeHolderContent="`)
		escapeToastText(&b, clipRunes(req.Placeholder, nativeToastMaxLabel))
		b.WriteString(`"/><action content="`)
		escapeToastText(&b, replyLabel)
		b.WriteString(`" arguments="` + nativeToastPrefix + "reply:" + req.Token + `" activationType="background" hint-inputId="` + nativeToastInputID + `"/>`)
	}
	b.WriteString(`<action content="`)
	escapeToastText(&b, readLabel)
	b.WriteString(`" arguments="` + nativeToastPrefix + "read:" + req.Token + `" activationType="background"/>`)
	b.WriteString(`</actions></toast>`)
	return b.String(), nil
}

// parseToastActivation validates activation arguments and extracts the
// reply text. Anything not produced by buildToastXML is rejected.
func parseToastActivation(arguments string, inputs []wintoast.UserData) (NativeToastActivation, error) {
	rest, ok := strings.CutPrefix(arguments, nativeToastPrefix)
	if !ok {
		return NativeToastActivation{}, errInvalidToast
	}
	action, token, ok := strings.Cut(rest, ":")
	if !ok || !validToastToken(token) {
		return NativeToastActivation{}, errInvalidToast
	}
	activation := NativeToastActivation{Token: token, Action: action}
	switch action {
	case "open", "read":
	case "reply":
		for _, input := range inputs {
			if input.Key == nativeToastInputID {
				activation.Reply = strings.TrimSpace(input.Value)
			}
		}
		if activation.Reply == "" || utf8.RuneCountInString(activation.Reply) > nativeToastMaxReply || !utf8.ValidString(activation.Reply) {
			return NativeToastActivation{}, errInvalidToast
		}
	default:
		return NativeToastActivation{}, errInvalidToast
	}
	return activation, nil
}

// Seams for tests.
var (
	nativeToastRegister = wintoast.Register
	nativeToastShow     = wintoast.Show
	nativeToastRemove   = wintoast.Remove
	nativeToastClear    = wintoast.Clear
	nativeToastRaise    = func(a *App) {
		if a.ctx == nil {
			return
		}
		wailsRuntime.WindowShow(a.ctx)
		windowUnminimize(a.ctx)
		trayMarkShown()
	}
	nativeToastEmit = func(a *App, activation NativeToastActivation) {
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, nativeToastEvent, activation)
		}
	}
)

// initNativeToasts registers the app with Windows and removes toasts left by
// an earlier run: their tokens died with that process.
func (a *App) initNativeToasts() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	data := wintoast.AppData{AppID: nativeToastAppID, DisplayName: "noXa", CLSID: nativeToastCLSID, Executable: exe, IconPath: nativeToastIcon()}
	wintoast.SetCallback(func(_, arguments string, inputs []wintoast.UserData) {
		a.handleToastActivation(arguments, inputs, time.Now())
	})
	if err := nativeToastRegister(data); err != nil {
		if !errors.Is(err, wintoast.ErrUnsupported) {
			log.Printf("native notifications unavailable: %v", err)
		}
		return
	}
	a.toasts.mu.Lock()
	a.toasts.available = true
	a.toasts.mu.Unlock()
	_ = nativeToastClear(nativeToastAppID)
}

// nativeToastIcon copies the app icon next to the profile so Action Center
// can show it; an empty path keeps the default icon.
func nativeToastIcon() string {
	data, err := assets.ReadFile("frontend/dist/branding/icon-192.png")
	if err != nil {
		return ""
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	// Rewritten on every start (a few KB) so an update replaces the icon.
	path := filepath.Join(dir, "noxa", "notification-icon.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return ""
	}
	return path
}

// NativeToastsAvailable reports whether Windows notifications can be shown.
func (a *App) NativeToastsAvailable() bool {
	a.toasts.mu.Lock()
	defer a.toasts.mu.Unlock()
	return a.toasts.available
}

// ShowNativeToast shows or replaces the conversation's toast. It returns an
// error message, or "" on success.
func (a *App) ShowNativeToast(req NativeToastRequest) string {
	if !a.NativeToastsAvailable() {
		return "native notifications are unavailable"
	}
	content, err := buildToastXML(req)
	if err != nil {
		return err.Error()
	}
	a.toasts.issue(req.Token, req.Conversation, time.Now())
	if err := nativeToastShow(nativeToastAppID, wintoast.Toast{XML: content, Tag: conversationToastTag(req.Conversation), Group: nativeToastGroup}); err != nil {
		log.Printf("native notification failed: %v", err)
		return "native notification failed"
	}
	return ""
}

// ClearNativeToasts removes the conversation's toast ("" removes all of them).
func (a *App) ClearNativeToasts(conversation string) {
	if !a.NativeToastsAvailable() {
		return
	}
	a.toasts.forget(conversation)
	if conversation == "" {
		_ = nativeToastClear(nativeToastAppID)
		return
	}
	_ = nativeToastRemove(nativeToastAppID, nativeToastGroup, conversationToastTag(conversation))
}

// handleToastActivation validates an activation and forwards it. Opening
// shows the window; reply and read run without stealing focus.
func (a *App) handleToastActivation(arguments string, inputs []wintoast.UserData, now time.Time) {
	activation, err := parseToastActivation(arguments, inputs)
	if err != nil {
		if strings.HasPrefix(arguments, nativeToastPrefix+"open:") || arguments == "" {
			nativeToastRaise(a)
		}
		return
	}
	conversation, ok := a.toasts.take(activation.Token, now)
	if activation.Action == "open" {
		nativeToastRaise(a)
	}
	if !ok {
		return // toast from an earlier run, already used, or forged
	}
	activation.Conversation = conversation
	nativeToastEmit(a, activation)
}
