package state

// UpdateVoiceQueue assigns FIFO positions atomically using server sequence
// numbers. Repeated raises keep their place. Callers authorize moderation.
func (m *Manager) UpdateVoiceQueue(channelID int64, clientID, action string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	channel, exists := m.channels[channelID]
	if !exists || channelID <= 0 {
		return "", ErrChannelNotFound
	}
	if channel.TextOnly {
		return "", ErrTextChannel
	}
	if action == "next" || action == "clear" {
		var first *Client
		for _, member := range m.clients {
			if member.ChannelID != channelID {
				continue
			}
			if action == "clear" {
				member.HandRaisedOrder = 0
				member.HandCalled = false
				continue
			}
			if member.HandRaisedOrder > 0 && (first == nil || member.HandRaisedOrder < first.HandRaisedOrder) {
				first = member
			}
		}
		if first != nil {
			for _, member := range m.clients {
				if member.ChannelID == channelID {
					member.HandCalled = false
				}
			}
			first.HandRaisedOrder = 0
			first.HandCalled = true
			return first.ClientID, nil
		}
		return "", nil
	}
	member, exists := m.clients[clientID]
	if !exists {
		return "", ErrClientNotFound
	}
	if member.ChannelID != channelID {
		return "", ErrNotInChannel
	}
	if action == "raise" || action == "lower" {
		member.HandCalled = false
	}
	if action == "raise" && member.HandRaisedOrder == 0 {
		m.handSequence++
		member.HandRaisedOrder = m.handSequence
	} else if action == "lower" {
		member.HandRaisedOrder = 0
	}
	return member.ClientID, nil
}
