// Package integrationevents defines the filtered event wire contract shared by
// role-aware integration transports. Authorization remains in the native backend.
package integrationevents

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"noxa/internal/broadcast"
	noxav1 "noxa/v1"
)

const MaxMessageBytes = 8 << 20
const maxItems = 10000

var ErrResponseTooLarge = errors.New("integration event exceeds limit")

// DeliveryState belongs to one subscription and must be used serially. It
// retains only hashes and a local sequence, never protected projection data.
type DeliveryState struct {
	sequence uint64
	topology [sha256.Size]byte
	activity map[[sha256.Size]byte]bool
	known    bool
}

// Send converts a current-policy projection and suppresses unchanged snapshots.
// It must run inside the backend callback, and deliver must finish the bounded
// socket write before returning. State advances only after successful delivery.
func (s *DeliveryState) Send(ctx context.Context, projection broadcast.IntegrationEvent, wantSpeaking bool, deliver func(context.Context, *noxav1.Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var message *noxav1.Event
	var digest [sha256.Size]byte
	var activity map[[sha256.Size]byte]bool
	isSnapshot := projection.Snapshot != nil
	switch {
	case isSnapshot:
		snapshot, err := snapshotProto(ctx, projection.Snapshot)
		if err != nil {
			return err
		}
		if proto.Size(snapshot) > MaxMessageBytes {
			return ErrResponseTooLarge
		}
		activity = make(map[[sha256.Size]byte]bool, len(snapshot.SpeakingClientIds))
		for _, id := range snapshot.SpeakingClientIds {
			activity[sha256.Sum256([]byte(id))] = true
		}
		// Track topology separately so speaking deltas can update the known
		// baseline without exposing the next hidden structural event's timing.
		speakingIDs := snapshot.SpeakingClientIds
		snapshot.SpeakingClientIds = nil
		data, err := proto.Marshal(snapshot)
		snapshot.SpeakingClientIds = speakingIDs
		if err != nil {
			return err
		}
		digest = sha256.Sum256(data)
		if s.known && digest == s.topology && maps.Equal(activity, s.activity) {
			return nil
		}
		message = &noxav1.Event{Type: noxav1.EventType_EVENT_TYPE_ROLE_SNAPSHOT, Payload: &noxav1.Event_RoleSnapshot{RoleSnapshot: snapshot}}
	case projection.Speaking != nil && wantSpeaking:
		activity := projection.Speaking
		if !s.known || (activity.Speaking && len(s.activity) >= maxItems && !s.activity[sha256.Sum256([]byte(activity.ClientID))]) {
			return ErrResponseTooLarge
		}
		message = &noxav1.Event{Type: noxav1.EventType_EVENT_TYPE_USER_SPEAKING, Payload: &noxav1.Event_UserSpeaking{UserSpeaking: &noxav1.UserSpeakingEvent{ChannelId: strconv.FormatInt(activity.ChannelID, 10), UserId: activity.ClientID, Speaking: activity.Speaking}}}
	default:
		return nil
	}
	message.Id, message.Timestamp = strconv.FormatUint(s.sequence+1, 10), time.Now().UnixMilli()
	if proto.Size(message) > MaxMessageBytes {
		return ErrResponseTooLarge
	}
	if err := deliver(ctx, message); err != nil {
		return err
	}
	s.sequence++
	if isSnapshot {
		s.topology, s.known, s.activity = digest, true, activity
	} else {
		id := sha256.Sum256([]byte(projection.Speaking.ClientID))
		if projection.Speaking.Speaking {
			s.activity[id] = true
		} else {
			delete(s.activity, id)
		}
	}
	return nil
}

func snapshotProto(ctx context.Context, snapshot *broadcast.TreeSnapshot) (*noxav1.RoleSnapshotEvent, error) {
	result := &noxav1.RoleSnapshotEvent{}
	add := func(clients []*broadcast.ClientInfo) error {
		for _, client := range clients {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(result.Clients)+len(result.Channels) >= maxItems {
				return ErrResponseTooLarge
			}
			result.Clients = append(result.Clients, &noxav1.VisibleClient{ClientId: client.ClientID, UniqueId: client.UniqueID, Nickname: client.Nickname, ChannelId: client.ChannelID})
			if client.IsSpeaking {
				result.SpeakingClientIds = append(result.SpeakingClientIds, client.ClientID)
			}
		}
		return nil
	}
	if err := add(snapshot.UnassignedClients); err != nil {
		return nil, err
	}
	stack := slices.Clone(snapshot.RootChannels)
	for len(stack) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(result.Clients)+len(result.Channels) >= maxItems {
			return nil, ErrResponseTooLarge
		}
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result.Channels = append(result.Channels, &noxav1.GetChannelInfoResponse{ChannelId: node.ChannelID, ParentId: node.ParentID, Name: node.Name, Topic: node.Topic, ChannelType: int64(node.ChannelType), MaxClients: int64(node.MaxClients), CurrentClients: int64(node.ClientCount), OpusBitrate: int64(node.OpusBitrate), OpusFec: node.OpusFEC, OpusDtx: node.OpusDTX, OpusStereo: node.OpusStereo, SlowModeSeconds: int64(node.SlowModeSeconds)})
		if err := add(node.Clients); err != nil {
			return nil, err
		}
		stack = append(stack, node.Children...)
	}
	slices.SortFunc(result.Channels, func(a, b *noxav1.GetChannelInfoResponse) int { return cmp.Compare(a.ChannelId, b.ChannelId) })
	slices.SortFunc(result.Clients, func(a, b *noxav1.VisibleClient) int { return strings.Compare(a.ClientId, b.ClientId) })
	slices.Sort(result.SpeakingClientIds)
	return result, nil
}
