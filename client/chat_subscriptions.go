// Channel subscription requests and the authoritative local subscription view.
package main

import (
	"encoding/json"

	"noxa/internal/netproto"
)

// SubscribeChannels asks the server to (un)subscribe the given channels. The
// answer is never returned here: the server replies with the authoritative
// full set on MsgSubscriptionState, which arrives on the read loop as the
// "subscriptions" event. Returning a set from here would give the UI a second
// source of truth that could disagree with it.
func (a *App) SubscribeChannels(channelIDs []int64, subscribe bool) string {
	if len(channelIDs) == 0 {
		return "no channels given"
	}
	m := a.cmLoad()
	if m == nil {
		return "not connected"
	}
	return m.subscribeChannels(channelIDs, subscribe)
}

// SubscribeChannelsForTab submits a change to the displayed server. The
// subscriptions event remains the sole source of authoritative membership.
func (a *App) SubscribeChannelsForTab(tabID string, channelIDs []int64, subscribe bool) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return m.subscribeChannels(channelIDs, subscribe)
}

func (m *connManager) subscribeChannels(channelIDs []int64, subscribe bool) string {
	if len(channelIDs) == 0 {
		return "no channels given"
	}
	if err := m.write(netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{
		ChannelIDs: channelIDs, Subscribe: subscribe,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// Subscriptions returns the newest authoritative subscription set the server
// pushed on this connection. A background server tab drops live events, so
// this is how a tab switch recovers the set without a round trip (281/312).
func (a *App) Subscriptions() []int64 {
	m := a.cmLoad()
	if m == nil {
		return []int64{}
	}
	return m.subscriptions()
}

// SubscriptionsForTab reads the cached subscription set of the displayed server.
func (a *App) SubscriptionsForTab(tabID string) ([]int64, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	return m.subscriptions(), nil
}

func (m *connManager) subscriptions() []int64 {
	m.mu.Lock()
	raw := m.lastSubscriptions
	m.mu.Unlock()
	out := []int64{}
	if raw == "" {
		return out
	}
	var st netproto.SubscriptionState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return out
	}
	return append(out, st.ChannelIDs...)
}
