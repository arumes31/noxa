package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"noxa/internal/webpush"
)

// PushSender delivers Web Push messages (webpush.Sender in production).
type PushSender interface {
	Send(ctx context.Context, sub webpush.Subscription, msg webpush.Message) error
	PublicKey() string
}

// PushStore persists web devices' push subscriptions. The device store
// implements it.
type PushStore interface {
	SavePushSubscription(ctx context.Context, p store.PushSubscription) error
	SetPushPreferences(ctx context.Context, principal, deviceID string, hideDetails, alwaysNotify bool) error
	SetPushFilters(ctx context.Context, principal, deviceID string, filters []byte) error
	PushSubscriptionForDevice(ctx context.Context, principal, deviceID string) (store.PushSubscription, error)
	PushSubscriptions(ctx context.Context, principal string) ([]store.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, principal, deviceID string) error
	DeletePushEndpoint(ctx context.Context, endpoint string) error
	RecordPushResult(ctx context.Context, endpoint string, ok bool, maxFailures int) error
}

const (
	// A desktop that reported input this recently delays pushes.
	pushDesktopActiveWindow = 2 * time.Minute
	pushMaxFailures         = 5
	// pushShownMemory bounds how long a sent notification can be cleared.
	pushShownMemory  = 24 * time.Hour
	pushConcurrency  = 16
	pushCallTTL      = 30 * time.Second
	pushMessageTTL   = 24 * time.Hour
	pushSendDeadline = 20 * time.Second
)

// Push kinds. The service worker words the notification in the phone's
// language; payloads carry names only, never message text.
const (
	pushKindDM           = "dm"
	pushKindConversation = "conversation"
	pushKindMention      = "mention"
	pushKindPoke         = "poke"
	pushKindCall         = "call"
	pushKindClear        = "clear"
)

// pushDesktopDelay is how long a push waits while the desktop is in use;
// reading the conversation anywhere cancels it. Tests shorten it.
var pushDesktopDelay = 60 * time.Second

// pushNotice is one event to notify an account's web devices about.
type pushNotice struct {
	Kind string
	// Tag groups notifications of one conversation: dm:<uid>,
	// conversation:<id>, channel:<id>, call:<id>, poke:<uid>.
	Tag string
	// From and Place (channel or group name) are dropped for devices with
	// "Hide notification details".
	From  string
	Place string
	// Video marks an incoming video call.
	Video bool
}

type pushPayload struct {
	Kind  string `json:"kind"`
	Tag   string `json:"tag"`
	From  string `json:"from,omitempty"`
	Place string `json:"place,omitempty"`
	Video bool   `json:"video,omitempty"`
	At    int64  `json:"at"`
}

type pushKey struct{ device, tag string }

type pendingPush struct {
	timer  *time.Timer
	sub    store.PushSubscription
	notice pushNotice
}

// pushState tracks delayed pushes and shown notifications in memory.
type pushState struct {
	mu        sync.Mutex
	pending   map[pushKey]*pendingPush
	shown     map[pushKey]time.Time
	lastSweep time.Time
	sem       chan struct{}
	// reachable remembers accounts whose phones subscribed since start, so
	// a mention can reach a phone whose app is closed.
	reachable map[string]pushAccount
}

type pushAccount struct {
	nickname string
	userID   int64
}

func newPushState() *pushState {
	return &pushState{
		pending:   make(map[pushKey]*pendingPush),
		shown:     make(map[pushKey]time.Time),
		sem:       make(chan struct{}, pushConcurrency),
		reachable: make(map[string]pushAccount),
	}
}

func (s *TCPServer) pushStore() (PushStore, bool) {
	if s.deps == nil || s.deps.Devices == nil || s.deps.Push == nil || s.push == nil {
		return nil, false
	}
	p, ok := s.deps.Devices.(PushStore)
	return p, ok
}

// pushTopic maps a tag to an RFC 8030 topic, so a newer push for the same
// conversation (including a clear) replaces one the service still holds.
func pushTopic(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return base64.RawURLEncoding.EncodeToString(sum[:24])
}

// desktopActive reports whether a desktop session of the account reported
// input recently.
func (s *TCPServer) desktopActive(principal string, now time.Time) bool {
	for _, c := range s.sessionsOf(principal) {
		if c.isWebTransport() {
			continue
		}
		c.mu.RLock()
		at := c.inputAt
		c.mu.RUnlock()
		if !at.IsZero() && now.Sub(at) < pushDesktopActiveWindow {
			return true
		}
	}
	return false
}

// foregroundWebDevices lists web devices with a session in the foreground;
// they receive the event live and get no push.
func (s *TCPServer) foregroundWebDevices(principal string) map[string]bool {
	out := make(map[string]bool)
	for _, c := range s.sessionsOf(principal) {
		if !c.isWebTransport() {
			continue
		}
		c.mu.RLock()
		id, hidden := c.deviceID, c.webHidden
		c.mu.RUnlock()
		if id != "" && !hidden {
			out[id] = true
		}
	}
	return out
}

// pushNotify notifies the account's web devices in the background. Calls
// push at once; other events wait while a desktop is in use, unless the
// device chose "Always notify this phone".
func (s *TCPServer) pushNotify(principal string, n pushNotice) {
	pushes, ok := s.pushStore()
	if !ok || principal == "" || strings.HasPrefix(principal, "guest:") {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendDeadline)
		defer cancel()
		subs, err := pushes.PushSubscriptions(ctx, principal)
		if err != nil {
			s.logger.Warn("loading push subscriptions failed", zap.Error(err))
			return
		}
		if len(subs) == 0 {
			return
		}
		foreground := s.foregroundWebDevices(principal)
		delay := n.Kind != pushKindCall && s.desktopActive(principal, time.Now())
		now := time.Now()
		for _, sub := range subs {
			if foreground[sub.DeviceID] || pushFiltered(sub.Filters, n, now) {
				continue
			}
			if delay && !sub.AlwaysNotify {
				s.schedulePush(sub, n)
				continue
			}
			s.deliverPush(ctx, sub, n)
		}
	}()
}

// pushFiltered reports whether the device's filters (netproto.PushFilters
// JSON) suppress the notice at now. Clears always pass so read
// conversations disappear even while muted.
func pushFiltered(raw []byte, n pushNotice, now time.Time) bool {
	if n.Kind == pushKindClear || len(raw) == 0 {
		return false
	}
	var f netproto.PushFilters
	if err := json.Unmarshal(raw, &f); err != nil {
		return false
	}
	if f.DNDUntil > now.Unix() || inQuietHours(f, now) {
		return true
	}
	for _, kind := range f.MutedKinds {
		if kind == n.Kind {
			return true
		}
	}
	for _, tag := range f.MutedTags {
		if tag == n.Tag {
			return true
		}
	}
	if n.Kind == pushKindMention {
		if id, ok := strings.CutPrefix(n.Tag, netproto.ReadScopeChannel+":"); ok {
			for _, muted := range f.MutedChannels {
				if strconv.FormatInt(muted, 10) == id {
					return true
				}
			}
		}
	}
	switch f.ChatLevel {
	case "mentions":
		return n.Kind == pushKindConversation
	case "dm":
		return n.Kind == pushKindConversation || n.Kind == pushKindMention
	}
	return false
}

// inQuietHours reports whether now falls in the filters' daily quiet range
// on the phone's wall clock.
func inQuietHours(f netproto.PushFilters, now time.Time) bool {
	from, ok1 := netproto.ClockMinutes(f.QuietFrom)
	to, ok2 := netproto.ClockMinutes(f.QuietTo)
	if !ok1 || !ok2 || from == to {
		return false
	}
	local := now.UTC().Add(time.Duration(f.UTCOffsetMinutes) * time.Minute)
	minute := local.Hour()*60 + local.Minute()
	if from < to {
		return minute >= from && minute < to
	}
	return minute >= from || minute < to
}

// schedulePush delays a push; a later event of the same conversation
// replaces the notice but keeps the original deadline.
func (s *TCPServer) schedulePush(sub store.PushSubscription, n pushNotice) {
	key := pushKey{sub.DeviceID, n.Tag}
	s.push.mu.Lock()
	defer s.push.mu.Unlock()
	if p := s.push.pending[key]; p != nil {
		p.sub, p.notice = sub, n
		return
	}
	p := &pendingPush{sub: sub, notice: n}
	p.timer = time.AfterFunc(pushDesktopDelay, func() {
		s.push.mu.Lock()
		if s.push.pending[key] != p {
			s.push.mu.Unlock()
			return
		}
		delete(s.push.pending, key)
		sub, n := p.sub, p.notice
		s.push.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), pushSendDeadline)
		defer cancel()
		s.deliverPush(ctx, sub, n)
	})
	s.push.pending[key] = p
}

func (s *TCPServer) deliverPush(ctx context.Context, sub store.PushSubscription, n pushNotice) {
	pushes, ok := s.pushStore()
	if !ok {
		return
	}
	// A notice can wait behind desktop activity or another send. Re-read the
	// subscription so a later mute, sign-out or privacy change takes effect.
	current, err := pushes.PushSubscriptionForDevice(ctx, sub.Principal, sub.DeviceID)
	if err != nil || pushFiltered(current.Filters, n, time.Now()) {
		return
	}
	sub = current
	payload := pushPayload{Kind: n.Kind, Tag: n.Tag, Video: n.Video, At: time.Now().Unix()}
	if !sub.HideDetails {
		payload.From, payload.Place = n.From, n.Place
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := webpush.Message{Payload: body, TTL: pushMessageTTL, Urgency: webpush.UrgencyNormal, Topic: pushTopic(n.Tag)}
	switch n.Kind {
	case pushKindCall:
		msg.TTL, msg.Urgency = pushCallTTL, webpush.UrgencyHigh
	case pushKindClear:
		msg.TTL = time.Hour
	}
	select {
	case s.push.sem <- struct{}{}:
		defer func() { <-s.push.sem }()
	case <-ctx.Done():
		return
	}
	err = s.deps.Push.Send(ctx, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth}, msg)
	switch {
	case errors.Is(err, webpush.ErrGone):
		if err := pushes.DeletePushEndpoint(ctx, sub.Endpoint); err != nil {
			s.logger.Warn("dropping expired push subscription failed", zap.Error(err))
		}
		return
	case err != nil:
		s.logger.Debug("push delivery failed", zap.String("device_id", sub.DeviceID), zap.Error(err))
		if err := pushes.RecordPushResult(ctx, sub.Endpoint, false, pushMaxFailures); err != nil {
			s.logger.Warn("recording push failure failed", zap.Error(err))
		}
		return
	}
	_ = pushes.RecordPushResult(ctx, sub.Endpoint, true, pushMaxFailures)
	key := pushKey{sub.DeviceID, n.Tag}
	now := time.Now()
	s.push.mu.Lock()
	defer s.push.mu.Unlock()
	if n.Kind == pushKindClear {
		delete(s.push.shown, key)
		return
	}
	s.push.shown[key] = now
	if now.Sub(s.push.lastSweep) > 10*time.Minute {
		s.push.lastSweep = now
		for k, at := range s.push.shown {
			if now.Sub(at) > pushShownMemory {
				delete(s.push.shown, k)
			}
		}
	}
}

// pushClear cancels delayed pushes for the tag and removes notifications
// already shown, except on skipDevice (the device that read it).
func (s *TCPServer) pushClear(principal, tag, skipDevice string) {
	pushes, ok := s.pushStore()
	if !ok || principal == "" {
		return
	}
	s.push.mu.Lock()
	for key, p := range s.push.pending {
		if key.tag == tag && p.sub.Principal == principal {
			p.timer.Stop()
			delete(s.push.pending, key)
		}
	}
	shown := false
	for key := range s.push.shown {
		if key.tag == tag && key.device != skipDevice {
			shown = true
			break
		}
	}
	s.push.mu.Unlock()
	if !shown {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendDeadline)
		defer cancel()
		subs, err := pushes.PushSubscriptions(ctx, principal)
		if err != nil {
			return
		}
		for _, sub := range subs {
			key := pushKey{sub.DeviceID, tag}
			s.push.mu.Lock()
			_, wasShown := s.push.shown[key]
			s.push.mu.Unlock()
			if wasShown && sub.DeviceID != skipDevice && !silentPushRevokes(sub.Endpoint) {
				s.deliverPush(ctx, sub, pushNotice{Kind: pushKindClear, Tag: tag})
			}
		}
	}()
}

// silentPushRevokes reports push services that end a subscription after
// pushes that show no notification (Apple). Phones there clear read
// conversations when the app next opens instead.
func silentPushRevokes(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && (u.Hostname() == "push.apple.com" || strings.HasSuffix(u.Hostname(), ".push.apple.com"))
}

// readTag maps a read marker to its notification tag.
func readTag(m netproto.ReadMarker) string {
	return m.Scope + ":" + m.ID
}

// syncRead tells the reader's other devices that a conversation was read:
// live sessions get a read event, phones lose the notification.
func (s *TCPServer) syncRead(client *Client, m netproto.ReadMarker) {
	principal := client.uniqueID()
	s.pushClear(principal, readTag(m), client.currentDeviceID())
	event := netproto.DeviceEvent{Event: netproto.DeviceEventRead, Read: &m}
	for _, c := range s.sessionsOf(principal) {
		if c.ID == client.ID {
			continue
		}
		if err := s.writeMessage(c, netproto.MsgDeviceEvent, event); err != nil {
			s.logger.Debug("read sync delivery failed", zap.String("client_id", c.ID), zap.Error(err))
		}
	}
}

// handlePushRequest serves push and activity actions of signed-in devices.
func (s *TCPServer) handlePushRequest(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	principal := client.uniqueID()
	switch r.Action {
	case netproto.DeviceActionActivity:
		client.mu.Lock()
		if client.isWebTransport() {
			client.webHidden = !r.Active
		} else if r.Active {
			client.inputAt = time.Now()
		}
		client.mu.Unlock()
		return nil
	case netproto.DeviceActionRead:
		s.syncRead(client, *r.Read)
		return nil
	}
	deviceID := client.currentDeviceID()
	if !client.isWebTransport() || deviceID == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "notifications are for linked phones")
	}
	pushes, ok := s.pushStore()
	if !ok {
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, Preferences: &netproto.PushPreferences{}})
	}
	if nick := client.nickname(); nick != "" {
		s.push.mu.Lock()
		s.push.reachable[principal] = pushAccount{nickname: nick, userID: client.userID()}
		s.push.mu.Unlock()
	}
	prefs := func() (*netproto.PushPreferences, error) {
		sub, err := pushes.PushSubscriptionForDevice(ctx, principal, deviceID)
		if errors.Is(err, store.ErrDeviceNotFound) {
			return &netproto.PushPreferences{}, nil
		}
		if err != nil {
			return nil, err
		}
		out := &netproto.PushPreferences{Subscribed: true, HideDetails: sub.HideDetails, AlwaysNotify: sub.AlwaysNotify, Filters: &netproto.PushFilters{}}
		if len(sub.Filters) > 0 {
			_ = json.Unmarshal(sub.Filters, out.Filters)
		}
		return out, nil
	}
	saveFilters := func() error {
		if r.Preferences == nil || r.Preferences.Filters == nil {
			return nil
		}
		raw, err := json.Marshal(r.Preferences.Filters)
		if err != nil {
			return err
		}
		if err := pushes.SetPushFilters(ctx, principal, deviceID, raw); err != nil && !errors.Is(err, store.ErrDeviceNotFound) {
			return err
		}
		return nil
	}
	switch r.Action {
	case netproto.DeviceActionPushSubscribe:
		p256dh, err1 := decodeBase64URL(r.Subscription.P256dh)
		auth, err2 := decodeBase64URL(r.Subscription.Auth)
		sub := webpush.Subscription{Endpoint: r.Subscription.Endpoint, P256dh: p256dh, Auth: auth}
		if err1 != nil || err2 != nil || sub.Validate() != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid push subscription")
		}
		record := store.PushSubscription{DeviceID: deviceID, Principal: principal, Endpoint: sub.Endpoint, P256dh: p256dh, Auth: auth}
		if r.Preferences != nil {
			record.HideDetails, record.AlwaysNotify = r.Preferences.HideDetails, r.Preferences.AlwaysNotify
		} else if current, err := pushes.PushSubscriptionForDevice(ctx, principal, deviceID); err == nil {
			record.HideDetails, record.AlwaysNotify = current.HideDetails, current.AlwaysNotify
		}
		if err := pushes.SavePushSubscription(ctx, record); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		if err := saveFilters(); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
	case netproto.DeviceActionPushUnsubscribe:
		if err := pushes.DeletePushSubscription(ctx, principal, deviceID); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
	case netproto.DeviceActionPushPreferences:
		err := pushes.SetPushPreferences(ctx, principal, deviceID, r.Preferences.HideDetails, r.Preferences.AlwaysNotify)
		if err != nil && !errors.Is(err, store.ErrDeviceNotFound) {
			return s.deviceFailure(ctx, client, err)
		}
		if err := saveFilters(); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
	}
	current, err := prefs()
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
		Action: r.Action, VAPIDPublicKey: s.deps.Push.PublicKey(), Preferences: current,
	})
}

func decodeBase64URL(v string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
}

// maxMentionPushes bounds pushes for one channel message.
const maxMentionPushes = 200

// pushMentions notifies mentioned members' phones. mentioned holds members
// resolved among online sessions; phones whose app is closed are matched by
// nickname and must be allowed to view the channel themselves.
func (s *TCPServer) pushMentions(ctx context.Context, client *Client, channelID int64, body string, mentioned ...[]string) {
	if _, ok := s.pushStore(); !ok {
		return
	}
	targets := make(map[string]bool)
	for _, list := range mentioned {
		for _, uid := range list {
			targets[uid] = true
		}
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "@") {
		s.push.mu.Lock()
		candidates := make(map[string]pushAccount)
		for principal, account := range s.push.reachable {
			if !targets[principal] && account.nickname != "" && strings.Contains(lower, "@"+strings.ToLower(account.nickname)) {
				candidates[principal] = account
			}
		}
		s.push.mu.Unlock()
		if len(candidates) > 0 {
			_ = s.withRolePolicy(ctx, func(ctx context.Context) error {
				lease, ok := ctx.Value(roleLeaseKey{}).(roleLease)
				if !ok {
					return nil
				}
				for principal, account := range candidates {
					if account.userID > 0 && lease.evaluator.Evaluate(account.userID, channelID, authorization.ViewChannel).Allowed {
						targets[principal] = true
					}
				}
				return nil
			})
		}
	}
	delete(targets, client.uniqueID())
	place := ""
	if s.deps.State != nil && channelID != 0 {
		if ch, ok := s.deps.State.GetChannel(channelID); ok {
			place = ch.Name
		}
	}
	tag := netproto.ReadScopeChannel + ":" + strconv.FormatInt(channelID, 10)
	sent := 0
	for uid := range targets {
		if sent == maxMentionPushes {
			break
		}
		s.pushNotify(uid, pushNotice{Kind: pushKindMention, Tag: tag, From: client.nickname(), Place: place})
		sent++
	}
}
