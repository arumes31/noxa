package channels

import (
	"context"
	"fmt"

	"noxa/internal/state"
)

const channelStateSelect = `SELECT id, COALESCE(parent_id, 0), name, COALESCE(topic, ''),
    order_index, channel_type, COALESCE(max_clients, 0), created_at,
    COALESCE(password_hash, ''),
    opus_bitrate, opus_fec, opus_dtx, opus_stereo, slow_mode_seconds,
    COALESCE(description, '') FROM channels`

func scanChannelState(row interface{ Scan(...any) error }) (*state.Channel, error) {
	var ch state.Channel
	var channelType int16
	if err := row.Scan(&ch.ChannelID, &ch.ParentID, &ch.Name, &ch.Topic,
		&ch.OrderIndex, &channelType, &ch.MaxClients, &ch.CreatedAt,
		&ch.PasswordHash, &ch.OpusBitrate, &ch.OpusFEC,
		&ch.OpusDTX, &ch.OpusStereo, &ch.SlowModeSeconds, &ch.Description); err != nil {
		return nil, err
	}
	parsed, err := ParseChannelType(int(channelType))
	if err != nil {
		return nil, fmt.Errorf("channel %d has invalid stored type %d: %w", ch.ChannelID, channelType, err)
	}
	ch.ChannelType = int(parsed)
	return &ch, nil
}

func (m *ChannelManager) loadChannelStates(ctx context.Context) ([]*state.Channel, error) {
	rows, err := m.store.DB().QueryContext(ctx, channelStateSelect+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var channels []*state.Channel
	for rows.Next() {
		ch, err := scanChannelState(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}
